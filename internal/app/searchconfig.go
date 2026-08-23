package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"git-ctx/internal/embedding"
	"git-ctx/internal/opensearch"
	"git-ctx/internal/rerank"
	"git-ctx/internal/search"
	secretstore "git-ctx/internal/secret"
	"git-ctx/internal/vectorstore"
)

// Search configuration and the retrieval backends it selects: embeddings,
// reranking, OpenSearch and the vector store.

func embeddingConfigFingerprint(value map[string]any) string {
	keys := []string{"provider", "baseUrl", "model", "apiKey"}
	digest := sha256.New()
	for _, key := range keys {
		_, _ = fmt.Fprintf(digest, "%s=%v\n", key, value[key])
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func retrievalModeFromSetting(value map[string]any) string {
	if mode, ok := value["retrievalMode"].(string); ok {
		return search.NormalizeRetrievalMode(mode)
	}
	if enabled, ok := value["embeddingEnabled"].(bool); ok && !enabled {
		return search.RetrievalKeywordOnly
	}
	return search.RetrievalHybridFallback
}

type reindexTarget struct{ repository, ref string }

// enqueueRetrievalReindex safely moves every known ref toward the new vector
// policy. Existing answers remain active until each staging generation commits.
func (a *App) enqueueRetrievalReindex(ctx context.Context) (int, error) {
	rows, err := a.store.DB.QueryContext(ctx, `SELECT DISTINCT r.id,COALESCE(NULLIF(s.ref_name,''),r.default_branch)
FROM repositories r LEFT JOIN repository_ref_states s ON s.repository_id=r.id
WHERE r.enabled=1 ORDER BY r.id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var targets []reindexTarget
	for rows.Next() {
		var item reindexTarget
		if err = rows.Scan(&item.repository, &item.ref); err != nil {
			return 0, err
		}
		if item.ref != "" {
			targets = append(targets, item)
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	return a.enqueueReindexTargets(ctx, targets, "retrieval-policy", "policy:")
}

// enqueueEmbeddingRevisionReindex repairs refs created by an earlier model,
// endpoint, or the legacy NUL-delimited revision format. This runs at startup
// after an upgrade, so search stays lexical-safe while the worker converges the
// corpus without waiting for a source commit or a manual setting change.
func (a *App) enqueueEmbeddingRevisionReindex(ctx context.Context, revision string) (int, error) {
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(`SELECT r.id,s.ref_name
FROM repositories r JOIN repository_ref_states s ON s.repository_id=r.id
WHERE r.enabled=1 AND s.embedding_revision<>? ORDER BY r.id,s.ref_name`), revision)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var targets []reindexTarget
	for rows.Next() {
		var item reindexTarget
		if err = rows.Scan(&item.repository, &item.ref); err != nil {
			return 0, err
		}
		if item.ref != "" {
			targets = append(targets, item)
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	return a.enqueueReindexTargets(ctx, targets, "embedding-revision", "embedding:")
}

func (a *App) enqueueReindexTargets(ctx context.Context, targets []reindexTarget, kind, prefix string) (int, error) {
	queued := 0
	for _, item := range targets {
		var existing string
		err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT id FROM index_jobs WHERE repository_id=? AND ref_name=? AND status IN ('pending','running') LIMIT 1`), item.repository, item.ref).Scan(&existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return queued, err
		}
		id, tokenErr := randomToken(16)
		if tokenErr != nil {
			return queued, tokenErr
		}
		_, err = a.store.DB.ExecContext(ctx, a.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,next_run_at) VALUES(?,?,?,?, 'pending',?)`),
			prefix+id, item.repository, item.ref, kind, time.Now().UTC())
		if err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

func (a *App) searchConfig(ctx context.Context) search.Config {
	cfg := search.Config{
		KeywordWeight: 1, VectorWeight: .35, FinalK: 8, CandidateLimit: 5000,
		RetrievalMode: search.RetrievalHybridFallback, MinimumEmbeddingCoverage: 80,
	}
	modelSettings, modelErr := a.loadSettingMap(ctx, "model")
	provider, _ := modelSettings["provider"].(string)
	noRemoteModel := modelErr != nil || provider != "openai-compatible"
	if !noRemoteModel {
		cfg.EmbeddingRevision = configuredEmbeddingRevision(modelSettings)
	}
	settings, err := a.loadSettingMap(ctx, "search")
	if err != nil {
		if noRemoteModel {
			cfg.VectorWeight = 0
			cfg.SourceQuerySearch = true
			cfg.RetrievalMode = search.RetrievalKeywordOnly
		}
		return cfg
	}
	if value, ok := settings["retrievalMode"].(string); ok {
		cfg.RetrievalMode = search.NormalizeRetrievalMode(value)
	} else if enabled, ok := settings["embeddingEnabled"].(bool); ok && !enabled {
		// Backward-compatible import of the short-lived boolean setting.
		cfg.RetrievalMode = search.RetrievalKeywordOnly
	}
	if value, ok := settings["keywordWeight"].(float64); ok {
		cfg.KeywordWeight = value
	}
	if value, ok := settings["vectorWeight"].(float64); ok {
		cfg.VectorWeight = value
	}
	if value, ok := settings["finalK"].(float64); ok {
		cfg.FinalK = int(value)
	}
	if value, ok := settings["candidateLimit"].(float64); ok {
		cfg.CandidateLimit = int(value)
	}
	if value, ok := settings["rerankLimit"].(float64); ok {
		cfg.RerankLimit = int(value)
	}
	if value, ok := settings["minimumEmbeddingCoveragePercent"].(float64); ok {
		cfg.MinimumEmbeddingCoverage = value
	}
	if cfg.RetrievalMode == search.RetrievalKeywordOnly || (noRemoteModel && cfg.RetrievalMode == search.RetrievalHybridFallback) {
		cfg.VectorWeight = 0
		cfg.SourceQuerySearch = true
		if noRemoteModel {
			cfg.RetrievalMode = search.RetrievalKeywordOnly
		}
	}
	return cfg
}

func (a *App) requestedRetrievalMode(ctx context.Context) string {
	settings, err := a.loadSettingMap(ctx, "search")
	if err != nil {
		return search.RetrievalHybridFallback
	}
	return retrievalModeFromSetting(settings)
}

func (a *App) embeddingRuntimePolicy(ctx context.Context) embedding.RuntimePolicy {
	policy := embedding.RuntimePolicy{
		FailureThreshold: 2, Cooldown: time.Minute, CacheTTL: 2 * time.Minute, CacheEntries: 1000,
	}
	settings, err := a.loadSettingMap(ctx, "search")
	if err != nil {
		return policy
	}
	if value, ok := settings["embeddingFailureThreshold"].(float64); ok {
		policy.FailureThreshold = int(value)
	}
	if value, ok := settings["embeddingCooldownSeconds"].(float64); ok {
		policy.Cooldown = time.Duration(value * float64(time.Second))
	}
	if value, ok := settings["embeddingCacheSeconds"].(float64); ok {
		policy.CacheTTL = time.Duration(value * float64(time.Second))
	}
	return policy
}

func embeddingRuntimeIdentity(settings map[string]any) string {
	provider, _ := settings["provider"].(string)
	model, _ := settings["model"].(string)
	baseURL, _ := settings["baseUrl"].(string)
	if provider == "" {
		return "unconfigured"
	}
	return provider + ":" + model + "@" + strings.TrimRight(baseURL, "/")
}

func configuredEmbeddingRevision(settings map[string]any) string {
	provider, err := embeddingProviderFromMap(settings)
	if err != nil {
		return ""
	}
	if metadata, ok := provider.(embedding.MetadataProvider); ok {
		return metadata.EmbeddingMetadata().Identity()
	}
	return ""
}

func (a *App) retrievalMode(ctx context.Context) string {
	return a.searchConfig(ctx).RetrievalMode
}

func (a *App) strictMCPCompatibility(ctx context.Context) bool {
	settings, err := a.loadSettingMap(ctx, "mcp")
	if err != nil {
		return false
	}
	strict, _ := settings["strictCompatibility"].(bool)
	return strict
}
func (a *App) rerankerProvider(ctx context.Context) (rerank.Provider, error) {
	settings, err := a.loadSettingMap(ctx, "model")
	if err != nil {
		return nil, nil
	}
	return rerankerProviderFromMap(settings)
}
func rerankerProviderFromMap(settings map[string]any) (rerank.Provider, error) {
	enabled, _ := settings["rerankerEnabled"].(bool)
	if !enabled {
		return nil, nil
	}
	provider, _ := settings["rerankerProvider"].(string)
	if provider != "openai-compatible" {
		return nil, errors.New("model.rerankerProvider must be openai-compatible when enabled")
	}
	baseURL, _ := settings["rerankerBaseUrl"].(string)
	model, _ := settings["rerankerModel"].(string)
	apiKey, _ := settings["rerankerApiKey"].(string)
	timeout := 15 * time.Second
	if seconds, ok := settings["rerankerTimeoutSeconds"].(float64); ok && seconds > 0 {
		timeout = time.Duration(seconds * float64(time.Second))
	}
	var tlsVerify *bool
	if value, ok := settings["tlsVerify"].(bool); ok {
		tlsVerify = &value
	}
	ca, _ := settings["caCertificate"].(string)
	proxy, _ := settings["proxyUrl"].(string)
	return rerank.NewOpenAI(rerank.OpenAIConfig{BaseURL: baseURL, Model: model, APIKey: apiKey, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: ca, ProxyURL: proxy})
}
func (a *App) embeddingProvider(ctx context.Context) (embedding.Provider, error) {
	settings, err := a.loadSettingMap(ctx, "model")
	if err != nil {
		return embedding.Local{}, nil
	}
	return embeddingProviderFromMap(settings)
}

// semanticEmbeddingProvider deliberately excludes the legacy hash-based local
// provider. It is useful for deterministic unit tests, but it must never be
// mixed with vectors produced by a configured model.
func (a *App) semanticEmbeddingProvider(ctx context.Context) (embedding.Provider, error) {
	settings, err := a.loadSettingMap(ctx, "model")
	if err != nil {
		return nil, errors.New("embedding model is not configured")
	}
	provider, _ := settings["provider"].(string)
	if provider != "openai-compatible" {
		return nil, errors.New("an OpenAI-compatible embedding model is not configured")
	}
	identity := embeddingRuntimeIdentity(settings)
	if a.embeddingRuntime == nil {
		return embeddingProviderFromMap(settings)
	}
	return a.embeddingRuntime.Guard(identity, a.embeddingRuntimePolicy(ctx), func() (embedding.Provider, error) {
		return embeddingProviderFromMap(settings)
	})
}

func openSearchConfigFromMap(settings map[string]any) (opensearch.Config, error) {
	cfg := opensearch.Config{Index: "git-ctx-chunks", Timeout: 30 * time.Second}
	cfg.Enabled, _ = settings["enabled"].(bool)
	cfg.BaseURL, _ = settings["baseUrl"].(string)
	if value, ok := settings["index"].(string); ok && value != "" {
		cfg.Index = value
	}
	cfg.Username, _ = settings["username"].(string)
	cfg.Password, _ = settings["password"].(string)
	cfg.APIKey, _ = settings["apiKey"].(string)
	if seconds, ok := settings["timeoutSeconds"].(float64); ok && seconds > 0 {
		cfg.Timeout = time.Duration(seconds * float64(time.Second))
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	cfg.CACertificate, _ = settings["caCertificate"].(string)
	cfg.ProxyURL, _ = settings["proxyUrl"].(string)
	if cfg.Enabled && strings.TrimSpace(cfg.BaseURL) == "" {
		return cfg, errors.New("opensearch.baseUrl is required when enabled")
	}
	return cfg, nil
}

type vaultSetting struct {
	Enabled bool
	secretstore.VaultConfig
}

func vaultConfigFromMap(settings map[string]any) (vaultSetting, error) {
	cfg := vaultSetting{VaultConfig: secretstore.VaultConfig{Mount: "secret", Prefix: "git-ctx", Timeout: 30 * time.Second}}
	cfg.Enabled, _ = settings["enabled"].(bool)
	cfg.BaseURL, _ = settings["baseUrl"].(string)
	cfg.Token, _ = settings["token"].(string)
	cfg.Namespace, _ = settings["namespace"].(string)
	if value, ok := settings["mount"].(string); ok && value != "" {
		cfg.Mount = value
	}
	if value, ok := settings["prefix"].(string); ok && value != "" {
		cfg.Prefix = value
	}
	if seconds, ok := settings["timeoutSeconds"].(float64); ok && seconds > 0 {
		cfg.Timeout = time.Duration(seconds * float64(time.Second))
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	cfg.CACertificate, _ = settings["caCertificate"].(string)
	cfg.ProxyURL, _ = settings["proxyUrl"].(string)
	if cfg.Enabled && (strings.TrimSpace(cfg.BaseURL) == "" || cfg.Token == "") {
		return cfg, errors.New("vault.baseUrl and vault.token are required when enabled")
	}
	return cfg, nil
}

func (a *App) vaultClient(ctx context.Context) (*secretstore.Vault, error) {
	settings, err := a.loadSettingMapRaw(ctx, "vault")
	if err != nil {
		return nil, errors.New("vault backend is not configured")
	}
	cfg, err := vaultConfigFromMap(settings)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("vault backend is disabled")
	}
	return secretstore.NewVault(cfg.VaultConfig)
}

func (a *App) openSearchClient(ctx context.Context) (*opensearch.Client, bool, error) {
	settings, err := a.loadSettingMap(ctx, "opensearch")
	if err != nil {
		return nil, false, nil
	}
	cfg, err := openSearchConfigFromMap(settings)
	if err != nil || !cfg.Enabled {
		return nil, cfg.Enabled, err
	}
	client, err := opensearch.New(cfg)
	return client, true, err
}

func (a *App) projectOpenSearch(ctx context.Context, repositoryID, ref string) error {
	client, enabled, err := a.openSearchClient(ctx)
	if err != nil || !enabled {
		return err
	}
	return client.SyncRef(ctx, a.store, repositoryID, ref)
}

// vectorStore opens the configured vector database. The zero configuration case
// is not an error: git-ctx scores the embeddings stored beside the text instead.
func (a *App) vectorStore(ctx context.Context) (vectorstore.Store, error) {
	settings, err := a.loadSettingMap(ctx, "vector")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, vectorstore.ErrNotConfigured
		}
		return nil, err
	}
	cfg := vectorstore.FromMap(settings)
	if !cfg.Enabled() {
		return nil, vectorstore.ErrNotConfigured
	}
	return vectorstore.Open(cfg, a.postgresDSN(ctx))
}

// postgresDSN returns the platform database DSN when it is PostgreSQL, so a
// pgvector deployment can reuse it instead of storing a second credential.
func (a *App) postgresDSN(ctx context.Context) string {
	if a.store.Driver() != "postgres" {
		return ""
	}
	if dsn, err := configuredDatabaseDSN(ctx, a.store, a.aead); err == nil && dsn != "" {
		return dsn
	}
	return a.cfg.DatabaseDSN
}

// projectSearchStores keeps every optional search backend in step with a ref
// that finished indexing. A failure here retries the job, so both projections
// report their errors.
func (a *App) projectSearchStores(ctx context.Context, repositoryID, ref string) error {
	if err := a.projectOpenSearch(ctx, repositoryID, ref); err != nil {
		return err
	}
	return a.projectVectors(ctx, repositoryID, ref)
}

// projectVectors republishes the embeddings of one ref to the vector database.
func (a *App) projectVectors(ctx context.Context, repositoryID, ref string) error {
	if !a.searchConfig(ctx).UsesEmbeddings() {
		return nil
	}
	store, err := a.vectorStore(ctx)
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vector database: %w", err)
	}
	defer store.Close()
	if err = store.DeleteRef(ctx, repositoryID, ref); err != nil {
		return fmt.Errorf("vector database: %w", err)
	}
	_, err = a.streamVectors(ctx, store, repositoryID, ref)
	if err != nil {
		return fmt.Errorf("vector database: %w", err)
	}
	return nil
}

// streamVectors reads stored embeddings in batches and upserts them, so a full
// rebuild never loads the whole corpus into memory.
func (a *App) streamVectors(ctx context.Context, store vectorstore.Store, repositoryID, ref string) (int, error) {
	statement := `SELECT c.id,c.repository_id,c.ref_name,COALESCE(r.library_id,''),c.file_path,COALESCE(c.embedding_revision,''),c.embedding
FROM document_chunks c LEFT JOIN repositories r ON r.id=c.repository_id
WHERE c.embedding IS NOT NULL`
	var args []any
	if repositoryID != "" {
		statement += ` AND c.repository_id=?`
		args = append(args, repositoryID)
	}
	if ref != "" {
		statement += ` AND c.ref_name=?`
		args = append(args, ref)
	}
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(statement), args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	batch := make([]vectorstore.Chunk, 0, 500)
	total := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := store.Upsert(ctx, batch); err != nil {
			return err
		}
		total += len(batch)
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		var chunk vectorstore.Chunk
		var raw []byte
		if err = rows.Scan(&chunk.ID, &chunk.RepositoryID, &chunk.Ref, &chunk.LibraryID, &chunk.FilePath, &chunk.Revision, &raw); err != nil {
			return total, err
		}
		chunk.Vector = embedding.Decode(raw)
		if len(chunk.Vector) == 0 {
			continue
		}
		batch = append(batch, chunk)
		if len(batch) == cap(batch) {
			if err = flush(); err != nil {
				return total, err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return total, err
	}
	return total, flush()
}

// vectorCandidates is the search side of the integration.
func (a *App) vectorCandidates(ctx context.Context, repositoryID, ref, query string, limit int) ([]search.VectorCandidate, error) {
	cfg := a.searchConfig(ctx)
	if !cfg.UsesEmbeddings() {
		return nil, nil
	}
	store, err := a.vectorStore(ctx)
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer store.Close()
	provider, providerErr := a.semanticEmbeddingProvider(ctx)
	if providerErr != nil {
		return nil, providerErr
	}
	vector, embedErr := provider.Embed(ctx, query)
	if embedErr != nil {
		return nil, embedErr
	}
	matches, searchErr := store.Search(ctx, repositoryID, ref, cfg.EmbeddingRevision, vector, limit)
	if searchErr != nil {
		return nil, searchErr
	}
	out := make([]search.VectorCandidate, 0, len(matches))
	for _, match := range matches {
		out = append(out, search.VectorCandidate{ID: match.ID, Score: match.Score})
	}
	return out, nil
}

// globalVectorCandidates asks the vector database for nearest neighbours across
// every repository. The repository ACL is applied by the caller afterwards.
func (a *App) globalVectorCandidates(ctx context.Context, query string, limit int) ([]search.VectorCandidate, error) {
	cfg := a.searchConfig(ctx)
	if !cfg.UsesEmbeddings() {
		return nil, nil
	}
	store, err := a.vectorStore(ctx)
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer store.Close()
	provider, providerErr := a.semanticEmbeddingProvider(ctx)
	if providerErr != nil {
		return nil, providerErr
	}
	vector, embedErr := provider.Embed(ctx, query)
	if embedErr != nil {
		return nil, embedErr
	}
	matches, searchErr := store.SearchGlobal(ctx, cfg.EmbeddingRevision, vector, limit)
	if searchErr != nil {
		return nil, searchErr
	}
	out := make([]search.VectorCandidate, 0, len(matches))
	for _, match := range matches {
		out = append(out, search.VectorCandidate{ID: match.ID, Score: match.Score})
	}
	return out, nil
}

func (a *App) openSearchCandidates(ctx context.Context, repositoryID, ref string, principals []string, query string, limit int) ([]search.KeywordCandidate, error) {
	client, enabled, err := a.openSearchClient(ctx)
	if err != nil {
		return nil, err
	}
	if enabled {
		candidates, searchErr := client.Search(ctx, repositoryID, ref, principals, query, limit)
		if searchErr != nil {
			return nil, searchErr
		}
		out := make([]search.KeywordCandidate, len(candidates))
		for i, candidate := range candidates {
			out[i] = search.KeywordCandidate{ID: candidate.ID, Score: candidate.Score}
		}
		return out, nil
	}
	if a.store.Driver() != "postgres" || len(principals) == 0 {
		return nil, nil
	}
	if limit < 1 || limit > 20000 {
		limit = 5000
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(`SELECT c.id,
ts_rank_cd(to_tsvector('simple',COALESCE(c.heading,'') || ' ' || COALESCE(c.content,'')),plainto_tsquery('simple',?))
FROM document_chunks c WHERE c.repository_id=? AND c.ref_name=? AND
to_tsvector('simple',COALESCE(c.heading,'') || ' ' || COALESCE(c.content,'')) @@ plainto_tsquery('simple',?) AND
EXISTS (SELECT 1 FROM repository_permissions p WHERE p.repository_id=c.repository_id AND (p.principal IN (`+placeholders+`) OR p.principal='*'))
ORDER BY 2 DESC LIMIT ?`), append([]any{query, repositoryID, ref, query}, append(principalArgs(principals), limit)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []search.KeywordCandidate
	for rows.Next() {
		var item search.KeywordCandidate
		if err = rows.Scan(&item.ID, &item.Score); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func principalArgs(principals []string) []any {
	out := make([]any, len(principals))
	for index, principal := range principals {
		out[index] = principal
	}
	return out
}
func embeddingProviderFromMap(settings map[string]any) (embedding.Provider, error) {
	provider, _ := settings["provider"].(string)
	if provider == "" || provider == "local" {
		return embedding.Local{}, nil
	}
	if provider != "openai-compatible" {
		return nil, errors.New("model.provider must be local or openai-compatible")
	}
	baseURL, _ := settings["baseUrl"].(string)
	model, _ := settings["model"].(string)
	apiKey, _ := settings["apiKey"].(string)
	timeout := 30 * time.Second
	if seconds, ok := settings["timeoutSeconds"].(float64); ok && seconds > 0 {
		timeout = time.Duration(seconds * float64(time.Second))
	}
	var tlsVerify *bool
	if value, ok := settings["tlsVerify"].(bool); ok {
		tlsVerify = &value
	}
	ca, _ := settings["caCertificate"].(string)
	proxy, _ := settings["proxyUrl"].(string)
	return embedding.NewOpenAI(embedding.OpenAIConfig{BaseURL: baseURL, Model: model, APIKey: apiKey, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: ca, ProxyURL: proxy})
}

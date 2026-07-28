package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"git-ctx/internal/embedding"
	"git-ctx/internal/rerank"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type Config struct {
	KeywordWeight     float64
	VectorWeight      float64
	FinalK            int
	CandidateLimit    int
	RerankLimit       int
	SourceQuerySearch bool
}
type ConfigLoader func(context.Context) Config
type EmbeddingLoader func(context.Context) embedding.Provider
type RerankerLoader func(context.Context) rerank.Provider
type KeywordCandidate struct {
	ID    string
	Score float64
}
type KeywordLoader func(context.Context, string, string, []string, string, int) ([]KeywordCandidate, error)
type Service struct {
	store    *store.Store
	load     ConfigLoader
	embedder EmbeddingLoader
	reranker RerankerLoader
	sources  func(context.Context, string) (source.RepositorySource, error)
	keyword  KeywordLoader
}

func (s *Service) SetKeywordLoader(loader KeywordLoader) { s.keyword = loader }

func (s *Service) SetRerankerLoader(loader RerankerLoader) {
	if loader != nil {
		s.reranker = loader
	}
}
func (s *Service) SetSourceLoader(loader func(context.Context, string) (source.RepositorySource, error)) {
	s.sources = loader
}

func New(s *store.Store) *Service {
	return &Service{store: s, load: func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 8, CandidateLimit: 5000}
	}, embedder: func(context.Context) embedding.Provider { return embedding.Local{} }}
}
func (s *Service) SetEmbeddingLoader(loader EmbeddingLoader) {
	if loader != nil {
		s.embedder = loader
	}
}
func (s *Service) SetConfigLoader(loader ConfigLoader) {
	if loader != nil {
		s.load = loader
	}
}

type Library struct {
	Name, ID, Description, Reputation string
	Snippets                          int
	Versions                          []string
}

type RepositoryResult struct {
	ID, ProjectKey, Slug, Name, Description, LibraryID, DefaultBranch, SourceType string
	IndexedAt                                                                     sql.NullTime
}

type SourceResult struct {
	LibraryID, SourceType, ProjectKey, RepositorySlug, Ref string
	source.QueryResult
}

type SymbolResult struct {
	LibraryID, Ref, CommitID, FilePath, Name, QualifiedName, Kind, Language, Signature, Documentation string
	LineStart, LineEnd                                                                                int
	Content                                                                                           string
}

type RepositoryMap struct {
	LibraryID, Ref, CommitID, SummaryJSON string
}

type DependencyResult struct {
	LibraryID, Ref, CommitID, FilePath, FromSymbol, Target, Kind string
	LineNumber                                                   int
}

type RefChange struct {
	Type, Name, Kind, FilePath, BeforeSignature, AfterSignature string
}

type RefComparison struct {
	LibraryID, BaseRef, HeadRef string
	Changes                     []RefChange
}

type ChangeImpact struct {
	Comparison RefComparison
	Dependents []DependencyResult
}

func (s *Service) RepositoryMap(ctx context.Context, principals []string, libraryID, requestedRef string) (RepositoryMap, error) {
	baseID, version, ok := splitLibraryID(libraryID)
	if !ok || len(principals) == 0 {
		return RepositoryMap{}, errors.New("library is unavailable or access is denied")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := []any{baseID}
	for _, principal := range principals {
		args = append(args, principal)
	}
	var repositoryID, defaultRef string
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT r.id,r.default_branch FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.library_id=? AND r.enabled=1 AND (p.principal IN (`+placeholders+`) OR p.principal='*') LIMIT 1`), args...).Scan(&repositoryID, &defaultRef)
	if err != nil {
		return RepositoryMap{}, errors.New("library is unavailable or access is denied")
	}
	ref := strings.TrimSpace(requestedRef)
	if ref == "" {
		ref = version
	}
	if ref == "" {
		ref = defaultRef
	}
	var result RepositoryMap
	result.LibraryID, result.Ref = baseID, ref
	err = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT commit_id,summary_json FROM repository_maps WHERE repository_id=? AND ref_name=?`), repositoryID, ref).Scan(&result.CommitID, &result.SummaryJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryMap{}, errors.New("repository map is unavailable; reindex this ref")
	}
	return result, err
}

func (s *Service) FindSymbols(ctx context.Context, principals []string, libraryID, ref, query, kind string, limit int) ([]SymbolResult, error) {
	query, kind = strings.TrimSpace(query), strings.ToLower(strings.TrimSpace(kind))
	if query == "" {
		return nil, errors.New("query is required")
	}
	if len(principals) == 0 {
		return []SymbolResult{}, nil
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := make([]any, 0, len(principals)+8)
	for _, principal := range principals {
		args = append(args, principal)
	}
	statement := `SELECT DISTINCT r.library_id,s.ref_name,s.commit_id,s.file_path,s.name,s.qualified_name,s.symbol_kind,s.language,s.signature,s.documentation,s.line_start,s.line_end
FROM code_symbols s JOIN repositories r ON r.id=s.repository_id JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.enabled=1 AND (p.principal IN (` + placeholders + `) OR p.principal='*')`
	if strings.TrimSpace(libraryID) != "" {
		baseID, version, ok := splitLibraryID(libraryID)
		if !ok {
			return nil, errors.New("libraryId must use /organization/project[/version]")
		}
		statement += ` AND r.library_id=?`
		args = append(args, baseID)
		if ref == "" {
			ref = version
		}
	}
	if ref != "" {
		statement += ` AND s.ref_name=?`
		args = append(args, ref)
	}
	if kind != "" {
		statement += ` AND s.symbol_kind=?`
		args = append(args, kind)
	}
	statement += ` AND (LOWER(s.name) LIKE LOWER(?) OR LOWER(s.qualified_name) LIKE LOWER(?) OR LOWER(s.signature) LIKE LOWER(?))
ORDER BY CASE WHEN LOWER(s.name)=LOWER(?) THEN 0 WHEN LOWER(s.qualified_name)=LOWER(?) THEN 1 ELSE 2 END,s.name,s.file_path LIMIT ?`
	like := "%" + query + "%"
	args = append(args, like, like, like, query, query, limit)
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolResult
	for rows.Next() {
		var item SymbolResult
		if err = rows.Scan(&item.LibraryID, &item.Ref, &item.CommitID, &item.FilePath, &item.Name, &item.QualifiedName, &item.Kind, &item.Language, &item.Signature, &item.Documentation, &item.LineStart, &item.LineEnd); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) SymbolContext(ctx context.Context, principals []string, libraryID, ref, symbol string) (SymbolResult, error) {
	results, err := s.FindSymbols(ctx, principals, libraryID, ref, symbol, "", 20)
	if err != nil {
		return SymbolResult{}, err
	}
	if len(results) == 0 {
		return SymbolResult{}, errors.New("symbol is unavailable or access is denied")
	}
	selected := results[0]
	for _, item := range results {
		if strings.EqualFold(item.QualifiedName, symbol) || strings.EqualFold(item.Name, symbol) {
			selected = item
			break
		}
	}
	var repositoryID string
	if err = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id FROM repositories WHERE library_id=?`), selected.LibraryID).Scan(&repositoryID); err != nil {
		return SymbolResult{}, errors.New("symbol is unavailable or access is denied")
	}
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT content FROM document_chunks WHERE repository_id=? AND ref_name=? AND file_path=? AND line_end>=? AND line_start<=? ORDER BY line_start LIMIT 5`), repositoryID, selected.Ref, selected.FilePath, selected.LineStart, selected.LineEnd)
	if err != nil {
		return SymbolResult{}, err
	}
	var contents []string
	for rows.Next() {
		var content string
		if rows.Scan(&content) == nil {
			contents = append(contents, content)
		}
	}
	rows.Close()
	selected.Content = strings.Join(contents, "\n\n")
	return selected, nil
}

func (s *Service) TraceDependencies(ctx context.Context, principals []string, libraryID, ref, symbol string, limit int) ([]DependencyResult, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, errors.New("symbol is required")
	}
	repositoryID, baseID, selectedRef, err := s.authorizedRepository(ctx, principals, libraryID, ref)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	like := "%" + symbol + "%"
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT commit_id,file_path,from_symbol,target,dependency_kind,line_number
FROM code_dependencies WHERE repository_id=? AND ref_name=? AND
(LOWER(from_symbol) LIKE LOWER(?) OR LOWER(target) LIKE LOWER(?))
ORDER BY CASE WHEN LOWER(from_symbol)=LOWER(?) OR LOWER(target)=LOWER(?) THEN 0 ELSE 1 END,file_path,line_number LIMIT ?`),
		repositoryID, selectedRef, like, like, symbol, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DependencyResult
	for rows.Next() {
		item := DependencyResult{LibraryID: baseID, Ref: selectedRef}
		if err = rows.Scan(&item.CommitID, &item.FilePath, &item.FromSymbol, &item.Target, &item.Kind, &item.LineNumber); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) CompareRefs(ctx context.Context, principals []string, libraryID, baseRef, headRef string) (RefComparison, error) {
	baseRef, headRef = strings.TrimSpace(baseRef), strings.TrimSpace(headRef)
	if baseRef == "" || headRef == "" || baseRef == headRef {
		return RefComparison{}, errors.New("distinct baseRef and headRef are required")
	}
	repositoryID, baseID, _, err := s.authorizedRepository(ctx, principals, libraryID, baseRef)
	if err != nil {
		return RefComparison{}, err
	}
	load := func(ref string) (map[string]SymbolResult, error) {
		rows, queryErr := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end
FROM code_symbols WHERE repository_id=? AND ref_name=? ORDER BY qualified_name,file_path,line_start`), repositoryID, ref)
		if queryErr != nil {
			return nil, queryErr
		}
		defer rows.Close()
		items := map[string]SymbolResult{}
		for rows.Next() {
			var item SymbolResult
			item.LibraryID, item.Ref = baseID, ref
			if queryErr = rows.Scan(&item.CommitID, &item.FilePath, &item.Name, &item.QualifiedName, &item.Kind, &item.Language, &item.Signature, &item.Documentation, &item.LineStart, &item.LineEnd); queryErr != nil {
				return nil, queryErr
			}
			items[item.QualifiedName+"\x00"+item.Kind+"\x00"+item.FilePath] = item
		}
		return items, rows.Err()
	}
	before, err := load(baseRef)
	if err != nil {
		return RefComparison{}, err
	}
	after, err := load(headRef)
	if err != nil {
		return RefComparison{}, err
	}
	if len(before) == 0 && len(after) == 0 {
		return RefComparison{}, errors.New("indexed refs are unavailable")
	}
	result := RefComparison{LibraryID: baseID, BaseRef: baseRef, HeadRef: headRef}
	for key, old := range before {
		current, exists := after[key]
		if !exists {
			result.Changes = append(result.Changes, RefChange{Type: "removed", Name: old.QualifiedName, Kind: old.Kind, FilePath: old.FilePath, BeforeSignature: old.Signature})
		} else if old.ContentHashKey() != current.ContentHashKey() {
			result.Changes = append(result.Changes, RefChange{Type: "modified", Name: current.QualifiedName, Kind: current.Kind, FilePath: current.FilePath, BeforeSignature: old.Signature, AfterSignature: current.Signature})
		}
	}
	for key, current := range after {
		if _, exists := before[key]; !exists {
			result.Changes = append(result.Changes, RefChange{Type: "added", Name: current.QualifiedName, Kind: current.Kind, FilePath: current.FilePath, AfterSignature: current.Signature})
		}
	}
	sort.Slice(result.Changes, func(i, j int) bool {
		if result.Changes[i].FilePath == result.Changes[j].FilePath {
			return result.Changes[i].Name < result.Changes[j].Name
		}
		return result.Changes[i].FilePath < result.Changes[j].FilePath
	})
	return result, nil
}

func (s SymbolResult) ContentHashKey() string {
	return s.Signature + "\x00" + s.Documentation + "\x00" + fmt.Sprint(s.LineStart) + "\x00" + fmt.Sprint(s.LineEnd)
}

func (s *Service) ChangeImpact(ctx context.Context, principals []string, libraryID, baseRef, headRef string, limit int) (ChangeImpact, error) {
	comparison, err := s.CompareRefs(ctx, principals, libraryID, baseRef, headRef)
	if err != nil {
		return ChangeImpact{}, err
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	seen := map[string]bool{}
	var dependents []DependencyResult
	for _, change := range comparison.Changes {
		if len(dependents) >= limit {
			break
		}
		results, traceErr := s.TraceDependencies(ctx, principals, libraryID, headRef, change.Name, limit-len(dependents))
		if traceErr != nil {
			return ChangeImpact{}, traceErr
		}
		for _, item := range results {
			key := item.FilePath + "\x00" + item.FromSymbol + "\x00" + item.Target + "\x00" + fmt.Sprint(item.LineNumber)
			if !seen[key] {
				seen[key] = true
				dependents = append(dependents, item)
			}
		}
	}
	return ChangeImpact{Comparison: comparison, Dependents: dependents}, nil
}

func (s *Service) authorizedRepository(ctx context.Context, principals []string, libraryID, requestedRef string) (repositoryID, baseID, ref string, err error) {
	baseID, version, ok := splitLibraryID(libraryID)
	if !ok || len(principals) == 0 {
		return "", "", "", errors.New("library is unavailable or access is denied")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := []any{baseID}
	for _, principal := range principals {
		args = append(args, principal)
	}
	var defaultRef string
	err = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT r.id,r.default_branch FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.library_id=? AND r.enabled=1 AND (p.principal IN (`+placeholders+`) OR p.principal='*') LIMIT 1`), args...).Scan(&repositoryID, &defaultRef)
	if err != nil {
		return "", "", "", errors.New("library is unavailable or access is denied")
	}
	ref = strings.TrimSpace(requestedRef)
	if ref == "" {
		ref = version
	}
	if ref == "" {
		ref = defaultRef
	}
	return repositoryID, baseID, ref, nil
}

func splitLibraryID(libraryID string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(libraryID), "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	version := ""
	if len(parts) == 3 {
		version = parts[2]
	}
	return "/" + parts[0] + "/" + parts[1], version, true
}

func (s *Service) SearchRepositories(ctx context.Context, principals []string, query, sourceType string, limit int) ([]RepositoryResult, error) {
	query, sourceType = strings.TrimSpace(query), strings.ToLower(strings.TrimSpace(sourceType))
	if query == "" {
		return nil, errors.New("query is required")
	}
	if sourceType != "" && sourceType != "bitbucket" && sourceType != "gitlab" {
		return nil, errors.New("sourceType must be bitbucket, gitlab, or empty")
	}
	if len(principals) == 0 {
		return []RepositoryResult{}, nil
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := make([]any, 0, len(principals)+1)
	for _, principal := range principals {
		args = append(args, principal)
	}
	statement := `SELECT DISTINCT r.id,r.project_key,r.slug,r.name,r.description,r.library_id,r.default_branch,r.source_type,r.indexed_at
FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.enabled=1 AND (p.principal IN (` + placeholders + `) OR p.principal='*')`
	if sourceType != "" {
		statement += ` AND r.source_type=?`
		args = append(args, sourceType)
	}
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		RepositoryResult
		score int
	}
	terms := strings.Fields(strings.ToLower(query))
	var matches []scored
	for rows.Next() {
		var item scored
		if err = rows.Scan(&item.ID, &item.ProjectKey, &item.Slug, &item.Name, &item.Description, &item.LibraryID, &item.DefaultBranch, &item.SourceType, &item.IndexedAt); err != nil {
			return nil, err
		}
		haystack := strings.ToLower(item.ProjectKey + " " + item.Slug + " " + item.Name + " " + item.Description + " " + item.LibraryID)
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				item.score++
			}
		}
		if strings.EqualFold(query, item.Name) || strings.EqualFold(query, item.Slug) {
			item.score += 10
		}
		if item.score > 0 {
			matches = append(matches, item)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].LibraryID < matches[j].LibraryID
		}
		return matches[i].score > matches[j].score
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]RepositoryResult, len(matches))
	for i := range matches {
		out[i] = matches[i].RepositoryResult
	}
	return out, nil
}

func (s *Service) SearchSource(ctx context.Context, principals []string, query, sourceType, project, repository, ref string, limit int) ([]SourceResult, error) {
	query, sourceType = strings.TrimSpace(query), strings.ToLower(strings.TrimSpace(sourceType))
	project, repository, ref = strings.TrimSpace(project), strings.TrimSpace(repository), strings.TrimSpace(ref)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if sourceType != "" && sourceType != "bitbucket" && sourceType != "gitlab" {
		return nil, errors.New("sourceType must be bitbucket, gitlab, or empty")
	}
	if len(principals) == 0 {
		return []SourceResult{}, nil
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := make([]any, 0, len(principals)+3)
	for _, principal := range principals {
		args = append(args, principal)
	}
	statement := `SELECT DISTINCT r.id,r.project_key,r.slug,r.library_id,r.default_branch,r.source_type
FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.enabled=1 AND (p.principal IN (` + placeholders + `) OR p.principal='*')`
	if sourceType != "" {
		statement += ` AND r.source_type=?`
		args = append(args, sourceType)
	}
	if project != "" {
		statement += ` AND LOWER(r.project_key)=LOWER(?)`
		args = append(args, project)
	}
	if repository != "" {
		statement += ` AND (LOWER(r.slug)=LOWER(?) OR LOWER(r.library_id)=LOWER(?))`
		args = append(args, repository, repository)
	}
	statement += ` ORDER BY r.library_id LIMIT 25`
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, project, slug, libraryID, defaultRef, sourceType string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.id, &item.project, &item.slug, &item.libraryID, &item.defaultRef, &item.sourceType); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	var out []SourceResult
	var lastErr error
	for _, item := range candidates {
		selectedRef := ref
		if selectedRef == "" {
			selectedRef = item.defaultRef
		}
		if s.sources == nil {
			continue
		}
		adapter, loadErr := s.sources(ctx, item.sourceType)
		if loadErr != nil {
			lastErr = loadErr
			continue
		}
		searcher, ok := adapter.(source.QuerySearcher)
		if !ok {
			continue
		}
		hits, searchErr := searcher.SearchQuery(ctx, source.RepositoryRef{ProjectKey: item.project, Slug: item.slug}, selectedRef, query, limit-len(out))
		if searchErr != nil {
			lastErr = searchErr
			continue
		}
		hits = s.indexedSourceHits(ctx, item.id, selectedRef, hits, limit-len(out))
		for _, hit := range hits {
			out = append(out, SourceResult{LibraryID: item.libraryID, SourceType: item.sourceType, ProjectKey: item.project, RepositorySlug: item.slug, Ref: selectedRef, QueryResult: hit})
		}
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func (s *Service) Resolve(ctx context.Context, principals []string, name, query string) (libraries []Library, err error) {
	ctx, span := otel.Tracer("git-ctx/search").Start(ctx, "search.resolve-library",
		oteltrace.WithAttributes(attribute.Int("git_ctx.acl.principal_count", len(principals))))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "library resolution failed")
		}
		span.End()
	}()
	if strings.TrimSpace(name) == "" || strings.TrimSpace(query) == "" {
		return nil, errors.New("libraryName and query are required")
	}
	if len(principals) == 0 {
		return []Library{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := make([]any, len(principals))
	for i := range principals {
		args[i] = principals[i]
	}
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`
SELECT r.id,r.name,r.library_id,r.description,r.reputation
FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.enabled=1 AND (p.principal IN (`+placeholders+`) OR p.principal='*')`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		Library
		score  int
		repoID string
	}
	var found []scored
	terms := strings.Fields(strings.ToLower(name + " " + query))
	for rows.Next() {
		var x scored
		if err := rows.Scan(&x.repoID, &x.Name, &x.ID, &x.Description, &x.Reputation); err != nil {
			return nil, err
		}
		hay := strings.ToLower(x.Name + " " + x.ID + " " + x.Description)
		for _, term := range terms {
			if strings.Contains(hay, term) {
				x.score++
			}
		}
		if strings.EqualFold(x.Name, name) || strings.HasSuffix(x.ID, "/"+strings.ToLower(name)) {
			x.score += 10
		}
		found = append(found, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// SQLite is intentionally configured with a single connection so settings
	// writes cannot race. Finish the repository cursor before issuing per-repo
	// aggregate queries, otherwise database/sql waits for the connection held by
	// that same cursor.
	selected := found[:0]
	for _, x := range found {
		cr, _ := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT DISTINCT ref_name FROM document_chunks WHERE repository_id=?`), x.repoID)
		for cr != nil && cr.Next() {
			var ref string
			_ = cr.Scan(&ref)
			x.Versions = append(x.Versions, ref)
		}
		if cr != nil {
			cr.Close()
		}
		_ = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=?`), x.repoID).Scan(&x.Snippets)
		if x.score > 0 {
			selected = append(selected, x)
		}
	}
	found = selected
	sort.Slice(found, func(i, j int) bool { return found[i].score > found[j].score })
	if len(found) > 10 {
		found = found[:10]
	}
	out := make([]Library, len(found))
	for i := range found {
		out[i] = found[i].Library
	}
	span.SetAttributes(attribute.Int("git_ctx.search.result_count", len(out)))
	return out, nil
}

func (s *Service) Query(ctx context.Context, principals []string, libraryID, query string) (result string, err error) {
	ctx, span := otel.Tracer("git-ctx/search").Start(ctx, "search.query-docs",
		oteltrace.WithAttributes(attribute.String("git_ctx.library_id", libraryID), attribute.Int("git_ctx.acl.principal_count", len(principals))))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "documentation query failed")
		}
		span.End()
	}()
	parts := strings.Split(strings.TrimPrefix(libraryID, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || strings.TrimSpace(query) == "" {
		return "", errors.New("libraryId must use /organization/project[/version] and query is required")
	}
	baseID := "/" + parts[0] + "/" + parts[1]
	if len(principals) == 0 {
		return "", errors.New("library is unavailable or access is denied")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := []any{baseID}
	for _, principal := range principals {
		args = append(args, principal)
	}
	var repoID, name, defaultRef, sourceType, projectKey, repositorySlug string
	err = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`
SELECT r.id,r.name,r.default_branch,r.source_type,r.project_key,r.slug FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.library_id=? AND r.enabled=1 AND (p.principal IN (`+placeholders+`) OR p.principal='*') LIMIT 1`), args...).Scan(&repoID, &name, &defaultRef, &sourceType, &projectKey, &repositorySlug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("library is unavailable or access is denied")
	}
	if err != nil {
		return "", err
	}
	ref := defaultRef
	if len(parts) == 3 {
		ref = parts[2]
	}
	terms := unique(embedding.Tokens(query))
	cfg := s.load(ctx)
	if cfg.KeywordWeight < 0 {
		cfg.KeywordWeight = 1
	}
	if cfg.VectorWeight < 0 {
		cfg.VectorWeight = .35
	}
	if cfg.FinalK < 1 || cfg.FinalK > 50 {
		cfg.FinalK = 8
	}
	if cfg.CandidateLimit < 10 || cfg.CandidateLimit > 20000 {
		cfg.CandidateLimit = 5000
	}
	if cfg.RerankLimit < 1 || cfg.RerankLimit > 100 {
		cfg.RerankLimit = 30
	}
	if cfg.SourceQuerySearch && s.sources != nil && (sourceType != "bitbucket" || ref == defaultRef) {
		adapter, sourceErr := s.sources(ctx, sourceType)
		if sourceErr == nil {
			if querySearcher, ok := adapter.(source.QuerySearcher); ok {
				remoteHits, queryErr := querySearcher.SearchQuery(ctx, source.RepositoryRef{ProjectKey: projectKey, Slug: repositorySlug}, ref, query, cfg.FinalK)
				if queryErr == nil && len(remoteHits) > 0 {
					safeHits := s.indexedSourceHits(ctx, repoID, ref, remoteHits, cfg.FinalK)
					if len(safeHits) > 0 {
						span.SetAttributes(attribute.Int("git_ctx.search.result_count", len(safeHits)), attribute.String("git_ctx.search.mode", "source-query-api"))
						return assembleSourceQueryResults(name, sourceType, baseID, ref, safeHits), nil
					}
				}
			}
		}
	}
	keywordScores := map[string]float64{}
	var keywordIDs []string
	if s.keyword != nil {
		if candidates, keywordErr := s.keyword(ctx, repoID, ref, principals, query, cfg.CandidateLimit); keywordErr == nil {
			for _, candidate := range candidates {
				if candidate.ID != "" {
					keywordIDs = append(keywordIDs, candidate.ID)
					keywordScores[candidate.ID] = candidate.Score
				}
			}
		}
	}
	candidateSQL := `SELECT id,content,file_path,line_start,line_end,commit_id,heading,embedding FROM document_chunks WHERE repository_id=? AND ref_name=?`
	args = []any{repoID, ref}
	if len(keywordIDs) > 0 {
		candidateSQL += " AND id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(keywordIDs)), ",") + ")"
		for _, id := range keywordIDs {
			args = append(args, id)
		}
	} else if len(terms) > 0 {
		candidateSQL += " AND ("
		limit := min(len(terms), 5)
		for n, term := range terms[:limit] {
			if n > 0 {
				candidateSQL += " OR "
			}
			candidateSQL += "LOWER(heading || ' ' || content) LIKE ?"
			args = append(args, "%"+term+"%")
		}
		candidateSQL += ")"
	}
	candidateSQL += fmt.Sprintf(" LIMIT %d", cfg.CandidateLimit)
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(candidateSQL), args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type hit struct {
		id, content, path, commit, heading string
		start, end                         int
		score                              float64
		tokens                             []string
		vector                             []byte
	}
	var hits []hit
	df := map[string]int{}
	totalLength := 0
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.content, &h.path, &h.start, &h.end, &h.commit, &h.heading, &h.vector); err != nil {
			return "", err
		}
		h.tokens = embedding.Tokens(h.heading + " " + h.content)
		totalLength += len(h.tokens)
		present := map[string]bool{}
		for _, token := range h.tokens {
			present[token] = true
		}
		for _, term := range terms {
			if present[term] {
				df[term]++
			}
		}
		hits = append(hits, h)
	}
	avgLength := float64(totalLength) / math.Max(1, float64(len(hits)))
	var queryVector []float32
	if cfg.VectorWeight > 0 {
		var embedErr error
		queryVector, embedErr = s.embedder(ctx).Embed(ctx, query)
		if embedErr != nil {
			queryVector = embedding.Embed(query)
		}
	}
	filtered := hits[:0]
	for n := range hits {
		tf := map[string]int{}
		for _, token := range hits[n].tokens {
			tf[token]++
		}
		var bm25 float64
		for _, term := range terms {
			frequency := float64(tf[term])
			if frequency == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(hits)-df[term])+0.5)/(float64(df[term])+0.5))
			lengthNorm := 1 - 0.75 + 0.75*float64(len(hits[n].tokens))/avgLength
			bm25 += idf * (frequency * 2.2) / (frequency + 1.2*lengthNorm)
		}
		vectorScore := 0.0
		if cfg.VectorWeight > 0 {
			vectorScore = embedding.Cosine(queryVector, embedding.Decode(hits[n].vector))
		}
		keywordScore := bm25
		if score, ok := keywordScores[hits[n].id]; ok {
			keywordScore = score
		}
		hits[n].score = cfg.KeywordWeight*keywordScore + cfg.VectorWeight*math.Max(0, vectorScore)
		if keywordScore > 0 || vectorScore > 0.18 {
			filtered = append(filtered, hits[n])
		}
	}
	hits = filtered
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if s.reranker != nil && len(hits) > 0 {
		limit := min(len(hits), cfg.RerankLimit)
		documents := make([]string, limit)
		for i := 0; i < limit; i++ {
			documents[i] = hits[i].heading + "\n" + hits[i].content
		}
		if provider := s.reranker(ctx); provider != nil {
			if scores, rerankErr := provider.Rerank(ctx, query, documents); rerankErr == nil && len(scores) == limit {
				for i := 0; i < limit; i++ {
					hits[i].score = scores[i]
				}
				sort.SliceStable(hits[:limit], func(i, j int) bool { return hits[i].score > hits[j].score })
			}
		}
	}
	if len(hits) > cfg.FinalK {
		hits = hits[:cfg.FinalK]
	}
	if len(hits) == 0 {
		span.SetAttributes(attribute.Int("git_ctx.search.result_count", 0))
		return fmt.Sprintf("No indexed documentation matched the query in %s at %s. Try another term or version.", name, ref), nil
	}
	span.SetAttributes(attribute.Int("git_ctx.search.result_count", len(hits)))
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	for _, h := range hits {
		fmt.Fprintf(&b, "## %s\n\n%s\n\nSource: `%s://%s@%s/%s#L%d-L%d`\n\n", h.heading, h.content, sourceType, strings.TrimPrefix(baseID, "/"), h.commit, h.path, h.start, h.end)
	}
	return b.String(), nil
}
func (s *Service) indexedSourceHits(ctx context.Context, repoID, ref string, remote []source.QueryResult, limit int) []source.QueryResult {
	out := make([]source.QueryResult, 0, min(limit, len(remote)))
	seen := map[string]bool{}
	for _, hit := range remote {
		if hit.Path == "" || seen[hit.Path] {
			continue
		}
		var safe source.QueryResult
		err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT file_path,content,commit_id,line_start,line_end FROM document_chunks WHERE repository_id=? AND ref_name=? AND file_path=? ORDER BY line_start LIMIT 1`), repoID, ref, hit.Path).Scan(&safe.Path, &safe.Snippet, &safe.CommitID, &safe.LineStart, &safe.LineEnd)
		if err != nil {
			continue
		}
		seen[safe.Path] = true
		out = append(out, safe)
		if len(out) == limit {
			break
		}
	}
	return out
}
func assembleSourceQueryResults(name, sourceType, baseID, ref string, hits []source.QueryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	for _, hit := range hits {
		commit := hit.CommitID
		if commit == "" {
			commit = ref
		}
		snippet := strings.TrimSpace(hit.Snippet)
		if len(snippet) > 16000 {
			snippet = snippet[:16000]
		}
		if snippet == "" {
			snippet = "Matched by the repository source query API."
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\nSource: `%s://%s@%s/%s#L%d-L%d`\n\n", hit.Path, snippet, sourceType, strings.TrimPrefix(baseID, "/"), commit, hit.Path, max(1, hit.LineStart), max(max(1, hit.LineStart), hit.LineEnd))
	}
	return b.String()
}
func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

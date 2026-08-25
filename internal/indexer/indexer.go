package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"git-ctx/internal/codeintel"
	"git-ctx/internal/contentsecurity"
	"git-ctx/internal/embedding"
	"git-ctx/internal/manifest"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

type Policy struct {
	IncludeExtensions []string `json:"includeExtensions"`
	ExcludePrefixes   []string `json:"excludePrefixes"`
	MaxFileBytes      int64    `json:"maxFileBytes"`
}

// embeddingBatchSize is how many chunks are vectorized per request, and
// indexProgressInterval is how often a running job publishes its file counter so
// operators can see progress instead of a job that looks stuck.
const (
	embeddingBatchSize    = 32
	indexProgressInterval = 25
)

// PolicyChangedRevision marks a ref whose policy was edited since it was
// indexed. It can never equal a real fingerprint, so the next run re-reads the
// ref even though its commit has not moved.
const PolicyChangedRevision = "policy-changed"

// Revision fingerprints the policy the content was built with. A ref whose
// commit has not moved is normally left alone, which is right until an operator
// changes what may be indexed: the stored chunks then reflect a policy nobody
// is using any more, and a manual reindex reports "0 files" because the commit
// is unchanged. Comparing this value makes a policy change a reason to re-read,
// exactly as an embedding model change already is.
func (p Policy) Revision() string {
	extensions := append([]string(nil), p.IncludeExtensions...)
	prefixes := append([]string(nil), p.ExcludePrefixes...)
	sort.Strings(extensions)
	sort.Strings(prefixes)
	sum := sha256.Sum256([]byte(strings.Join(extensions, ",") + "\x00" + strings.Join(prefixes, ",") + "\x00" + strconv.FormatInt(p.MaxFileBytes, 10)))
	return hex.EncodeToString(sum[:8])
}

// DefaultPolicy covers the languages and configuration formats found in a normal
// enterprise repository. The list used to stop at a handful of extensions, so a
// JavaScript, Kotlin, C# or Ruby repository indexed zero files and looked broken
// even though the job completed.
func DefaultPolicy() Policy {
	return Policy{
		IncludeExtensions: []string{
			".md", ".mdx", ".rst", ".txt", ".adoc", ".asciidoc",
			".yaml", ".yml", ".json", ".jsonc", ".xml", ".toml", ".ini", ".conf", ".cfg", ".properties",
			".mod", ".gradle", ".sbt", ".bzl", ".cmake", ".tf", ".tfvars", ".proto", ".graphql", ".gql",
			".go", ".java", ".kt", ".kts", ".scala", ".groovy",
			".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte",
			".py", ".rb", ".php", ".cs", ".fs", ".rs", ".swift", ".dart", ".lua", ".pl", ".r",
			".c", ".h", ".cc", ".cpp", ".hpp", ".hh", ".m", ".mm",
			".sh", ".bash", ".zsh", ".ps1", ".bat",
			".sql", ".ddl", ".pks", ".pkb",
		},
		ExcludePrefixes: []string{"node_modules/", "vendor/", "dist/", "build/", "target/", ".git/", ".idea/", "secrets/"},
		MaxFileBytes:    1 << 20,
	}
}

// indexableByName lists build and operations files that carry no extension but
// describe how a service is built and run.
var indexableByName = map[string]bool{
	"dockerfile": true, "containerfile": true, "makefile": true, "jenkinsfile": true,
	"readme": true, "changelog": true, "codeowners": true, "procfile": true,
}

type Indexer struct {
	store             *store.Store
	policy            Policy
	embedder          embedding.Provider
	embeddingRequired bool
}

func New(s *store.Store, p Policy) *Indexer {
	if len(p.IncludeExtensions) == 0 {
		p = DefaultPolicy()
	}
	return &Indexer{store: s, policy: p, embedder: embedding.Local{}}
}
func NewWithEmbedder(s *store.Store, p Policy, provider embedding.Provider) *Indexer {
	index := New(s, p)
	if provider != nil {
		index.embedder = provider
	}
	index.embeddingRequired = true
	return index
}

// NewWithoutEmbeddings builds the lexical-only indexing path. Chunks, symbols,
// dependencies and source metadata are still indexed; only the vector column is
// omitted.
func NewWithoutEmbeddings(s *store.Store, p Policy) *Indexer {
	index := New(s, p)
	index.embedder = nil
	return index
}

// NewWithOptionalEmbedder keeps indexing usable when an embedding service
// becomes unavailable mid-batch. The completed generation contains lexical
// chunks with NULL vectors and the job records a warning for operators.
func NewWithOptionalEmbedder(s *store.Store, p Policy, provider embedding.Provider) *Indexer {
	index := NewWithoutEmbeddings(s, p)
	index.embedder = provider
	return index
}
func LibraryID(projectKey, slug string) string {
	return "/" + normalize(projectKey) + "/" + normalize(slug)
}

func LibraryIDForSource(sourceType, projectKey, slug string) string {
	return source.LibraryID(sourceType, projectKey, slug)
}

func (i *Indexer) SyncRepository(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference) error {
	return i.syncRepository(ctx, adapter, sourceType, repo, refs, true, nil)
}

// JobLease identifies the exact worker claim allowed to publish an index
// generation. A stale worker may finish remote work after another replica has
// reclaimed the row; the swap transaction must reject that generation.
type JobLease struct {
	ID        string
	StartedAt time.Time
}

func (i *Indexer) lockJobLease(ctx context.Context, tx *sql.Tx, lease *JobLease) error {
	if lease == nil {
		return nil
	}
	// A conditional no-op update both validates ownership and locks the job row
	// until this transaction commits.
	result, err := tx.ExecContext(ctx, i.store.Rebind(`UPDATE index_jobs SET started_at=started_at WHERE id=? AND status='running' AND started_at=?`), lease.ID, lease.StartedAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("index job lease changed before generation publish")
	}
	return nil
}

// ApplyPendingJob indexes content for a job the worker already owns.
func (i *Indexer) ApplyPendingJob(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference, lease JobLease) error {
	return i.syncRepository(ctx, adapter, sourceType, repo, refs, false, &lease)
}

func (i *Indexer) syncRepository(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference, trackJobs bool, workerLease *JobLease) error {
	if sourceType != "bitbucket" && sourceType != "gitlab" && sourceType != "confluence" && sourceType != "jira" {
		return errors.New("unsupported source type")
	}
	repoID := sourceType + ":" + fmt.Sprint(repo.ID)
	libraryID := LibraryIDForSource(sourceType, repo.ProjectKey, repo.Slug)
	_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES(?,?,?,?,?,?,?,?,?,1) ON CONFLICT(id) DO UPDATE SET project_key=excluded.project_key,slug=excluded.slug,name=excluded.name,description=excluded.description,default_branch=excluded.default_branch,enabled=1`), repoID, repo.ProjectKey, repo.Slug, repo.Name, repo.Description, sourceType, fmt.Sprint(repo.ID), libraryID, repo.DefaultBranch)
	if err != nil {
		// The path is unique, so a project that took over a path another project
		// used to hold — a rename or a transfer, both routine on GitLab and
		// Bitbucket — fails here with a bare constraint string that names a
		// database index and no repository. The conflict is looked up and
		// reported in terms an operator can act on.
		var holder, holderExternal string
		if lookup := i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT id,source_external_id FROM repositories WHERE source_type=? AND project_key=? AND slug=? AND id<>?`),
			sourceType, repo.ProjectKey, repo.Slug, repoID).Scan(&holder, &holderExternal); lookup == nil {
			return fmt.Errorf("%s/%s is already registered as %s (source id %s), so %s cannot take that path. The project was renamed, transferred or replaced at the source: disable or remove the stale repository, then index again",
				repo.ProjectKey, repo.Slug, holder, holderExternal, repoID)
		}
		return err
	}
	ref := source.RepositoryRef{ProjectKey: repo.ProjectKey, Slug: repo.Slug}
	perms, err := adapter.GetPermissions(ctx, ref)
	if err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	tx, err := i.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_permissions WHERE repository_id=?`), repoID); err != nil {
		return err
	}
	for _, p := range perms {
		if !readable(p.Permission) {
			continue
		}
		principal := p.Principal
		if p.Kind == "group" {
			principal = "group:" + strings.TrimPrefix(strings.TrimPrefix(principal, "group:"), "/")
		}
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,?,?) ON CONFLICT(repository_id,principal) DO UPDATE SET permission=excluded.permission`), repoID, principal, p.Permission); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, r := range refs {
		if err := i.syncRef(ctx, adapter, repoID, ref, r, trackJobs, workerLease); err != nil {
			return err
		}
	}
	_, _ = i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE repositories SET indexed_at=? WHERE id=?`), time.Now().UTC(), repoID)
	return nil
}
func (i *Indexer) syncRef(ctx context.Context, adapter source.RepositorySource, repoID string, repo source.RepositoryRef, ref source.Reference, trackJob bool, workerLease *JobLease) error {
	jobID := fmt.Sprintf("%d", time.Now().UnixNano())
	generationID := repoID + ":" + ref.Name + ":" + jobID
	// reportJobID is the row that receives progress and result details. The
	// indexer creates it for direct syncs and reuses the worker row otherwise.
	reportJobID := ""
	if workerLease != nil {
		reportJobID = workerLease.ID
	}
	if trackJob {
		reportJobID = jobID
	}
	if trackJob {
		_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,started_at,attempts) VALUES(?,?,?,'sync','running',?,1)`), jobID, repoID, ref.Name, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	fail := func(e error) error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`DELETE FROM document_chunks_staging WHERE generation_id=?`), generationID)
		_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`DELETE FROM code_symbols_staging WHERE generation_id=?`), generationID)
		_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`DELETE FROM code_dependencies_staging WHERE generation_id=?`), generationID)
		_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`DELETE FROM repository_packages_staging WHERE generation_id=?`), generationID)
		if trackJob {
			_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`UPDATE index_jobs SET status='failed',error_message=?,completed_at=? WHERE id=?`), truncate(e.Error(), 1000), time.Now().UTC(), jobID)
		}
		return e
	}
	// complete records the finished job. A non-empty warning exposes degraded
	// embedding or policy outcomes instead of leaving a silent gap.
	complete := func(processed int, warning string) error {
		if reportJobID == "" {
			return nil
		}
		if !trackJob {
			// The worker owns the status transition; only the details are ours.
			result, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE index_jobs SET files_processed=?,error_message=? WHERE id=? AND status='running' AND started_at=?`), processed, truncate(warning, 1000), reportJobID, workerLease.StartedAt)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				return errors.New("index job lease changed before progress update")
			}
			return nil
		}
		_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE index_jobs SET status='completed',files_processed=?,error_message=?,completed_at=? WHERE id=?`), processed, truncate(warning, 1000), time.Now().UTC(), reportJobID)
		return err
	}
	updateProgress := func(processed int) error {
		if reportJobID == "" {
			return nil
		}
		query := `UPDATE index_jobs SET files_processed=? WHERE id=? AND status='running'`
		args := []any{processed, reportJobID}
		if !trackJob {
			query += ` AND started_at=?`
			args = append(args, workerLease.StartedAt)
		}
		result, err := i.store.DB.ExecContext(ctx, i.store.Rebind(query), args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("index job lease changed before progress update")
		}
		return nil
	}
	embeddingMetadata := embedding.Metadata{}
	metadataKnown := false
	activeEmbedder := i.embedder
	if provider, ok := activeEmbedder.(embedding.MetadataProvider); ok && activeEmbedder != nil {
		embeddingMetadata = provider.EmbeddingMetadata()
		metadataKnown = embeddingMetadata.Provider != "" && embeddingMetadata.Model != ""
	}
	embeddingRevision := embeddingMetadata.Identity()
	if activeEmbedder == nil {
		embeddingRevision = "keyword-only"
	}
	previousCommit := ""
	previousEmbeddingRevision := ""
	previousPolicyRevision := ""
	policyRevision := i.policy.Revision()
	err := i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT commit_id,embedding_revision,policy_revision FROM repository_ref_states WHERE repository_id=? AND ref_name=?`), repoID, ref.Name).Scan(&previousCommit, &previousEmbeddingRevision, &previousPolicyRevision)
	if errors.Is(err, sql.ErrNoRows) {
		err = i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT commit_id FROM document_chunks WHERE repository_id=? AND ref_name=? ORDER BY indexed_at DESC LIMIT 1`), repoID, ref.Name).Scan(&previousCommit)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	// An empty stored policy revision is a ref indexed before this was recorded.
	// Treating that as a mismatch would re-read every repository on upgrade, so
	// it is accepted as "unknown but current" until the policy next changes.
	policyChanged := previousPolicyRevision != "" && previousPolicyRevision != policyRevision
	if previousCommit != "" && previousCommit == ref.LatestCommit && previousEmbeddingRevision == embeddingRevision && !policyChanged {
		var totalChunks, embeddedChunks int
		tx, txErr := i.store.DB.BeginTx(ctx, nil)
		if txErr != nil {
			return fail(txErr)
		}
		defer tx.Rollback()
		if txErr = i.lockJobLease(ctx, tx, workerLease); txErr != nil {
			_ = tx.Rollback()
			return fail(txErr)
		}
		if err = tx.QueryRowContext(ctx, i.store.Rebind(`SELECT COUNT(*),COUNT(embedding) FROM document_chunks WHERE repository_id=? AND ref_name=?`), repoID, ref.Name).Scan(&totalChunks, &embeddedChunks); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		embeddingStatus, embeddingError := describeEmbeddingState(embeddingRevision, totalChunks, embeddedChunks, nil)
		_, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at,embedding_revision,policy_revision,total_chunks,embedded_chunks,embedding_status,embedding_error)
VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repository_id,ref_name) DO UPDATE SET commit_id=excluded.commit_id,indexed_at=excluded.indexed_at,embedding_revision=excluded.embedding_revision,policy_revision=excluded.policy_revision,total_chunks=excluded.total_chunks,embedded_chunks=excluded.embedded_chunks,embedding_status=excluded.embedding_status,embedding_error=excluded.embedding_error`),
			repoID, ref.Name, ref.LatestCommit, time.Now().UTC(), embeddingRevision, policyRevision, totalChunks, embeddedChunks, embeddingStatus, embeddingError)
		if err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if err = i.refreshRepositoryMap(ctx, tx, repoID, ref.Name, ref.LatestCommit); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if err = tx.Commit(); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		return complete(0, "")
	}

	incremental := false
	var files []source.File
	removedPaths := map[string]bool{}
	upsertPaths := map[string]bool{}
	// A changed policy invalidates the whole ref, not the files a diff names:
	// the paths that were excluded before are exactly the ones a diff will not
	// mention.
	forceFull := previousCommit == ref.LatestCommit && (previousEmbeddingRevision != embeddingRevision || policyChanged)
	snapshotRef := ref.LatestCommit
	if snapshotRef == "" {
		snapshotRef = ref.Name
	}
	if previousCommit != "" && !forceFull {
		if feed, ok := adapter.(source.ChangeFeed); ok {
			changes, changeErr := feed.Changes(ctx, repo, previousCommit, ref.LatestCommit)
			if changeErr == nil {
				incremental = true
				seen := map[string]bool{}
				for _, change := range changes {
					changeType := strings.ToLower(change.Type)
					if change.OldPath != "" {
						removedPaths[filepath.ToSlash(change.OldPath)] = true
					}
					if change.Path != "" {
						removedPaths[filepath.ToSlash(change.Path)] = true
					}
					if strings.Contains(changeType, "delete") || change.Path == "" {
						continue
					}
					path := filepath.ToSlash(change.Path)
					if !seen[path] {
						seen[path] = true
						files = append(files, source.File{Path: path})
					}
				}
			}
		}
	}
	if !incremental {
		files, err = adapter.ListFiles(ctx, repo, snapshotRef)
		if err != nil {
			return fail(err)
		}
	}
	type securityEvent struct {
		id, path, finding, action string
	}
	var securityEvents []securityEvent
	processed := 0
	// Chunks are embedded in batches instead of one request per chunk, and each
	// staged row waits for its vector. pending holds the rows of the current
	// batch together with the text that still needs a vector.
	type pendingChunk struct {
		id, filePath, heading, contentType, content, contentHash string
		start, end                                               int
		vector                                                   []byte
		embedText                                                string
	}
	var pending []pendingChunk
	var embeddingWarnings []string
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		var texts []string
		var positions []int
		for index := range pending {
			if len(pending[index].vector) == 0 {
				texts = append(texts, pending[index].embedText)
				positions = append(positions, index)
			}
		}
		if len(texts) > 0 && activeEmbedder != nil {
			vectors, embedErr := embedding.EmbedAll(ctx, activeEmbedder, texts)
			if embedErr != nil {
				if i.embeddingRequired {
					return fmt.Errorf("embedding %d chunks near %s: %w", len(texts), pending[positions[0]].filePath, embedErr)
				}
				embeddingWarnings = append(embeddingWarnings,
					fmt.Sprintf("embedding disabled for %d chunk(s) near %s after model error: %s", len(texts), pending[positions[0]].filePath, truncate(embedErr.Error(), 160)))
				// Do not keep calling a broken endpoint for every remaining
				// batch in this generation.
				activeEmbedder = nil
				embeddingMetadata = embedding.Metadata{}
				embeddingRevision = "keyword-only"
				metadataKnown = false
				texts = nil
			}
			if embedErr == nil {
				for position, vector := range vectors {
					pending[positions[position]].vector = embedding.Encode(vector)
					embeddingMetadata.Dimensions = len(vector)
				}
			}
		}
		for _, chunk := range pending {
			if _, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO document_chunks_staging(generation_id,id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding,embedding_provider,embedding_model,embedding_dimensions,embedding_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
				generationID, chunk.id, repoID, ref.Name, ref.LatestCommit, chunk.filePath, chunk.start, chunk.end, chunk.heading, chunk.contentType, chunk.content, chunk.contentHash, chunk.vector, embeddingMetadata.Provider, embeddingMetadata.Model, embeddingMetadata.Dimensions, embeddingRevision); err != nil {
				return err
			}
		}
		pending = pending[:0]
		return nil
	}
	// Policy exclusions are intentional and happen before the remote read. Once a
	// file is accepted by policy, failing to read it makes this generation
	// incomplete, so it must never replace the active ref index.
	candidates := 0
	// parsedManifests records which manifest paths this run has already read, so
	// the second pass does not fetch a file the content pass already parsed.
	parsedManifests := map[string]bool{}
	var packages []inventoryPackage
	var manifestWarnings []string
	for _, file := range files {
		if !i.allowed(file) {
			continue
		}
		candidates++
		if reportJobID != "" && processed > 0 && processed%indexProgressInterval == 0 {
			if err = updateProgress(processed); err != nil {
				return fail(err)
			}
		}
		raw, e := adapter.GetFile(ctx, repo, snapshotRef, file.Path)
		if e != nil {
			return fail(fmt.Errorf("read %s at %s: %w", file.Path, snapshotRef, e))
		}
		if int64(len(raw)) > i.policy.MaxFileBytes {
			continue
		}
		safeContent, finding := sanitize(string(raw))
		if finding != "" {
			action := "redacted"
			if safeContent == "" {
				action = "blocked"
			}
			eventID := hash(repoID + "\x00" + ref.Name + "\x00" + file.Path + "\x00" + finding)
			securityEvents = append(securityEvents, securityEvent{id: eventID, path: file.Path, finding: finding, action: action})
			if safeContent == "" {
				continue
			}
		}
		// A manifest states the versions an import line never carries, so the
		// dependency inventory is built from the same read as the chunks.
		// Manifests are parsed as they are read and the text is dropped. Holding
		// every manifest and lock file of a ref in memory at once is a real cost:
		// a lock file is megabytes, and a monorepo has dozens.
		if path := filepath.ToSlash(file.Path); !parsedManifests[path] {
			if _, isManifest := manifest.Recognize(path); isManifest {
				parsedManifests[path] = true
				packages = appendPackages(packages, path, safeContent)
			} else if _, isLock := manifest.RecognizeLock(path); isLock {
				parsedManifests[path] = true
				packages = appendPackages(packages, path, safeContent)
			}
		}
		contentLines := strings.Split(strings.ReplaceAll(safeContent, "\r\n", "\n"), "\n")
		for _, symbol := range codeintel.Extract(file.Path, safeContent) {
			symbolID := hash(repoID + "\x00" + ref.Name + "\x00" + file.Path + "\x00" + symbol.QualifiedName + "\x00" + fmt.Sprint(symbol.LineStart))
			start := min(max(1, symbol.LineStart), len(contentLines))
			end := min(len(contentLines), max(start, symbol.LineEnd))
			symbolHash := hash(strings.TrimSpace(strings.Join(contentLines[start-1:end], "\n")))
			_, e = i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO code_symbols_staging(generation_id,id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
				generationID, symbolID, repoID, ref.Name, ref.LatestCommit, filepath.ToSlash(file.Path), symbol.Name, symbol.QualifiedName, symbol.Kind, symbol.Language, symbol.Signature, symbol.Documentation, symbol.LineStart, symbol.LineEnd, symbolHash)
			if e != nil {
				return fail(e)
			}
		}
		for _, dependency := range codeintel.ExtractDependencies(file.Path, safeContent) {
			dependencyID := hash(repoID + "\x00" + ref.Name + "\x00" + file.Path + "\x00" + dependency.FromSymbol + "\x00" + dependency.Target + "\x00" + dependency.Kind + "\x00" + fmt.Sprint(dependency.Line))
			_, e = i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO code_dependencies_staging(generation_id,id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES(?,?,?,?,?,?,?,?,?,?)`),
				generationID, dependencyID, repoID, ref.Name, ref.LatestCommit, filepath.ToSlash(file.Path), dependency.FromSymbol, dependency.Target, dependency.Kind, dependency.Line)
			if e != nil {
				return fail(e)
			}
		}
		for _, chunk := range parse(file.Path, safeContent) {
			id := hash(repoID + "\x00" + ref.Name + "\x00" + file.Path + "\x00" + fmt.Sprint(chunk.Start) + "\x00" + chunk.Content)
			contentHash := hash(chunk.Content)
			var vector []byte
			if metadataKnown {
				reuseErr := i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT embedding FROM document_chunks WHERE repository_id=? AND heading=? AND content_hash=? AND embedding_provider=? AND embedding_model=? AND embedding_revision=? AND embedding IS NOT NULL LIMIT 1`), repoID, chunk.Heading, contentHash, embeddingMetadata.Provider, embeddingMetadata.Model, embeddingRevision).Scan(&vector)
				if reuseErr != nil && !errors.Is(reuseErr, sql.ErrNoRows) {
					return fail(reuseErr)
				}
			}
			pending = append(pending, pendingChunk{
				id: id, filePath: file.Path, heading: chunk.Heading, contentType: contentType(file.Path),
				content: chunk.Content, contentHash: contentHash, start: chunk.Start, end: chunk.End,
				vector: vector, embedText: chunk.Heading + "\n" + chunk.Content,
			})
			if len(pending) >= embeddingBatchSize {
				if flushErr := flush(); flushErr != nil {
					return fail(flushErr)
				}
			}
		}
		processed++
		upsertPaths[filepath.ToSlash(file.Path)] = true
	}
	if flushErr := flush(); flushErr != nil {
		return fail(flushErr)
	}
	// Manifests the content policy excluded are still read: they are small, named
	// exactly, and an inventory that silently omits the repositories whose policy
	// happens to exclude .json or .xml would be worse than none. The count is
	// bounded so a monorepo of a thousand packages cannot turn one index run into
	// a thousand extra round trips.
	for _, file := range files {
		if len(parsedManifests) >= maxManifestsPerRef {
			break
		}
		path := filepath.ToSlash(file.Path)
		if parsedManifests[path] {
			continue
		}
		_, isManifest := manifest.Recognize(path)
		_, isLock := manifest.RecognizeLock(path)
		if !isManifest && !isLock {
			continue
		}
		limit := int64(manifest.MaxManifestBytes)
		if isLock {
			// A lock file is large by nature; it is read up to its own bound.
			limit = manifest.MaxLockBytes
		}
		if file.Size > limit {
			continue
		}
		raw, readErr := adapter.GetFile(ctx, repo, snapshotRef, file.Path)
		if readErr != nil {
			// A manifest that cannot be read leaves the inventory incomplete for
			// this repository, which is recorded as a warning rather than failing a
			// generation that is otherwise complete.
			manifestWarnings = append(manifestWarnings, fmt.Sprintf("manifest %s: %v", path, readErr))
			continue
		}
		safeManifest, _ := sanitize(string(raw))
		parsedManifests[path] = true
		packages = appendPackages(packages, path, safeManifest)
	}
	// A ref with an enormous number of declarations is bounded here rather than
	// at write time, so the inventory stays a catalogue instead of a copy of
	// every lock file in the repository.
	if len(packages) > maxPackagesPerRef {
		manifestWarnings = append(manifestWarnings,
			fmt.Sprintf("dependency inventory truncated at %d declarations", maxPackagesPerRef))
		packages = packages[:maxPackagesPerRef]
	}
	// One statement per package would be thousands of round trips for a single
	// lock file, so declarations are written in batches.
	for start := 0; start < len(packages); start += packageInsertBatch {
		end := min(start+packageInsertBatch, len(packages))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*10)
		for _, item := range packages[start:end] {
			values = append(values, "(?,?,?,?,?,?,?,?,?,?)")
			args = append(args, generationID, repoID, ref.Name, item.Ecosystem, item.Name,
				strings.ToLower(item.Name), item.Version, item.Scope, item.ManifestPath, ref.LatestCommit)
		}
		statement := `INSERT INTO repository_packages_staging(generation_id,repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES ` +
			strings.Join(values, ",") +
			` ON CONFLICT(generation_id,repository_id,ref_name,ecosystem,name,version,manifest_path) DO UPDATE SET scope=excluded.scope`
		if _, err = i.store.DB.ExecContext(ctx, i.store.Rebind(statement), args...); err != nil {
			return fail(err)
		}
	}
	tx, err := i.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if leaseErr := i.lockJobLease(ctx, tx, workerLease); leaseErr != nil {
		_ = tx.Rollback()
		return fail(leaseErr)
	}
	deletedChunkIDs := map[string][]string{}
	var activeCommit string
	stateErr := tx.QueryRowContext(ctx, i.store.Rebind(`SELECT commit_id FROM repository_ref_states WHERE repository_id=? AND ref_name=?`), repoID, ref.Name).Scan(&activeCommit)
	if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) {
		_ = tx.Rollback()
		return fail(stateErr)
	}
	if activeCommit != "" && activeCommit != previousCommit {
		_ = tx.Rollback()
		return fail(fmt.Errorf("index state changed concurrently from %s to %s", previousCommit, activeCommit))
	}
	if incremental {
		for path := range removedPaths {
			rows, queryErr := tx.QueryContext(ctx, i.store.Rebind(`SELECT id FROM document_chunks WHERE repository_id=? AND ref_name=? AND file_path=? ORDER BY id`), repoID, ref.Name, path)
			if queryErr != nil {
				_ = tx.Rollback()
				return fail(queryErr)
			}
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					deletedChunkIDs[path] = append(deletedChunkIDs[path], id)
				}
			}
			queryErr = rows.Err()
			rows.Close()
			if queryErr != nil {
				_ = tx.Rollback()
				return fail(queryErr)
			}
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM document_chunks WHERE repository_id=? AND ref_name=? AND file_path=?`), repoID, ref.Name, path); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM code_symbols WHERE repository_id=? AND ref_name=? AND file_path=?`), repoID, ref.Name, path); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM code_dependencies WHERE repository_id=? AND ref_name=? AND file_path=?`), repoID, ref.Name, path); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
	} else {
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM document_chunks WHERE repository_id=? AND ref_name=?`), repoID, ref.Name); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM code_symbols WHERE repository_id=? AND ref_name=?`), repoID, ref.Name); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM code_dependencies WHERE repository_id=? AND ref_name=?`), repoID, ref.Name); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
	}
	// The file listing is stored for every path in the ref, not only the ones the
	// index policy accepted, so filename search can answer for lockfiles, images
	// and excluded sources too.
	if err = i.recordFiles(ctx, tx, repoID, ref, files, incremental, removedPaths); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding,indexed_at,embedding_provider,embedding_model,embedding_dimensions,embedding_revision)
SELECT id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding,indexed_at,embedding_provider,embedding_model,embedding_dimensions,embedding_revision
FROM document_chunks_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash,indexed_at)
SELECT id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash,indexed_at
FROM code_symbols_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number,indexed_at)
SELECT id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number,indexed_at
FROM code_dependencies_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	// A manifest that stopped declaring a package must stop reporting it, so the
	// stored rows are replaced rather than merged. What may be replaced depends on
	// what this run actually saw: an incremental sync is given only the changed
	// files, so replacing the whole ref from that set would delete every manifest
	// the commit did not touch and leave the repository looking dependency-free.
	if incremental {
		for path := range parsedManifests {
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_packages WHERE repository_id=? AND ref_name=? AND manifest_path=?`), repoID, ref.Name, path); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
		// A manifest deleted by this commit leaves the inventory with it.
		for path := range removedPaths {
			_, isManifest := manifest.Recognize(path)
			_, isLock := manifest.RecognizeLock(path)
			if !isManifest && !isLock {
				continue
			}
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_packages WHERE repository_id=? AND ref_name=? AND manifest_path=?`), repoID, ref.Name, path); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
	} else if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_packages WHERE repository_id=? AND ref_name=?`), repoID, ref.Name); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id,indexed_at)
SELECT repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id,indexed_at
FROM repository_packages_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	for _, event := range securityEvents {
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO index_security_events(id,repository_id,ref_name,file_path,finding_type,action) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), event.id, repoID, ref.Name, event.path, event.finding, event.action); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM document_chunks_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM code_symbols_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM code_dependencies_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_packages_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`UPDATE document_chunks SET commit_id=?,indexed_at=? WHERE repository_id=? AND ref_name=?`), ref.LatestCommit, time.Now().UTC(), repoID, ref.Name); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`UPDATE code_symbols SET commit_id=?,indexed_at=? WHERE repository_id=? AND ref_name=?`), ref.LatestCommit, time.Now().UTC(), repoID, ref.Name); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`UPDATE code_dependencies SET commit_id=?,indexed_at=? WHERE repository_id=? AND ref_name=?`), ref.LatestCommit, time.Now().UTC(), repoID, ref.Name); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	var totalChunks, embeddedChunks int
	if err = tx.QueryRowContext(ctx, i.store.Rebind(`SELECT COUNT(*),COUNT(embedding) FROM document_chunks WHERE repository_id=? AND ref_name=?`), repoID, ref.Name).Scan(&totalChunks, &embeddedChunks); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	embeddingStatus, embeddingError := describeEmbeddingState(embeddingRevision, totalChunks, embeddedChunks, embeddingWarnings)
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at,embedding_revision,policy_revision,total_chunks,embedded_chunks,embedding_status,embedding_error)
VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repository_id,ref_name) DO UPDATE SET commit_id=excluded.commit_id,indexed_at=excluded.indexed_at,embedding_revision=excluded.embedding_revision,policy_revision=excluded.policy_revision,total_chunks=excluded.total_chunks,embedded_chunks=excluded.embedded_chunks,embedding_status=excluded.embedding_status,embedding_error=excluded.embedding_error`),
		repoID, ref.Name, ref.LatestCommit, time.Now().UTC(), embeddingRevision, policyRevision, totalChunks, embeddedChunks, embeddingStatus, embeddingError); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_ref_changes WHERE repository_id=? AND ref_name=? AND commit_id=?`), repoID, ref.Name, ref.LatestCommit); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if !incremental {
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_changes(repository_id,ref_name,commit_id,previous_commit_id,file_path,action) VALUES(?,?,?,?,?,'full')`), repoID, ref.Name, ref.LatestCommit, previousCommit, ""); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
	} else {
		for path := range removedPaths {
			ids := deletedChunkIDs[path]
			rawIDs, _ := json.Marshal(ids)
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_changes(repository_id,ref_name,commit_id,previous_commit_id,file_path,action,deleted_chunk_ids) VALUES(?,?,?,?,?,'delete',?)`), repoID, ref.Name, ref.LatestCommit, previousCommit, path, string(rawIDs)); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
		for path := range upsertPaths {
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_changes(repository_id,ref_name,commit_id,previous_commit_id,file_path,action) VALUES(?,?,?,?,?,'upsert')`), repoID, ref.Name, ref.LatestCommit, previousCommit, path); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
		if len(removedPaths) == 0 && len(upsertPaths) == 0 {
			if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_changes(repository_id,ref_name,commit_id,previous_commit_id,file_path,action) VALUES(?,?,?,?,?,'noop')`), repoID, ref.Name, ref.LatestCommit, previousCommit, ""); err != nil {
				_ = tx.Rollback()
				return fail(err)
			}
		}
	}
	if err = i.refreshRepositoryMap(ctx, tx, repoID, ref.Name, ref.LatestCommit); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	warning := ""
	if len(embeddingWarnings) > 0 {
		warning += strings.Join(embeddingWarnings[:min(len(embeddingWarnings), 3)], "; ")
	}
	// An unreadable manifest leaves the dependency inventory incomplete for this
	// repository. The generation is otherwise sound, so it is reported rather
	// than failed — a silent gap would make an advisory search look clean.
	if len(manifestWarnings) > 0 {
		if warning != "" {
			warning += "; "
		}
		warning += strings.Join(manifestWarnings[:min(len(manifestWarnings), 3)], "; ")
	}
	// A completed job with nothing indexed is the most confusing outcome of all,
	// so name the reason instead of leaving a silent zero.
	if candidates == 0 && len(files) > 0 {
		warning = fmt.Sprintf("%d file(s) listed but none matched the index policy; adjust the repository extension policy", len(files))
	}
	return complete(processed, warning)
}

func describeEmbeddingState(revision string, total, embedded int, warnings []string) (string, string) {
	message := ""
	if len(warnings) > 0 {
		message = truncate(strings.Join(warnings[:min(len(warnings), 3)], "; "), 1000)
	}
	switch {
	case total == 0:
		return "empty", message
	case revision == "keyword-only":
		if message != "" {
			return "degraded", message
		}
		return "disabled", ""
	case message != "":
		return "degraded", message
	case embedded == total:
		return "ready", ""
	case embedded == 0:
		return "unavailable", "no chunks have embeddings"
	default:
		return "partial", fmt.Sprintf("%d of %d chunks have embeddings", embedded, total)
	}
}

func (i *Indexer) refreshRepositoryMap(ctx context.Context, tx *sql.Tx, repoID, ref, commit string) error {
	type repositoryMap struct {
		Languages   map[string]int `json:"languages"`
		Symbols     map[string]int `json:"symbols"`
		Directories []string       `json:"directories"`
		KeyFiles    []string       `json:"keyFiles"`
		EntryPoints []string       `json:"entryPoints"`
	}
	summary := repositoryMap{Languages: map[string]int{}, Symbols: map[string]int{}}
	rows, err := tx.QueryContext(ctx, i.store.Rebind(`SELECT language,symbol_kind,qualified_name,file_path FROM code_symbols WHERE repository_id=? AND ref_name=? ORDER BY file_path,line_start`), repoID, ref)
	if err != nil {
		return err
	}
	for rows.Next() {
		var language, kind, qualified, path string
		if err = rows.Scan(&language, &kind, &qualified, &path); err != nil {
			rows.Close()
			return err
		}
		summary.Languages[language]++
		summary.Symbols[kind]++
		if len(summary.EntryPoints) < 50 && (kind == "function" || kind == "method" || kind == "class" || kind == "interface") {
			summary.EntryPoints = append(summary.EntryPoints, path+":"+qualified)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	fileRows, err := tx.QueryContext(ctx, i.store.Rebind(`SELECT DISTINCT file_path FROM document_chunks WHERE repository_id=? AND ref_name=? ORDER BY file_path`), repoID, ref)
	if err != nil {
		return err
	}
	directories := map[string]bool{}
	for fileRows.Next() {
		var path string
		if err = fileRows.Scan(&path); err != nil {
			fileRows.Close()
			return err
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		if len(parts) > 1 {
			directories[parts[0]] = true
		}
		if isKeyFile(path) {
			summary.KeyFiles = append(summary.KeyFiles, path)
		}
	}
	fileRows.Close()
	for directory := range directories {
		summary.Directories = append(summary.Directories, directory)
	}
	sort.Strings(summary.Directories)
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_maps(repository_id,ref_name,commit_id,summary_json,generated_at) VALUES(?,?,?,?,?) ON CONFLICT(repository_id,ref_name) DO UPDATE SET commit_id=excluded.commit_id,summary_json=excluded.summary_json,generated_at=excluded.generated_at`), repoID, ref, commit, string(raw), time.Now().UTC())
	return err
}

func isKeyFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "readme.md", "go.mod", "pom.xml", "build.gradle", "package.json", "dockerfile", "compose.yaml", "docker-compose.yml", "openapi.yaml", "openapi.yml", "swagger.yaml", "chart.yaml", "main.go":
		return true
	default:
		return false
	}
}

// recordFiles refreshes the searchable file listing of a ref. A full sync
// replaces the ref, an incremental sync applies only the changed paths.
// maxManifestsPerRef bounds the extra reads one index run may spend on
// manifests the content policy excluded.
const maxManifestsPerRef = 60

// maxPackagesPerRef bounds the whole inventory of one ref. Sixty lock files can
// declare hundreds of thousands of resolved packages between them; the estate
// view needs the catalogue, not every edge of every dependency tree.
const maxPackagesPerRef = 20000

// packageInsertBatch bounds one multi-row insert. It stays well under the
// parameter limits both engines impose (ten placeholders per row).
const packageInsertBatch = 200

// inventoryPackage is one parsed dependency with the manifest it came from.
type inventoryPackage struct {
	manifest.Package
	ManifestPath string
}

// appendPackages parses one manifest or lock file and adds what it declares.
// A package declared by two files is kept once per file, because "which file
// declares this" is what an upgrade has to edit.
func appendPackages(out []inventoryPackage, path, content string) []inventoryPackage {
	// A manifest states intent and a lock file states the resolved result. Both
	// are recorded: the first is what a team edits, the second is what an
	// advisory can actually judge.
	declared := manifest.Parse(path, content)
	declared = append(declared, manifest.ParseLock(path, content)...)
	for _, item := range declared {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		out = append(out, inventoryPackage{Package: item, ManifestPath: path})
	}
	return out
}

func (i *Indexer) recordFiles(ctx context.Context, tx *sql.Tx, repoID string, ref source.Reference, files []source.File, incremental bool, removed map[string]bool) error {
	if !incremental {
		if _, err := tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_files WHERE repository_id=? AND ref_name=?`), repoID, ref.Name); err != nil {
			return err
		}
	} else {
		for path := range removed {
			if _, err := tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_files WHERE repository_id=? AND ref_name=? AND path=?`), repoID, ref.Name, path); err != nil {
				return err
			}
		}
	}
	now := time.Now().UTC()
	for _, file := range files {
		path := filepath.ToSlash(strings.TrimPrefix(file.Path, "./"))
		if path == "" {
			continue
		}
		indexed := 0
		if i.allowed(file) {
			indexed = 1
		}
		if _, err := tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id,updated_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(repository_id,ref_name,path) DO UPDATE SET base_name=excluded.base_name,size_bytes=excluded.size_bytes,content_indexed=excluded.content_indexed,commit_id=excluded.commit_id,updated_at=excluded.updated_at`),
			repoID, ref.Name, path, strings.ToLower(filepath.Base(path)), file.Size, indexed, ref.LatestCommit, now); err != nil {
			return err
		}
	}
	return nil
}

func (i *Indexer) allowed(f source.File) bool {
	p := strings.TrimPrefix(filepath.ToSlash(f.Path), "./")
	for _, x := range i.policy.ExcludePrefixes {
		if strings.HasPrefix(p, x) {
			return false
		}
	}
	withinSize := f.Size == 0 || f.Size <= i.policy.MaxFileBytes
	ext := strings.ToLower(filepath.Ext(p))
	for _, x := range i.policy.IncludeExtensions {
		if ext == strings.ToLower(strings.TrimSpace(x)) {
			return withinSize
		}
	}
	if ext == "" && indexableByName[strings.ToLower(filepath.Base(p))] {
		return withinSize
	}
	return false
}

type chunk struct {
	Heading, Content string
	Start, End       int
}

var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

func parse(path, content string) []chunk {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if symbols := codeintel.Extract(path, content); len(symbols) > 0 {
		out := make([]chunk, 0, len(symbols))
		for _, symbol := range symbols {
			start, end := max(1, symbol.LineStart), min(len(lines), max(symbol.LineStart, symbol.LineEnd))
			out = append(out, chunk{Heading: symbol.QualifiedName, Content: strings.TrimSpace(strings.Join(lines[start-1:end], "\n")), Start: start, End: end})
		}
		return out
	}
	heading := path
	start := 1
	var body []string
	var out []chunk
	flush := func(end int) {
		text := strings.TrimSpace(strings.Join(body, "\n"))
		if text != "" {
			out = append(out, chunk{Heading: heading, Content: text, Start: start, End: end})
		}
		body = nil
	}
	for n, line := range lines {
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flush(n)
			heading = m[2]
			start = n + 1
			continue
		}
		body = append(body, line)
	}
	flush(len(lines))
	return out
}
func sanitize(content string) (string, string) {
	return contentsecurity.Sanitize(content)
}
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func readable(p string) bool {
	p = strings.ToLower(p)
	switch p {
	case "read", "write", "admin",
		"repo_read", "repo_write", "repo_admin",
		"project_read", "project_write", "project_admin",
		"sys_admin", "reporter", "developer", "maintainer", "owner":
		return true
	default:
		return strings.Contains(p, "read")
	}
}
func contentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdx", ".rst", ".txt", ".adoc", ".asciidoc":
		return "document"
	case ".yaml", ".yml", ".json":
		return "configuration"
	default:
		return "code"
	}
}
func hash(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

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
	"strings"
	"time"
	"unicode"

	"git-ctx/internal/codeintel"
	"git-ctx/internal/contentsecurity"
	"git-ctx/internal/embedding"
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
	return i.syncRepository(ctx, adapter, sourceType, repo, refs, true, "")
}

// ApplyPendingJob indexes content for a job the worker already owns. The job id
// is required so progress, the processed file count and skip warnings land on
// the row the operations screen is showing; the worker still owns the status.
func (i *Indexer) ApplyPendingJob(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference, jobID string) error {
	return i.syncRepository(ctx, adapter, sourceType, repo, refs, false, jobID)
}

func (i *Indexer) syncRepository(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference, trackJobs bool, workerJobID string) error {
	if sourceType != "bitbucket" && sourceType != "gitlab" && sourceType != "confluence" && sourceType != "jira" {
		return errors.New("unsupported source type")
	}
	repoID := sourceType + ":" + fmt.Sprint(repo.ID)
	libraryID := LibraryIDForSource(sourceType, repo.ProjectKey, repo.Slug)
	_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES(?,?,?,?,?,?,?,?,?,1) ON CONFLICT(id) DO UPDATE SET project_key=excluded.project_key,slug=excluded.slug,name=excluded.name,description=excluded.description,default_branch=excluded.default_branch,enabled=1`), repoID, repo.ProjectKey, repo.Slug, repo.Name, repo.Description, sourceType, fmt.Sprint(repo.ID), libraryID, repo.DefaultBranch)
	if err != nil {
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
		if err := i.syncRef(ctx, adapter, repoID, ref, r, trackJobs, workerJobID); err != nil {
			return err
		}
	}
	_, _ = i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE repositories SET indexed_at=? WHERE id=?`), time.Now().UTC(), repoID)
	return nil
}
func (i *Indexer) syncRef(ctx context.Context, adapter source.RepositorySource, repoID string, repo source.RepositoryRef, ref source.Reference, trackJob bool, workerJobID string) error {
	jobID := fmt.Sprintf("%d", time.Now().UnixNano())
	generationID := repoID + ":" + ref.Name + ":" + jobID
	// reportJobID is the row that receives progress and result details. The
	// indexer creates it for direct syncs and reuses the worker row otherwise.
	reportJobID := workerJobID
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
		if trackJob {
			_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`UPDATE index_jobs SET status='failed',error_message=?,completed_at=? WHERE id=?`), truncate(e.Error(), 1000), time.Now().UTC(), jobID)
		}
		return e
	}
	// complete records the finished job. A non-empty warning marks files that
	// were skipped so the operations screen shows partial results instead of a
	// silent gap.
	complete := func(processed int, warning string) error {
		if reportJobID == "" {
			return nil
		}
		if !trackJob {
			// The worker owns the status transition; only the details are ours.
			_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE index_jobs SET files_processed=?,error_message=? WHERE id=?`), processed, truncate(warning, 1000), reportJobID)
			return err
		}
		_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE index_jobs SET status='completed',files_processed=?,error_message=?,completed_at=? WHERE id=?`), processed, truncate(warning, 1000), time.Now().UTC(), reportJobID)
		return err
	}
	embeddingMetadata := embedding.Metadata{}
	metadataKnown := false
	if provider, ok := i.embedder.(embedding.MetadataProvider); ok && i.embedder != nil {
		embeddingMetadata = provider.EmbeddingMetadata()
		metadataKnown = embeddingMetadata.Provider != "" && embeddingMetadata.Model != ""
	}
	embeddingRevision := embeddingMetadata.Identity()
	if i.embedder == nil {
		embeddingRevision = "keyword-only"
	}
	previousCommit := ""
	previousEmbeddingRevision := ""
	err := i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT commit_id,embedding_revision FROM repository_ref_states WHERE repository_id=? AND ref_name=?`), repoID, ref.Name).Scan(&previousCommit, &previousEmbeddingRevision)
	if errors.Is(err, sql.ErrNoRows) {
		err = i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT commit_id FROM document_chunks WHERE repository_id=? AND ref_name=? ORDER BY indexed_at DESC LIMIT 1`), repoID, ref.Name).Scan(&previousCommit)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	if previousCommit != "" && previousCommit == ref.LatestCommit && previousEmbeddingRevision == embeddingRevision {
		var totalChunks, embeddedChunks int
		if err = i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT COUNT(*),COUNT(embedding) FROM document_chunks WHERE repository_id=? AND ref_name=?`), repoID, ref.Name).Scan(&totalChunks, &embeddedChunks); err != nil {
			return fail(err)
		}
		embeddingStatus, embeddingError := describeEmbeddingState(embeddingRevision, totalChunks, embeddedChunks, nil)
		_, err = i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at,embedding_revision,total_chunks,embedded_chunks,embedding_status,embedding_error)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(repository_id,ref_name) DO UPDATE SET commit_id=excluded.commit_id,indexed_at=excluded.indexed_at,embedding_revision=excluded.embedding_revision,total_chunks=excluded.total_chunks,embedded_chunks=excluded.embedded_chunks,embedding_status=excluded.embedding_status,embedding_error=excluded.embedding_error`),
			repoID, ref.Name, ref.LatestCommit, time.Now().UTC(), embeddingRevision, totalChunks, embeddedChunks, embeddingStatus, embeddingError)
		if err != nil {
			return fail(err)
		}
		if err = i.refreshRepositoryMap(ctx, repoID, ref.Name, ref.LatestCommit); err != nil {
			return fail(err)
		}
		return complete(0, "")
	}

	incremental := false
	var files []source.File
	removedPaths := map[string]bool{}
	upsertPaths := map[string]bool{}
	forceFull := previousCommit == ref.LatestCommit && previousEmbeddingRevision != embeddingRevision
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
		if len(texts) > 0 && i.embedder != nil {
			vectors, embedErr := embedding.EmbedAll(ctx, i.embedder, texts)
			if embedErr != nil {
				if i.embeddingRequired {
					return fmt.Errorf("embedding %d chunks near %s: %w", len(texts), pending[positions[0]].filePath, embedErr)
				}
				embeddingWarnings = append(embeddingWarnings,
					fmt.Sprintf("embedding disabled for %d chunk(s) near %s after model error: %s", len(texts), pending[positions[0]].filePath, truncate(embedErr.Error(), 160)))
				// Do not keep calling a broken endpoint for every remaining
				// batch in this generation.
				i.embedder = nil
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
				generationID, chunk.id, repoID, ref.Name, ref.LatestCommit, chunk.filePath, chunk.start, chunk.end, chunk.heading, chunk.contentType, chunk.content, chunk.contentHash, chunk.vector, embeddingMetadata.Provider, embeddingMetadata.Model, embeddingMetadata.Dimensions, embeddingMetadata.Revision); err != nil {
				return err
			}
		}
		pending = pending[:0]
		return nil
	}
	// A single unreadable file must not discard a whole repository index. Skipped
	// files are counted and reported on the job so operators can see what was
	// left out instead of watching the job retry forever.
	var skipped []string
	candidates := 0
	for _, file := range files {
		if !i.allowed(file) {
			continue
		}
		candidates++
		if reportJobID != "" && processed > 0 && processed%indexProgressInterval == 0 {
			_, _ = i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE index_jobs SET files_processed=? WHERE id=?`), processed, reportJobID)
		}
		raw, e := adapter.GetFile(ctx, repo, snapshotRef, file.Path)
		if e != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", file.Path, truncate(e.Error(), 120)))
			continue
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
		for _, symbol := range codeintel.Extract(file.Path, safeContent) {
			symbolID := hash(repoID + "\x00" + ref.Name + "\x00" + file.Path + "\x00" + symbol.QualifiedName + "\x00" + fmt.Sprint(symbol.LineStart))
			_, e = i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO code_symbols_staging(generation_id,id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
				generationID, symbolID, repoID, ref.Name, ref.LatestCommit, filepath.ToSlash(file.Path), symbol.Name, symbol.QualifiedName, symbol.Kind, symbol.Language, symbol.Signature, symbol.Documentation, symbol.LineStart, symbol.LineEnd, hash(symbol.Signature+"\n"+symbol.Documentation))
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
				reuseErr := i.store.DB.QueryRowContext(ctx, i.store.Rebind(`SELECT embedding FROM document_chunks WHERE repository_id=? AND heading=? AND content_hash=? AND embedding_provider=? AND embedding_model=? AND embedding_revision=? AND embedding IS NOT NULL LIMIT 1`), repoID, chunk.Heading, contentHash, embeddingMetadata.Provider, embeddingMetadata.Model, embeddingMetadata.Revision).Scan(&vector)
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
	if len(skipped) > 0 && processed == 0 && candidates > 0 {
		return fail(fmt.Errorf("every indexable file failed to download, first error: %s", skipped[0]))
	}
	tx, err := i.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
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
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at,embedding_revision,total_chunks,embedded_chunks,embedding_status,embedding_error)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(repository_id,ref_name) DO UPDATE SET commit_id=excluded.commit_id,indexed_at=excluded.indexed_at,embedding_revision=excluded.embedding_revision,total_chunks=excluded.total_chunks,embedded_chunks=excluded.embedded_chunks,embedding_status=excluded.embedding_status,embedding_error=excluded.embedding_error`),
		repoID, ref.Name, ref.LatestCommit, time.Now().UTC(), embeddingRevision, totalChunks, embeddedChunks, embeddingStatus, embeddingError); err != nil {
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
	if err = tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if err = i.refreshRepositoryMap(ctx, repoID, ref.Name, ref.LatestCommit); err != nil {
		return fail(err)
	}
	warning := ""
	if len(skipped) > 0 {
		warning = fmt.Sprintf("%d file(s) skipped: %s", len(skipped), strings.Join(skipped[:min(len(skipped), 5)], "; "))
	}
	if len(embeddingWarnings) > 0 {
		if warning != "" {
			warning += "; "
		}
		warning += strings.Join(embeddingWarnings[:min(len(embeddingWarnings), 3)], "; ")
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

func (i *Indexer) refreshRepositoryMap(ctx context.Context, repoID, ref, commit string) error {
	type repositoryMap struct {
		Languages   map[string]int `json:"languages"`
		Symbols     map[string]int `json:"symbols"`
		Directories []string       `json:"directories"`
		KeyFiles    []string       `json:"keyFiles"`
		EntryPoints []string       `json:"entryPoints"`
	}
	summary := repositoryMap{Languages: map[string]int{}, Symbols: map[string]int{}}
	rows, err := i.store.DB.QueryContext(ctx, i.store.Rebind(`SELECT language,symbol_kind,qualified_name,file_path FROM code_symbols WHERE repository_id=? AND ref_name=? ORDER BY file_path,line_start`), repoID, ref)
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
	fileRows, err := i.store.DB.QueryContext(ctx, i.store.Rebind(`SELECT DISTINCT file_path FROM document_chunks WHERE repository_id=? AND ref_name=? ORDER BY file_path`), repoID, ref)
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
	_, err = i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_maps(repository_id,ref_name,commit_id,summary_json,generated_at) VALUES(?,?,?,?,?) ON CONFLICT(repository_id,ref_name) DO UPDATE SET commit_id=excluded.commit_id,summary_json=excluded.summary_json,generated_at=excluded.generated_at`), repoID, ref, commit, string(raw), time.Now().UTC())
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

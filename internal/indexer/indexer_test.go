package indexer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git-ctx/internal/embedding"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("model unavailable")
}

type countingEmbedder struct {
	calls    int
	revision string
}

func (e *countingEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.calls++
	return []float32{1, 0, 0}, nil
}
func (e *countingEmbedder) EmbeddingMetadata() embedding.Metadata {
	revision := e.revision
	if revision == "" {
		revision = "v1"
	}
	return embedding.Metadata{Provider: "test", Model: "counting", Revision: revision, Dimensions: 3}
}

type incrementalSource struct {
	fakeSource
	files       map[string]string
	changes     []source.Change
	changeErr   error
	listCalls   int
	getFileCall int
	getHook     func()
}

func (s *incrementalSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	s.listCalls++
	out := make([]source.File, 0, len(s.files))
	for path := range s.files {
		out = append(out, source.File{Path: path})
	}
	return out, nil
}
func (s *incrementalSource) GetFile(_ context.Context, _ source.RepositoryRef, _ string, path string) ([]byte, error) {
	s.getFileCall++
	if s.getHook != nil {
		s.getHook()
	}
	return []byte(s.files[path]), nil
}
func (s *incrementalSource) Changes(context.Context, source.RepositoryRef, string, string) ([]source.Change, error) {
	return s.changes, s.changeErr
}

type fakeSource struct{}

func (fakeSource) ListProjects(context.Context) ([]source.Project, error) { return nil, nil }
func (fakeSource) ListRepositories(context.Context, string) ([]source.Repository, error) {
	return nil, nil
}

func TestSanitizeBlocksPrivateKeysAndRedactsCredentials(t *testing.T) {
	if safe, finding := sanitize("-----BEGIN PRIVATE KEY-----\nabc"); safe != "" || finding != "private_key" {
		t.Fatalf("safe=%q finding=%s", safe, finding)
	}
	safe, finding := sanitize("password = super-secret-value\nkey AKIA1234567890123456")
	if finding == "" || safe == "password = super-secret-value\nkey AKIA1234567890123456" {
		t.Fatalf("content not redacted: %q %s", safe, finding)
	}
	if strings.Contains(safe, "super-secret-value") || strings.Contains(safe, "AKIA1234567890123456") {
		t.Fatalf("secret remained: %q", safe)
	}
}

func TestSanitizeKnownTokensDSNAndEntropyWithoutMaskingCommitHashes(t *testing.T) {
	content := strings.Join([]string{
		"token glpat-1234567890abcdefghijklmnop",
		"dsn postgresql://alice:secret@db.internal/gitctx",
		"opaque aZ9xY8wV7uT6sR5qP4oN3mL2kJ1hG0fE9dC8bA7",
		"commit 0123456789abcdef0123456789abcdef01234567",
	}, "\n")
	safe, finding := sanitize(content)
	if finding == "" || strings.Contains(safe, "glpat-") || strings.Contains(safe, "alice:secret") || strings.Contains(safe, "aZ9xY8") {
		t.Fatalf("secrets were not redacted: finding=%s content=%s", finding, safe)
	}
	if !strings.Contains(safe, "0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("commit hash was incorrectly redacted: %s", safe)
	}
}

func TestReadableIncludesInheritedBitbucketPermissionLevels(t *testing.T) {
	for _, permission := range []string{"READ", "WRITE", "ADMIN", "REPO_ADMIN", "PROJECT_WRITE", "SYS_ADMIN"} {
		if !readable(permission) {
			t.Errorf("%s should imply repository read access", permission)
		}
	}
	for _, permission := range []string{"NONE", "LICENSED_USER", "GUEST"} {
		if readable(permission) {
			t.Errorf("%s must not imply repository read access", permission)
		}
	}
}
func (fakeSource) ListBranches(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, nil
}
func (fakeSource) ListTags(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, nil
}
func (fakeSource) GetCommit(context.Context, source.RepositoryRef, string) (source.Commit, error) {
	return source.Commit{}, nil
}
func (fakeSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	return []source.File{{Path: "README.md"}, {Path: "node_modules/secret.md"}, {Path: ".env"}}, nil
}
func (fakeSource) GetFile(_ context.Context, _ source.RepositoryRef, _ string, p string) ([]byte, error) {
	return []byte("# GPU Guide\nUse DCGM exporter.\n\n## API\nCall GetGPU()."), nil
}
func (fakeSource) GetPermissions(context.Context, source.RepositoryRef) ([]source.Permission, error) {
	return []source.Permission{{Principal: "alice", Kind: "user", Permission: "REPO_READ"}, {Principal: "engineering", Kind: "group", Permission: "REPO_READ"}, {Principal: "bob", Kind: "user", Permission: "none"}}, nil
}
func (fakeSource) RegisterWebhook(context.Context, source.RepositoryRef, string, string) error {
	return nil
}

func TestSyncRepositoryAppliesPolicyACLAndChunks(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	idx := New(s, DefaultPolicy())
	repo := source.Repository{ID: 7, ProjectKey: "KCB", Slug: "GPU Platform", Name: "GPU Platform", DefaultBranch: "main"}
	err = idx.SyncRepository(ctx, fakeSource{}, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "abc123"}})
	if err != nil {
		t.Fatal(err)
	}
	var libraryID string
	if err = s.DB.QueryRow(`SELECT library_id FROM repositories WHERE id='bitbucket:7'`).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	if libraryID != "/kcb/gpu-platform" {
		t.Fatalf("library ID=%s", libraryID)
	}
	var perms int
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_permissions WHERE repository_id='bitbucket:7'`).Scan(&perms); err != nil || perms != 2 {
		t.Fatalf("permissions=%d err=%v", perms, err)
	}
	var groupCount int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_permissions WHERE repository_id='bitbucket:7' AND principal='group:engineering'`).Scan(&groupCount)
	if groupCount != 1 {
		t.Fatal("group permission was not namespaced")
	}
	var chunks int
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='bitbucket:7' AND ref_name='main'`).Scan(&chunks); err != nil || chunks != 2 {
		t.Fatalf("chunks=%d err=%v", chunks, err)
	}
	var status string
	var files int
	if err = s.DB.QueryRow(`SELECT status,files_processed FROM index_jobs LIMIT 1`).Scan(&status, &files); err != nil || status != "completed" || files != 1 {
		t.Fatalf("job=%s files=%d err=%v", status, files, err)
	}
}

// batchEmbedder records how the indexer calls the model so the test can prove
// chunks are vectorized in batches rather than one request each.
type batchEmbedder struct {
	countingEmbedder
	batches    int
	batchSizes []int
}

func (e *batchEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	e.batches++
	e.batchSizes = append(e.batchSizes, len(texts))
	e.calls += len(texts)
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = []float32{1, 0, 0}
	}
	return vectors, nil
}

// unreliableSource fails one file download the way a remote server does for LFS
// pointers, permission edges or files removed between listing and fetching.
type unreliableSource struct {
	fakeSource
	files  []string
	broken string
}

func (s *unreliableSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	out := make([]source.File, 0, len(s.files))
	for _, path := range s.files {
		out = append(out, source.File{Path: path})
	}
	return out, nil
}
func (s *unreliableSource) GetFile(_ context.Context, _ source.RepositoryRef, _ string, path string) ([]byte, error) {
	if path == s.broken {
		return nil, errors.New("404 file not found")
	}
	return []byte("# Title\nbody text\n\n## Second\nmore text"), nil
}

func TestIndexBatchesEmbeddingsAndSurvivesOneUnreadableFile(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:index-resilience?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	remote := &unreliableSource{files: []string{"a.md", "b.md", "broken.md", "c.md"}, broken: "broken.md"}
	model := &batchEmbedder{}
	repo := source.Repository{ID: 9, ProjectKey: "kcb", Slug: "docs", Name: "Docs", DefaultBranch: "main"}
	if err = NewWithEmbedder(s, DefaultPolicy(), model).SyncRepository(ctx, remote, "gitlab", repo, []source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatalf("one unreadable file must not fail the repository index: %v", err)
	}
	var status, warning string
	var files int
	if err = s.DB.QueryRow(`SELECT status,files_processed,error_message FROM index_jobs LIMIT 1`).Scan(&status, &files, &warning); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || files != 3 {
		t.Fatalf("job status=%s files=%d", status, files)
	}
	if !strings.Contains(warning, "broken.md") || !strings.Contains(warning, "1 file(s) skipped") {
		t.Fatalf("skipped file was not reported: %q", warning)
	}
	var chunks int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='gitlab:9'`).Scan(&chunks)
	if chunks != 6 {
		t.Fatalf("chunks=%d, want two per readable file", chunks)
	}
	// Six chunks must not cost six requests.
	if model.batches != 1 || model.batchSizes[0] != 6 {
		t.Fatalf("embeddings were not batched: batches=%d sizes=%v", model.batches, model.batchSizes)
	}

	// Every file failing is a real failure, not a silently empty index.
	broken := &unreliableSource{files: []string{"only.md"}, broken: "only.md"}
	err = NewWithEmbedder(s, DefaultPolicy(), model).SyncRepository(ctx, broken, "gitlab", source.Repository{ID: 10, ProjectKey: "kcb", Slug: "empty", DefaultBranch: "main"}, []source.Reference{{Name: "main", LatestCommit: "c1"}})
	if err == nil || !strings.Contains(err.Error(), "every indexable file failed") {
		t.Fatalf("expected a hard failure when nothing could be downloaded, got %v", err)
	}
}

func TestFailedGenerationPreservesActiveChunksAndCleansStaging(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	repo := source.Repository{ID: 7, ProjectKey: "KCB", Slug: "demo", Name: "Demo", DefaultBranch: "main"}
	ref := []source.Reference{{Name: "main", LatestCommit: "abc123"}}
	if err = New(s, DefaultPolicy()).SyncRepository(ctx, fakeSource{}, "bitbucket", repo, ref); err != nil {
		t.Fatal(err)
	}
	var before int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='bitbucket:7'`).Scan(&before)
	_, _ = s.DB.Exec(`UPDATE document_chunks SET embedding=NULL WHERE repository_id='bitbucket:7'`)
	ref[0].LatestCommit = "abc124"
	if err = NewWithEmbedder(s, DefaultPolicy(), failingEmbedder{}).SyncRepository(ctx, fakeSource{}, "bitbucket", repo, ref); err == nil {
		t.Fatal("expected embedding failure")
	}
	var after, staged int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='bitbucket:7'`).Scan(&after)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_staging`).Scan(&staged)
	if after != before || staged != 0 {
		t.Fatalf("active chunks changed after failed generation: before=%d after=%d staged=%d", before, after, staged)
	}
}

func TestOptionalEmbeddingFailureCompletesLexicalGeneration(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:optional-embedding?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	repo := source.Repository{ID: 71, ProjectKey: "KCB", Slug: "lexical", Name: "Lexical", DefaultBranch: "main"}
	err = NewWithOptionalEmbedder(s, DefaultPolicy(), failingEmbedder{}).SyncRepository(ctx, fakeSource{}, "bitbucket", repo,
		[]source.Reference{{Name: "main", LatestCommit: "abc123"}})
	if err != nil {
		t.Fatalf("optional embedding must not fail indexing: %v", err)
	}
	var chunks, vectors int
	_ = s.DB.QueryRow(`SELECT COUNT(*),COUNT(embedding) FROM document_chunks WHERE repository_id='bitbucket:71'`).Scan(&chunks, &vectors)
	if chunks == 0 || vectors != 0 {
		t.Fatalf("chunks=%d vectors=%d", chunks, vectors)
	}
	var status, warning string
	_ = s.DB.QueryRow(`SELECT status,COALESCE(error_message,'') FROM index_jobs ORDER BY created_at DESC LIMIT 1`).Scan(&status, &warning)
	if status != "completed" || !strings.Contains(warning, "embedding disabled") {
		t.Fatalf("status=%s warning=%q", status, warning)
	}
	var embeddingStatus string
	var totalChunks, embeddedChunks int
	if err = s.DB.QueryRow(`SELECT embedding_status,total_chunks,embedded_chunks
FROM repository_ref_states WHERE repository_id='bitbucket:71' AND ref_name='main'`).Scan(
		&embeddingStatus, &totalChunks, &embeddedChunks,
	); err != nil {
		t.Fatal(err)
	}
	if embeddingStatus != "degraded" || totalChunks != chunks || embeddedChunks != 0 {
		t.Fatalf("embedding status=%s coverage=%d/%d want degraded 0/%d", embeddingStatus, embeddedChunks, totalChunks, chunks)
	}
}

func TestIncrementalIndexOnlyFetchesChangesAndReusesEmbeddings(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	src := &incrementalSource{files: map[string]string{
		"docs/a.md": "# One\nsame\n# Two\nold",
		"docs/b.md": "# Removed\nobsolete",
	}}
	embedder := &countingEmbedder{}
	idx := NewWithEmbedder(s, DefaultPolicy(), embedder)
	repo := source.Repository{ID: 9, ProjectKey: "KCB", Slug: "incremental", Name: "Incremental", DefaultBranch: "main"}
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatal(err)
	}
	if src.listCalls != 1 || src.getFileCall != 2 || embedder.calls != 3 {
		t.Fatalf("initial list=%d files=%d embeddings=%d", src.listCalls, src.getFileCall, embedder.calls)
	}
	src.files = map[string]string{"docs/a.md": "# One\nsame\n# Two\nnew"}
	src.changes = []source.Change{
		{Path: "docs/a.md", OldPath: "docs/a.md", Type: "modified"},
		{Path: "docs/b.md", OldPath: "docs/b.md", Type: "deleted"},
	}
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c2"}}); err != nil {
		t.Fatal(err)
	}
	if src.listCalls != 1 || src.getFileCall != 3 {
		t.Fatalf("incremental path performed full scan: list=%d files=%d", src.listCalls, src.getFileCall)
	}
	if embedder.calls != 4 {
		t.Fatalf("unchanged chunk embedding was not reused: calls=%d", embedder.calls)
	}
	var paths, commit, state string
	if err = s.DB.QueryRow(`SELECT GROUP_CONCAT(DISTINCT file_path),MIN(commit_id) FROM document_chunks WHERE repository_id='bitbucket:9'`).Scan(&paths, &commit); err != nil {
		t.Fatal(err)
	}
	_ = s.DB.QueryRow(`SELECT commit_id FROM repository_ref_states WHERE repository_id='bitbucket:9' AND ref_name='main'`).Scan(&state)
	if paths != "docs/a.md" || commit != "c2" || state != "c2" {
		t.Fatalf("paths=%q commit=%q state=%q", paths, commit, state)
	}
	listCalls, getCalls, embeddingCalls := src.listCalls, src.getFileCall, embedder.calls
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c2"}}); err != nil {
		t.Fatal(err)
	}
	if src.listCalls != listCalls || src.getFileCall != getCalls || embedder.calls != embeddingCalls {
		t.Fatalf("same commit performed work: list=%d files=%d embeddings=%d", src.listCalls-listCalls, src.getFileCall-getCalls, embedder.calls-embeddingCalls)
	}

	src.files = map[string]string{"docs/renamed.md": "# One\nsame\n# Two\nnew"}
	src.changes = []source.Change{{Path: "docs/renamed.md", OldPath: "docs/a.md", Type: "renamed"}}
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c3"}}); err != nil {
		t.Fatal(err)
	}
	if embedder.calls != embeddingCalls {
		t.Fatalf("rename re-embedded unchanged content: calls=%d want=%d", embedder.calls, embeddingCalls)
	}
	if err = s.DB.QueryRow(`SELECT GROUP_CONCAT(DISTINCT file_path) FROM document_chunks WHERE repository_id='bitbucket:9'`).Scan(&paths); err != nil || paths != "docs/renamed.md" {
		t.Fatalf("renamed paths=%q err=%v", paths, err)
	}

	embedder.revision = "v2"
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c3"}}); err != nil {
		t.Fatal(err)
	}
	if src.listCalls != listCalls+1 || embedder.calls != embeddingCalls+2 {
		t.Fatalf("model revision did not force full re-embedding: list=%d embeddings=%d", src.listCalls, embedder.calls)
	}
}

func TestChangeFeedFailureFallsBackToFullIndex(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	src := &incrementalSource{files: map[string]string{"README.md": "# First\none"}}
	repo := source.Repository{ID: 10, ProjectKey: "KCB", Slug: "fallback", Name: "Fallback", DefaultBranch: "main"}
	idx := New(s, DefaultPolicy())
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatal(err)
	}
	src.files["README.md"] = "# Second\ntwo"
	src.changeErr = errors.New("compare history unavailable")
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c2"}}); err != nil {
		t.Fatal(err)
	}
	if src.listCalls != 2 {
		t.Fatalf("expected safe full fallback, list calls=%d", src.listCalls)
	}
}

func TestConcurrentRefActivationCannotOverwriteNewerState(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	src := &incrementalSource{files: map[string]string{"README.md": "# First\none"}}
	repo := source.Repository{ID: 11, ProjectKey: "KCB", Slug: "concurrent", Name: "Concurrent", DefaultBranch: "main"}
	idx := New(s, DefaultPolicy())
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatal(err)
	}
	src.files["README.md"] = "# Second\ntwo"
	src.changes = []source.Change{{Path: "README.md", OldPath: "README.md", Type: "modified"}}
	src.getHook = func() {
		src.getHook = nil
		_, _ = s.DB.Exec(`UPDATE repository_ref_states SET commit_id='c3' WHERE repository_id='bitbucket:11' AND ref_name='main'`)
	}
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c2"}}); err == nil || !strings.Contains(err.Error(), "index state changed concurrently") {
		t.Fatalf("expected optimistic activation conflict, got %v", err)
	}
	var content, commit string
	_ = s.DB.QueryRow(`SELECT content,commit_id FROM document_chunks WHERE repository_id='bitbucket:11'`).Scan(&content, &commit)
	if !strings.Contains(content, "one") || commit != "c1" {
		t.Fatalf("concurrent activation changed active chunks: content=%q commit=%q", content, commit)
	}
}

func TestCodeSymbolsAndRepositoryMapFollowIncrementalChanges(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	src := &incrementalSource{files: map[string]string{
		"main.go": "package main\n\nfunc main() { Run() }\n\n// Run starts work.\nfunc Run() {}\n",
		"go.mod":  "module example.local/demo\n",
	}}
	repo := source.Repository{ID: 12, ProjectKey: "KCB", Slug: "symbols", Name: "Symbols", DefaultBranch: "main"}
	idx := New(s, DefaultPolicy())
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatal(err)
	}
	var symbols, chunks, dependencies int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM code_symbols WHERE repository_id='bitbucket:12'`).Scan(&symbols)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='bitbucket:12' AND file_path='main.go'`).Scan(&chunks)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM code_dependencies WHERE repository_id='bitbucket:12' AND target='Run'`).Scan(&dependencies)
	if symbols != 2 || chunks != 2 || dependencies != 1 {
		t.Fatalf("symbols=%d AST chunks=%d dependencies=%d", symbols, chunks, dependencies)
	}
	var summary string
	_ = s.DB.QueryRow(`SELECT summary_json FROM repository_maps WHERE repository_id='bitbucket:12' AND ref_name='main'`).Scan(&summary)
	if !strings.Contains(summary, `"go":2`) || !strings.Contains(summary, `"main.go"`) {
		t.Fatalf("repository map=%s", summary)
	}
	src.files["main.go"] = "package main\n\nfunc main() {}\n"
	src.changes = []source.Change{{Path: "main.go", OldPath: "main.go", Type: "modified"}}
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: "c2"}}); err != nil {
		t.Fatal(err)
	}
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM code_symbols WHERE repository_id='bitbucket:12'`).Scan(&symbols)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM code_dependencies WHERE repository_id='bitbucket:12'`).Scan(&dependencies)
	if symbols != 1 || dependencies != 0 {
		t.Fatalf("removed code intelligence survived incremental activation: symbols=%d dependencies=%d", symbols, dependencies)
	}
}

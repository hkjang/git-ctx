package indexer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/embedding"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("model unavailable")
}

type failOnceEmbedder struct{ calls int }

func (e *failOnceEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.calls++
	if e.calls == 1 {
		return nil, errors.New("temporary model outage")
	}
	return []float32{1, 0, 0}, nil
}
func (*failOnceEmbedder) EmbeddingMetadata() embedding.Metadata {
	return embedding.Metadata{Provider: "test", Model: "flaky", Revision: "v1", Dimensions: 3}
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
	getErrs     map[string]error
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
	if err := s.getErrs[path]; err != nil {
		return nil, err
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

// batchingSource returns the same multi-section document for each listed path.
type batchingSource struct {
	fakeSource
	files []string
}

func (s *batchingSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	out := make([]source.File, 0, len(s.files))
	for _, path := range s.files {
		out = append(out, source.File{Path: path})
	}
	return out, nil
}
func (s *batchingSource) GetFile(context.Context, source.RepositoryRef, string, string) ([]byte, error) {
	return []byte("# Title\nbody text\n\n## Second\nmore text"), nil
}

type leaseStealingFilesSource struct {
	batchingSource
	steal func()
	calls int
}

func (s *leaseStealingFilesSource) GetFile(context.Context, source.RepositoryRef, string, string) ([]byte, error) {
	s.calls++
	if s.calls == indexProgressInterval && s.steal != nil {
		s.steal()
	}
	return []byte("# Title\nbody text"), nil
}

func TestProgressUpdateRejectsStaleWorkerLease(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:index-progress-lease?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	startedAt := time.Now().UTC().Add(-time.Minute)
	newStartedAt := time.Now().UTC().Add(time.Minute)
	repo := source.Repository{ID: 91, ProjectKey: "KCB", Slug: "lease", Name: "Lease", DefaultBranch: "main"}
	_, err = s.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,started_at,attempts)
VALUES('lease-progress','bitbucket:91','main','webhook','running',?,1)`, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, indexProgressInterval+1)
	for n := range paths {
		paths[n] = "doc-" + string(rune('a'+n)) + ".md"
	}
	remote := &leaseStealingFilesSource{batchingSource: batchingSource{files: paths}}
	remote.steal = func() {
		if _, updateErr := s.DB.Exec(`UPDATE index_jobs
SET started_at=?,error_message='new worker owns this lease'
WHERE id='lease-progress'`, newStartedAt); updateErr != nil {
			t.Errorf("steal lease: %v", updateErr)
		}
	}

	err = New(s, DefaultPolicy()).ApplyPendingJob(ctx, remote, "bitbucket", repo,
		[]source.Reference{{Name: "main", LatestCommit: "c1"}},
		JobLease{ID: "lease-progress", StartedAt: startedAt})
	if err == nil || !strings.Contains(err.Error(), "lease changed") {
		t.Fatalf("ApplyPendingJob error=%v, want lease loss", err)
	}
	if remote.calls != indexProgressInterval {
		t.Fatalf("remote reads=%d, want stale worker stopped at %d", remote.calls, indexProgressInterval)
	}
	var status, message string
	var claimedAt time.Time
	if err = s.DB.QueryRow(`SELECT status,error_message,started_at FROM index_jobs WHERE id='lease-progress'`).Scan(&status, &message, &claimedAt); err != nil {
		t.Fatal(err)
	}
	if status != "running" || message != "new worker owns this lease" || !claimedAt.Equal(newStartedAt) {
		t.Fatalf("new lease changed: status=%q message=%q started=%v want=%v", status, message, claimedAt, newStartedAt)
	}
	var staged, chunks, states, maps int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_staging`).Scan(&staged)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='bitbucket:91'`).Scan(&chunks)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_ref_states WHERE repository_id='bitbucket:91'`).Scan(&states)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_maps WHERE repository_id='bitbucket:91'`).Scan(&maps)
	if staged != 0 || chunks != 0 || states != 0 || maps != 0 {
		t.Fatalf("stale generation leaked: staged=%d chunks=%d states=%d maps=%d", staged, chunks, states, maps)
	}
}

func TestIndexBatchesEmbeddings(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:index-resilience?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	remote := &batchingSource{files: []string{"a.md", "b.md", "c.md"}}
	model := &batchEmbedder{}
	repo := source.Repository{ID: 9, ProjectKey: "kcb", Slug: "docs", Name: "Docs", DefaultBranch: "main"}
	if err = NewWithEmbedder(s, DefaultPolicy(), model).SyncRepository(ctx, remote, "gitlab", repo, []source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatal(err)
	}
	var status, warning string
	var files int
	if err = s.DB.QueryRow(`SELECT status,files_processed,error_message FROM index_jobs LIMIT 1`).Scan(&status, &files, &warning); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || files != 3 {
		t.Fatalf("job status=%s files=%d", status, files)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %q", warning)
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
}

func TestPolicySkippedFilesDoNotTriggerRemoteReadFailures(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:policy-skip?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	src := &incrementalSource{
		files: map[string]string{
			"README.md":         "# Included\nbody",
			"vendor/ignored.md": "# Excluded by path\nbody",
			"diagram.png":       "not really an image",
		},
		getErrs: map[string]error{
			"vendor/ignored.md": errors.New("excluded path must not be fetched"),
			"diagram.png":       errors.New("excluded extension must not be fetched"),
		},
	}
	repo := source.Repository{ID: 75, ProjectKey: "KCB", Slug: "policy", Name: "Policy", DefaultBranch: "main"}
	if err = NewWithoutEmbeddings(s, DefaultPolicy()).SyncRepository(ctx, src, "bitbucket", repo,
		[]source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatalf("intentional policy skips must complete: %v", err)
	}
	if src.getFileCall != 1 {
		t.Fatalf("remote reads=%d, want only the policy-accepted file", src.getFileCall)
	}
	var state, status string
	var filesProcessed, listed, indexed int
	if err = s.DB.QueryRow(`SELECT commit_id FROM repository_ref_states
WHERE repository_id='bitbucket:75' AND ref_name='main'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT status,files_processed FROM index_jobs
WHERE repository_id='bitbucket:75' ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&status, &filesProcessed); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(content_indexed),0) FROM repository_files
WHERE repository_id='bitbucket:75' AND ref_name='main'`).Scan(&listed, &indexed); err != nil {
		t.Fatal(err)
	}
	if state != "c1" || status != "completed" || filesProcessed != 1 || listed != 3 || indexed != 1 {
		t.Fatalf("state=%q status=%q processed=%d listed=%d indexed=%d", state, status, filesProcessed, listed, indexed)
	}
}

func TestRemoteReadFailurePreservesActiveRefGeneration(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:remote-read-atomicity?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	src := &incrementalSource{files: map[string]string{
		"a.go": "package demo\n\nfunc OldA() {}\n",
		"b.go": "package demo\n\nfunc OldB() {}\n",
	}}
	repo := source.Repository{ID: 76, ProjectKey: "KCB", Slug: "atomic", Name: "Atomic", DefaultBranch: "main"}
	idx := NewWithoutEmbeddings(s, DefaultPolicy())
	if err = idx.SyncRepository(ctx, src, "bitbucket", repo,
		[]source.Reference{{Name: "main", LatestCommit: "c1"}}); err != nil {
		t.Fatal(err)
	}
	var beforeChunks, beforeSymbols, beforeFiles int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&beforeChunks)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM code_symbols WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&beforeSymbols)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_files WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&beforeFiles)

	src.files = map[string]string{
		"a.go": "package demo\n\n" + strings.Repeat("func NewA() {}\n\n", embeddingBatchSize+1),
		"b.go": "package demo\n\nfunc NewB() {}\n",
	}
	src.changes = []source.Change{
		{Path: "a.go", OldPath: "a.go", Type: "modified"},
		{Path: "b.go", OldPath: "b.go", Type: "modified"},
	}
	src.getErrs = map[string]error{"b.go": errors.New("upstream object read failed")}
	err = idx.SyncRepository(ctx, src, "bitbucket", repo,
		[]source.Reference{{Name: "main", LatestCommit: "c2"}})
	if err == nil || !strings.Contains(err.Error(), "b.go") || !strings.Contains(err.Error(), "upstream object read failed") {
		t.Fatalf("expected the accepted remote read to fail the generation, got %v", err)
	}

	var stateCommit, mapCommit string
	if err = s.DB.QueryRow(`SELECT commit_id FROM repository_ref_states
WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&stateCommit); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT commit_id FROM repository_maps
WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&mapCommit); err != nil {
		t.Fatal(err)
	}
	var afterChunks, c2Chunks, newChunks int
	if err = s.DB.QueryRow(`SELECT COUNT(*),
COALESCE(SUM(CASE WHEN commit_id='c2' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN content LIKE '%NewA%' OR content LIKE '%NewB%' THEN 1 ELSE 0 END),0)
FROM document_chunks WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&afterChunks, &c2Chunks, &newChunks); err != nil {
		t.Fatal(err)
	}
	var afterSymbols, c2Symbols, newSymbols int
	if err = s.DB.QueryRow(`SELECT COUNT(*),
COALESCE(SUM(CASE WHEN commit_id='c2' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN name IN ('NewA','NewB') THEN 1 ELSE 0 END),0)
FROM code_symbols WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&afterSymbols, &c2Symbols, &newSymbols); err != nil {
		t.Fatal(err)
	}
	var afterFiles, c2Files, c2Changes int
	_ = s.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN commit_id='c2' THEN 1 ELSE 0 END),0)
FROM repository_files WHERE repository_id='bitbucket:76' AND ref_name='main'`).Scan(&afterFiles, &c2Files)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_ref_changes
WHERE repository_id='bitbucket:76' AND ref_name='main' AND commit_id='c2'`).Scan(&c2Changes)
	var stagedChunks, stagedSymbols, stagedDependencies int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_staging`).Scan(&stagedChunks)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM code_symbols_staging`).Scan(&stagedSymbols)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM code_dependencies_staging`).Scan(&stagedDependencies)
	var failedJob int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs
WHERE repository_id='bitbucket:76' AND status='failed' AND error_message LIKE '%b.go%'`).Scan(&failedJob)

	if stateCommit != "c1" || mapCommit != "c1" {
		t.Fatalf("active metadata advanced after failed read: state=%q map=%q", stateCommit, mapCommit)
	}
	if afterChunks != beforeChunks || c2Chunks != 0 || newChunks != 0 {
		t.Fatalf("active chunks changed: before=%d after=%d c2=%d new=%d", beforeChunks, afterChunks, c2Chunks, newChunks)
	}
	if afterSymbols != beforeSymbols || c2Symbols != 0 || newSymbols != 0 {
		t.Fatalf("active symbols changed: before=%d after=%d c2=%d new=%d", beforeSymbols, afterSymbols, c2Symbols, newSymbols)
	}
	if afterFiles != beforeFiles || c2Files != 0 || c2Changes != 0 {
		t.Fatalf("active file/ref history changed: before=%d after=%d c2 files=%d c2 changes=%d", beforeFiles, afterFiles, c2Files, c2Changes)
	}
	if stagedChunks != 0 || stagedSymbols != 0 || stagedDependencies != 0 || failedJob != 1 {
		t.Fatalf("cleanup/job mismatch: staged chunks=%d symbols=%d dependencies=%d failed jobs=%d",
			stagedChunks, stagedSymbols, stagedDependencies, failedJob)
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

func TestOptionalEmbeddingFailureDoesNotDisableLaterRepositories(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:optional-embedding-recovery?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	embedder := &failOnceEmbedder{}
	idx := NewWithOptionalEmbedder(s, DefaultPolicy(), embedder)
	first := source.Repository{ID: 73, ProjectKey: "KCB", Slug: "first", Name: "First", DefaultBranch: "main"}
	if err = idx.SyncRepository(ctx, fakeSource{}, "bitbucket", first,
		[]source.Reference{{Name: "main", LatestCommit: "first-commit"}}); err != nil {
		t.Fatal(err)
	}
	second := source.Repository{ID: 74, ProjectKey: "KCB", Slug: "second", Name: "Second", DefaultBranch: "main"}
	if err = idx.SyncRepository(ctx, fakeSource{}, "bitbucket", second,
		[]source.Reference{{Name: "main", LatestCommit: "second-commit"}}); err != nil {
		t.Fatal(err)
	}
	var chunks, vectors int
	if err = s.DB.QueryRow(`SELECT COUNT(*),COUNT(embedding) FROM document_chunks WHERE repository_id='bitbucket:74'`).Scan(&chunks, &vectors); err != nil {
		t.Fatal(err)
	}
	if embedder.calls < 2 || chunks == 0 || vectors != chunks {
		t.Fatalf("provider calls=%d second repository vectors=%d/%d", embedder.calls, vectors, chunks)
	}
}

func TestEmbeddingIdentityIsStoredConsistentlyOnChunksAndRefState(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:embedding-identity?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	embedder := &countingEmbedder{revision: "https://models.internal/v1/embeddings"}
	repo := source.Repository{ID: 72, ProjectKey: "KCB", Slug: "identity", Name: "Identity", DefaultBranch: "main"}
	if err = NewWithEmbedder(s, DefaultPolicy(), embedder).SyncRepository(ctx, fakeSource{}, "bitbucket", repo,
		[]source.Reference{{Name: "main", LatestCommit: "abc123"}}); err != nil {
		t.Fatal(err)
	}
	expected := embedder.EmbeddingMetadata().Identity()
	var stateRevision string
	if err = s.DB.QueryRow(`SELECT embedding_revision FROM repository_ref_states
WHERE repository_id='bitbucket:72' AND ref_name='main'`).Scan(&stateRevision); err != nil {
		t.Fatal(err)
	}
	var chunks, mismatches int
	if err = s.DB.QueryRow(`SELECT COUNT(*),
COALESCE(SUM(CASE WHEN c.embedding_revision<>s.embedding_revision THEN 1 ELSE 0 END),0)
FROM document_chunks c JOIN repository_ref_states s
  ON s.repository_id=c.repository_id AND s.ref_name=c.ref_name
WHERE c.repository_id='bitbucket:72' AND c.ref_name='main' AND c.embedding IS NOT NULL`).Scan(&chunks, &mismatches); err != nil {
		t.Fatal(err)
	}
	if chunks == 0 || stateRevision != expected || mismatches != 0 {
		t.Fatalf("chunks=%d state revision=%q expected=%q mismatches=%d", chunks, stateRevision, expected, mismatches)
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

// manifestSource serves a repository whose dependency manifests are not all
// accepted by the content policy, which is the normal case: a policy tuned for
// source code rarely lists .xml or .json.
type manifestSource struct{ fakeSource }

func (manifestSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	return []source.File{
		{Path: "README.md"},
		{Path: "go.mod"},
		{Path: "web/package.json"},
		{Path: "service/pom.xml"},
	}, nil
}

func (manifestSource) GetFile(_ context.Context, _ source.RepositoryRef, _ string, path string) ([]byte, error) {
	switch path {
	case "go.mod":
		return []byte("module svc\n\nrequire (\n\tgithub.com/gin-gonic/gin v1.10.0\n\tgolang.org/x/sync v0.10.0 // indirect\n)\n"), nil
	case "web/package.json":
		return []byte(`{"dependencies":{"lodash":"4.17.21"},"devDependencies":{"vitest":"^1.0.0"}}`), nil
	case "service/pom.xml":
		return []byte(`<project><dependencies><dependency><groupId>org.apache.logging.log4j</groupId><artifactId>log4j-core</artifactId><version>2.17.1</version></dependency></dependencies></project>`), nil
	default:
		return []byte("# Guide\nUse the service.\n"), nil
	}
}

// The inventory is only trustworthy if it covers manifests the content policy
// excluded, and if it is replaced rather than accumulated on every run.
func TestSyncBuildsDependencyInventoryIncludingExcludedManifests(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:manifest-index?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	// A policy that only accepts Markdown: go.mod, package.json and pom.xml are
	// all outside it, and must still be inventoried.
	policy := DefaultPolicy()
	policy.IncludeExtensions = []string{".md"}
	idx := New(s, policy)
	repo := source.Repository{ID: 11, ProjectKey: "core", Slug: "svc", Name: "svc", DefaultBranch: "main"}
	if err = idx.SyncRepository(ctx, manifestSource{}, "gitlab", repo, []source.Reference{{Name: "main", LatestCommit: "abc123"}}); err != nil {
		t.Fatal(err)
	}
	type row struct{ ecosystem, name, version, scope, path string }
	read := func() []row {
		t.Helper()
		rows, err := s.DB.Query(`SELECT ecosystem,name,version,scope,manifest_path FROM repository_packages WHERE repository_id='gitlab:11' AND ref_name='main' ORDER BY ecosystem,name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.ecosystem, &item.name, &item.version, &item.scope, &item.path); err != nil {
				t.Fatal(err)
			}
			out = append(out, item)
		}
		return out
	}
	packages := read()
	if len(packages) != 5 {
		t.Fatalf("packages=%#v", packages)
	}
	found := map[string]row{}
	for _, item := range packages {
		found[item.name] = item
	}
	if item := found["github.com/gin-gonic/gin"]; item.version != "v1.10.0" || item.scope != "direct" || item.path != "go.mod" {
		t.Fatalf("go dependency=%#v", item)
	}
	if item := found["golang.org/x/sync"]; item.scope != "transitive" {
		t.Fatalf("the indirect marker must survive indexing: %#v", item)
	}
	if item := found["org.apache.logging.log4j:log4j-core"]; item.version != "2.17.1" || item.path != "service/pom.xml" {
		t.Fatalf("maven dependency=%#v", item)
	}
	if item := found["vitest"]; item.scope != "dev" {
		t.Fatalf("npm scope=%#v", item)
	}

	// A second run must replace the inventory, not double it.
	if err = idx.SyncRepository(ctx, manifestSource{}, "gitlab", repo, []source.Reference{{Name: "main", LatestCommit: "def456"}}); err != nil {
		t.Fatal(err)
	}
	if again := read(); len(again) != 5 {
		t.Fatalf("re-index must replace the inventory, got %d rows", len(again))
	}
	var staging int
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_packages_staging`).Scan(&staging); err != nil || staging != 0 {
		t.Fatalf("staging must be cleaned up: %d err=%v", staging, err)
	}
}

// incrementalManifestSource reports a single changed file that is not a
// manifest, which is the common case: most commits touch source code.
type incrementalManifestSource struct {
	manifestSource
	changed string
}

func (s incrementalManifestSource) Changes(_ context.Context, _ source.RepositoryRef, _, _ string) ([]source.Change, error) {
	return []source.Change{{Path: s.changed, Type: "modify"}}, nil
}

// An incremental sync sees only the changed files. Replacing the whole ref's
// inventory from that set would delete every manifest the commit did not touch,
// leaving the estate looking dependency-free until someone ran a full re-index —
// and an advisory search would then answer "nobody uses it".
func TestIncrementalSyncKeepsUntouchedManifestsInTheInventory(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:manifest-incremental?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	idx := New(s, DefaultPolicy())
	repo := source.Repository{ID: 12, ProjectKey: "core", Slug: "svc", Name: "svc", DefaultBranch: "main"}
	if err = idx.SyncRepository(ctx, manifestSource{}, "gitlab", repo, []source.Reference{{Name: "main", LatestCommit: "abc123"}}); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var total int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM repository_packages WHERE repository_id='gitlab:12' AND ref_name='main'`).Scan(&total); err != nil {
			t.Fatal(err)
		}
		return total
	}
	if before := count(); before != 5 {
		t.Fatalf("full sync inventory=%d", before)
	}

	// A commit that touches only a source file must leave the inventory intact.
	if err = idx.SyncRepository(ctx, incrementalManifestSource{changed: "README.md"}, "gitlab", repo,
		[]source.Reference{{Name: "main", LatestCommit: "def456"}}); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != 5 {
		t.Fatalf("an incremental sync erased the inventory: %d rows remain", after)
	}

	// A commit that touches one manifest replaces only that manifest's rows.
	if err = idx.SyncRepository(ctx, incrementalManifestSource{changed: "go.mod"}, "gitlab", repo,
		[]source.Reference{{Name: "main", LatestCommit: "ghi789"}}); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != 5 {
		t.Fatalf("re-reading one manifest changed the inventory size: %d", after)
	}
	var goRows int
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM repository_packages WHERE repository_id='gitlab:12' AND manifest_path='go.mod'`).Scan(&goRows); err != nil || goRows != 2 {
		t.Fatalf("go.mod rows=%d err=%v", goRows, err)
	}
}

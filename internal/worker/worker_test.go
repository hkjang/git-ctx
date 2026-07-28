package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/embedding"

	"git-ctx/internal/indexer"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

type fakeSource struct{}

func (fakeSource) ListProjects(context.Context) ([]source.Project, error) { return nil, nil }
func (fakeSource) ListRepositories(context.Context, string) ([]source.Repository, error) {
	return nil, nil
}

func TestRunOnceMovesRepeatedFailureToFailed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('bitbucket:9','KCB','broken','Broken','bitbucket','9','/kcb/broken','main')`)
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES('fail-job','bitbucket:9','main','webhook','pending')`)
	w := New(db, indexer.New(db, indexer.DefaultPolicy()), func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("source unavailable")
	})
	for attempt := 1; attempt <= 5; attempt++ {
		ok, runErr := w.RunOnce(ctx)
		if !ok || runErr == nil {
			t.Fatalf("attempt %d ok=%v err=%v", attempt, ok, runErr)
		}
		_, _ = db.DB.Exec(`UPDATE index_jobs SET next_run_at=datetime('now','-1 second') WHERE id='fail-job'`)
	}
	var status, message string
	var attempts int
	if err = db.DB.QueryRow(`SELECT status,attempts,error_message FROM index_jobs WHERE id='fail-job'`).Scan(&status, &attempts, &message); err != nil {
		t.Fatal(err)
	}
	// The recorded message must name the failing step and where to fix it.
	if status != "failed" || attempts != 5 || !strings.Contains(message, "source unavailable") || !strings.Contains(message, "bitbucket setting") {
		t.Fatalf("status=%s attempts=%d message=%s", status, attempts, message)
	}
}
func (fakeSource) ListBranches(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return []source.Reference{{Name: "main", LatestCommit: "abc"}}, nil
}
func (fakeSource) ListTags(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, nil
}
func (fakeSource) GetCommit(context.Context, source.RepositoryRef, string) (source.Commit, error) {
	return source.Commit{}, nil
}
func (fakeSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	return []source.File{{Path: "README.md"}}, nil
}
func (fakeSource) GetFile(context.Context, source.RepositoryRef, string, string) ([]byte, error) {
	return []byte("# Guide\nworker content"), nil
}
func (fakeSource) GetPermissions(context.Context, source.RepositoryRef) ([]source.Permission, error) {
	return []source.Permission{{Principal: "alice", Permission: "read"}}, nil
}
func (fakeSource) RegisterWebhook(context.Context, source.RepositoryRef, string, string) error {
	return nil
}

func TestRunOnceClaimsAndCompletesPendingJob(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('bitbucket:7','KCB','demo','Demo','bitbucket','7','/kcb/demo','main')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES('j1','bitbucket:7','main','webhook','pending')`)
	if err != nil {
		t.Fatal(err)
	}
	w := New(db, indexer.New(db, indexer.DefaultPolicy()), func(context.Context, string) (source.RepositorySource, error) { return fakeSource{}, nil })
	projected := false
	w.SetProjection(func(_ context.Context, repositoryID, ref string) error {
		projected = repositoryID == "bitbucket:7" && ref == "main"
		return nil
	})
	ok, err := w.RunOnce(ctx)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var status string
	var attempts int
	if err = db.DB.QueryRow(`SELECT status,attempts FROM index_jobs WHERE id='j1'`).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || attempts != 1 {
		t.Fatalf("status=%s attempts=%d", status, attempts)
	}
	var chunks int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks WHERE repository_id='bitbucket:7'`).Scan(&chunks)
	if chunks != 1 {
		t.Fatalf("chunks=%d", chunks)
	}
	if !projected {
		t.Fatal("completed job did not update the search projection")
	}
	// The operations screen reads this row: a worker job that indexed files must
	// not report zero, otherwise a healthy index looks like it never ran.
	var files int
	if err = db.DB.QueryRow(`SELECT files_processed FROM index_jobs WHERE id='j1'`).Scan(&files); err != nil || files != 1 {
		t.Fatalf("files_processed=%d err=%v", files, err)
	}
}

// unreadableSource serves one file and fails another, like a repository with an
// LFS pointer or a file removed between listing and download.
type unreadableSource struct{ fakeSource }

func (unreadableSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	return []source.File{{Path: "README.md"}, {Path: "gone.md"}}, nil
}
func (unreadableSource) GetFile(_ context.Context, _ source.RepositoryRef, _ string, path string) ([]byte, error) {
	if path == "gone.md" {
		return nil, errors.New("404 file not found")
	}
	return []byte("# Title\ncontent"), nil
}

func TestWorkerJobReportsSkippedFilesInsteadOfFailing(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:worker-skips?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('gitlab:3','core','docs','Docs','gitlab','3','/gitlab~core/docs','main')`)
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES('j2','gitlab:3','main','initial','pending')`)
	w := New(db, indexer.New(db, indexer.DefaultPolicy()), func(context.Context, string) (source.RepositorySource, error) { return unreadableSource{}, nil })
	if ok, runErr := w.RunOnce(ctx); !ok || runErr != nil {
		t.Fatalf("ok=%v err=%v", ok, runErr)
	}
	var status, message string
	var files int
	if err = db.DB.QueryRow(`SELECT status,files_processed,error_message FROM index_jobs WHERE id='j2'`).Scan(&status, &files, &message); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || files != 1 {
		t.Fatalf("status=%s files=%d", status, files)
	}
	if !strings.Contains(message, "gone.md") {
		t.Fatalf("the skipped file must stay visible on the job: %q", message)
	}
}

// A job left in `running` by a restart or a hung remote call must return to the
// queue by itself. Without this the repository stays unindexed forever and no
// screen ever shows a failure.
func TestStaleRunningJobsAreRequeued(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:stale-jobs?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('bitbucket:5','KCB','demo','Demo','bitbucket','5','/kcb/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,attempts,started_at) VALUES('stuck','bitbucket:5','main','initial','running',1,?)`, time.Now().UTC().Add(-time.Hour))
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,attempts,started_at) VALUES('fresh','bitbucket:5','main','initial','running',1,?)`, time.Now().UTC())
	w := New(db, indexer.New(db, indexer.DefaultPolicy()), func(context.Context, string) (source.RepositorySource, error) { return fakeSource{}, nil })
	recovered, err := w.RecoverStaleJobs(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	var status, message string
	if err = db.DB.QueryRow(`SELECT status,error_message FROM index_jobs WHERE id='stuck'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || !strings.Contains(message, "Requeued automatically") {
		t.Fatalf("stale job status=%s message=%q", status, message)
	}
	var fresh string
	_ = db.DB.QueryRow(`SELECT status FROM index_jobs WHERE id='fresh'`).Scan(&fresh)
	if fresh != "running" {
		t.Fatalf("a job inside its lease must not be requeued: %s", fresh)
	}
	// The recovered job runs on the next pass instead of staying stuck.
	if ok, runErr := w.RunOnce(ctx); !ok || runErr != nil {
		t.Fatalf("ok=%v err=%v", ok, runErr)
	}
}

// A misconfigured embedding endpoint must fail in seconds with a message that
// names the setting, not after the whole repository has been downloaded.
func TestEmbeddingProbeFailsFastWithActionableMessage(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:embedding-probe?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('bitbucket:6','KCB','demo','Demo','bitbucket','6','/kcb/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES('probe','bitbucket:6','main','initial','pending')`)
	downloads := 0
	counting := countingSource{downloads: &downloads}
	w := New(db, indexer.New(db, indexer.DefaultPolicy()), func(context.Context, string) (source.RepositorySource, error) { return counting, nil })
	w.SetEmbeddingFactory(func(context.Context) (embedding.Provider, error) { return brokenEmbedder{}, nil })
	if ok, runErr := w.RunOnce(ctx); !ok || runErr == nil {
		t.Fatalf("ok=%v err=%v", ok, runErr)
	}
	var message string
	_ = db.DB.QueryRow(`SELECT error_message FROM index_jobs WHERE id='probe'`).Scan(&message)
	if !strings.Contains(message, "embedding endpoint rejected a probe") || !strings.Contains(message, "model setting") {
		t.Fatalf("message=%q", message)
	}
	if downloads != 0 {
		t.Fatalf("the probe must run before any file download, downloads=%d", downloads)
	}
}

type brokenEmbedder struct{}

func (brokenEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embedding API 404: unknown model")
}

type countingSource struct {
	fakeSource
	downloads *int
}

func (c countingSource) GetFile(context.Context, source.RepositoryRef, string, string) ([]byte, error) {
	*c.downloads++
	return []byte("# Guide\ncontent"), nil
}

func TestProjectionFailureRetriesIndexJob(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:projection-failure?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('bitbucket:8','KCB','demo','Demo','bitbucket','8','/kcb/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES('j2','bitbucket:8','main','manual','pending')`)
	w := New(db, indexer.New(db, indexer.DefaultPolicy()), func(context.Context, string) (source.RepositorySource, error) { return fakeSource{}, nil })
	w.SetProjection(func(context.Context, string, string) error { return errors.New("opensearch unavailable") })
	if ok, runErr := w.RunOnce(ctx); !ok || runErr == nil || !strings.Contains(runErr.Error(), "search projection") {
		t.Fatalf("ok=%v err=%v", ok, runErr)
	}
	var status string
	if err = db.DB.QueryRow(`SELECT status FROM index_jobs WHERE id='j2'`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

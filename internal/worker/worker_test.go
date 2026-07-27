package worker

import (
	"context"
	"errors"
	"testing"

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
	if status != "failed" || attempts != 5 || message != "source unavailable" {
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
}

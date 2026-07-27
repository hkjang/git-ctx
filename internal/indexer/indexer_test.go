package indexer

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

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

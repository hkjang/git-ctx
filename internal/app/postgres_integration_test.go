package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git-ctx/internal/config"
)

func TestPostgresDSNOnlyBootstrapIntegration(t *testing.T) {
	dsn := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GIT_CTX_TEST_POSTGRES_DSN is not set")
	}
	t.Setenv("GIT_CTX_DB_DSN", dsn)
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupDirectory = filepath.Join(t.TempDir(), "backups")
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer a.disableBootstrapAdmin()
	if cfg.DatabaseDriver != "postgres" || a.bootstrapAdminToken() == "" {
		t.Fatalf("driver=%q bootstrap=%t", cfg.DatabaseDriver, a.bootstrapAdminToken() != "")
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%s", rec.Code, rec.Body.String())
	}
	public := httptest.NewRecorder()
	a.Handler().ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil))
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), `"driver":"postgres"`) {
		t.Fatalf("public DB status=%d body=%s", public.Code, public.Body.String())
	}
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/database/status", nil)
	adminRequest.Header.Set("Authorization", "Bearer "+a.bootstrapAdminToken())
	admin := httptest.NewRecorder()
	a.Handler().ServeHTTP(admin, adminRequest)
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), `"database"`) || !strings.Contains(admin.Body.String(), `"latest":"028_source_capabilities.sql"`) {
		t.Fatalf("admin DB status=%d body=%s", admin.Code, admin.Body.String())
	}
	if _, err = a.store.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,library_id,default_branch) VALUES('fts-repo','OPS','fts','FTS','/ops/fts','main')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('fts-repo','alice','read')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('fts-chunk','fts-repo','main','c1','docs/runbook.md',1,5,'GPU Runbook','document','restart exporter for gpu metrics','h1')`); err != nil {
		t.Fatal(err)
	}
	candidates, err := a.openSearchCandidates(context.Background(), "fts-repo", "main", []string{"alice"}, "gpu metrics", 10)
	if err != nil || len(candidates) != 1 || candidates[0].ID != "fts-chunk" {
		t.Fatalf("PostgreSQL FTS candidates=%#v err=%v", candidates, err)
	}
}

func TestSQLiteRecoveryToPostgresMigrationIntegration(t *testing.T) {
	targetDSN := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if targetDSN == "" {
		t.Skip("GIT_CTX_TEST_POSTGRES_DSN is not set")
	}
	directory := filepath.Join(t.TempDir(), "backups")
	cfg := config.Config{
		DatabaseDriver: "postgres", DatabaseDSN: "postgres://gitctx:invalid@127.0.0.1:1/gitctx?sslmode=disable&connect_timeout=1",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: directory,
	}
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !a.recoveryMode {
		t.Fatal("expected recovery mode")
	}
	if _, err = a.store.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,library_id,default_branch) VALUES('migration-repo','OPS','migration','Migration','/ops/migration','main')`); err != nil {
		t.Fatal(err)
	}
	call := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer bootstrap")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	testBody := `{"dsn":` + strconv.Quote(targetDSN) + `}`
	if rec := call("/api/v1/admin/database/test", testBody); rec.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", rec.Code, rec.Body.String())
	}
	migrateBody := `{"dsn":` + strconv.Quote(targetDSN) + `,"confirm":"MIGRATE TO POSTGRES","reason":"integration test"}`
	if rec := call("/api/v1/admin/database/migrate", migrateBody); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"restartRequired":true`) {
		t.Fatalf("migrate status=%d body=%s", rec.Code, rec.Body.String())
	}
	a.Close()
	b, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.recoveryMode || b.store.Driver() != "postgres" {
		t.Fatalf("configured target was not activated: recovery=%v driver=%s", b.recoveryMode, b.store.Driver())
	}
	var count int
	if err = b.store.DB.QueryRow(`SELECT COUNT(*) FROM repositories WHERE id='migration-repo'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migrated repository count=%d err=%v", count, err)
	}
}

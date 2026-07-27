package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
}

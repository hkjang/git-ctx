package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git-ctx/internal/config"
	"git-ctx/internal/store"
	"git-ctx/internal/testsupport"
)

// Moving from SQLite to PostgreSQL is an operation this platform offers, and
// it produced a database the platform could not start on.
//
// The logical copy carried system_settings and api_keys but not the keys those
// were sealed and peppered with, because there was nothing to carry: the keys
// were derived from the connection string and existed nowhere. The target
// derived its own, different keys from its own DSN, so the settings it had just
// received could not be decrypted and every copied API key was hashed against
// the wrong pepper. The endpoint reported success and asked for a restart, and
// the restart failed.

func TestMigratingToPostgresKeepsSettingsAndKeysIntegration(t *testing.T) {
	base := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if reason := testsupport.SkipReason("GIT_CTX_TEST_POSTGRES_DSN", base); reason != "" {
		t.Skip(reason)
	}
	ctx := context.Background()
	targetDSN, drop, err := testsupport.NewPostgresDatabase(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)

	source := newFakeGitLab(map[string]string{"README.md": "# api\n"})
	defer source.Close()
	directory := t.TempDir()
	before := openWithDSN(t, directory, "file:"+filepath.Join(directory, "before.db")+"?_foreign_keys=on&_busy_timeout=5000")
	if saved := adminCall(t, before, http.MethodPut, "/api/v1/admin/settings/gitlab",
		`{"baseUrl":"`+source.URL+`","token":"a-real-token","webhookSecret":"s3cret"}`); saved.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if _, err := before.store.DB.Exec(before.store.Rebind(
		`INSERT INTO users(id,subject,username,email,status) VALUES('dev','dev','dev','','active')`)); err != nil {
		t.Fatal(err)
	}
	_, secret, err := before.keys.Create(ctx, "dev", "agent", []string{"search-code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated := adminCall(t, before, http.MethodPost, "/api/v1/admin/database/migrate",
		fmt.Sprintf(`{"dsn":%q,"confirm":"MIGRATE TO POSTGRES"}`, targetDSN))
	if migrated.Code != http.StatusOK {
		t.Fatalf("migrate status=%d body=%s", migrated.Code, migrated.Body.String())
	}
	before.Close()

	// The keys have to be on the other side, or nothing else here can work.
	target, err := store.Open(ctx, "postgres", targetDSN)
	if err != nil {
		t.Fatal(err)
	}
	var keys int
	if err := target.DB.QueryRow(`SELECT COUNT(*) FROM platform_keys`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	_ = target.DB.Close()
	if keys != 1 {
		t.Fatalf("the migration copied everything the keys protect and not the keys: platform_keys has %d rows", keys)
	}

	// The restart the endpoint asks for.
	t.Setenv("GIT_CTX_DB_DSN", targetDSN)
	t.Setenv("GIT_CTX_RECOVERY_KEY", strings.Repeat("r", 48))
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupDirectory = filepath.Join(directory, "backups")
	after, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("the platform could not start on the database its own migration produced: %v", err)
	}
	defer after.Close()

	read := adminCall(t, after, http.MethodGet, "/api/v1/admin/settings/gitlab", "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), source.URL) {
		t.Fatalf("the migrated settings could not be read: %d %s", read.Code, read.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"api"}}}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	after.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "invalid_api_key") {
		t.Fatalf("an API key stopped working after the migration: %d %s", recorder.Code, recorder.Body.String())
	}
}

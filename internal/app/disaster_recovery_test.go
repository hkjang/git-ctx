package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"git-ctx/internal/config"
)

// Restoring onto a replacement machine is the case a backup exists for, and it
// could not be done.
//
// The backup file sat in the directory and the new installation listed nothing,
// because the listing read backup_records in its own empty database. Restoring
// by id failed with "sql: no rows in result set" — a database error handed to
// an operator in the middle of a recovery. And even with the file found, it
// could not have been opened: archives were sealed with the installation's own
// master key, and that key was derived from the connection string of the
// database that had just been lost.

func TestABackupCanBeRestoredOntoAReplacement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backups := filepath.Join(root, "shared-backups")
	source := newFakeGitLab(map[string]string{"README.md": "# api\n"})
	defer source.Close()

	open := func(dsn string) *App {
		t.Setenv("GIT_CTX_DB_DSN", dsn)
		t.Setenv("GIT_CTX_RECOVERY_KEY", strings.Repeat("r", 48))
		cfg, err := config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		cfg.BackupDirectory = backups
		a, err := New(ctx, cfg)
		if err != nil {
			t.Fatalf("open %s: %v", dsn, err)
		}
		return a
	}

	primary := open("file:" + filepath.Join(root, "primary.db") + "?_foreign_keys=on&_busy_timeout=5000")
	if saved := adminCall(t, primary, http.MethodPut, "/api/v1/admin/settings/gitlab",
		`{"baseUrl":"`+source.URL+`","token":"a-real-token","webhookSecret":"s3cret"}`); saved.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if _, err := primary.store.DB.Exec(primary.store.Rebind(
		`INSERT INTO users(id,subject,username,email,status) VALUES('dev','dev','dev','','active')`)); err != nil {
		t.Fatal(err)
	}
	_, secret, err := primary.keys.Create(ctx, "dev", "agent", []string{"search-code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	created := adminCall(t, primary, http.MethodPost, "/api/v1/admin/backups", "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create backup status=%d body=%s", created.Code, created.Body.String())
	}
	var record struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	primary.Close()

	// A replacement machine: empty database, the backup directory restored from
	// wherever it is kept, the same recovery key.
	replacement := open("file:" + filepath.Join(root, "replacement.db") + "?_foreign_keys=on&_busy_timeout=5000")
	defer replacement.Close()

	listed := adminCall(t, replacement, http.MethodGet, "/api/v1/admin/backups", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), record.ID) {
		t.Fatalf("the replacement cannot see the backup sitting in its directory: %d %s", listed.Code, listed.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/"+record.ID+"/restore", nil)
	request.Header.Set("Authorization", "Bearer "+replacement.bootstrapAdminToken())
	request.Header.Set("X-Restore-Confirmation", "RESTORE "+record.ID)
	recorder := httptest.NewRecorder()
	replacement.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("the restore failed on the replacement: %d %s", recorder.Code, recorder.Body.String())
	}

	// The restore is only a restore if what came back works.
	read := adminCall(t, replacement, http.MethodGet, "/api/v1/admin/settings/gitlab", "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), source.URL) {
		t.Fatalf("the restored settings could not be read: %d %s", read.Code, read.Body.String())
	}
	mcp := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"api"}}}`))
	mcp.Header.Set("Authorization", "Bearer "+secret)
	mcp.Header.Set("Content-Type", "application/json")
	mcpRecorder := httptest.NewRecorder()
	replacement.Handler().ServeHTTP(mcpRecorder, mcp)
	if mcpRecorder.Code != http.StatusOK || strings.Contains(mcpRecorder.Body.String(), "invalid_api_key") {
		t.Fatalf("an API key did not survive the restore: %d %s", mcpRecorder.Code, mcpRecorder.Body.String())
	}
}

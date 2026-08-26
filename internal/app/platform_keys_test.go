package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"git-ctx/internal/config"
)

// An on-premises installation changes its database connection string for
// reasons that have nothing to do with security: the data directory moves, a
// relative path is written absolute, two query parameters swap places, a
// PostgreSQL host is renamed.
//
// The encryption key and the API-key pepper were derived from that string, so
// any of those changes produced different keys. The platform then could not
// decrypt its own settings and refused to start, reporting "cipher: message
// authentication failed" — the name of the primitive, not the cause — and
// every API key stopped authenticating, silently, because its pepper had
// changed too.

func openWithDSN(t *testing.T, directory, dsn string) *App {
	t.Helper()
	t.Setenv("GIT_CTX_DB_DSN", dsn)
	t.Setenv("GIT_CTX_RECOVERY_KEY", strings.Repeat("r", 48))
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupDirectory = filepath.Join(directory, "backups")
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	return a
}

func adminCall(t *testing.T, a *App, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+a.bootstrapAdminToken())
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestTheConnectionStringMayChange(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "keys.db")
	// The same file, the same parameters, written in a different order.
	dsnAsCreated := "file:" + file + "?_foreign_keys=on&_busy_timeout=5000"
	dsnReordered := "file:" + file + "?_busy_timeout=5000&_foreign_keys=on"

	source := newFakeGitLab(map[string]string{"README.md": "# api\n"})
	defer source.Close()

	first := openWithDSN(t, directory, dsnAsCreated)
	saved := adminCall(t, first, http.MethodPut, "/api/v1/admin/settings/gitlab",
		`{"baseUrl":"`+source.URL+`","token":"a-real-token","webhookSecret":"s3cret"}`)
	if saved.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	// An API key, because its pepper was derived from the same string.
	if _, err := first.store.DB.Exec(first.store.Rebind(
		`INSERT INTO users(id,subject,username,email,status) VALUES('dev','dev','dev','','active')`)); err != nil {
		t.Fatal(err)
	}
	_, secret, err := first.keys.Create(context.Background(), "dev", "agent", []string{"search-code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := openWithDSN(t, directory, dsnReordered)
	defer second.Close()

	read := adminCall(t, second, http.MethodGet, "/api/v1/admin/settings/gitlab", "")
	if read.Code != http.StatusOK {
		t.Fatalf("the settings could not be read after the connection string changed: %d %s", read.Code, read.Body.String())
	}
	if !strings.Contains(read.Body.String(), source.URL) {
		t.Fatalf("the settings came back without their content: %s", read.Body.String())
	}
	// The key has to keep working: an agent's access must not depend on how the
	// connection string is spelled.
	request := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"settlement"}}}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	second.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "invalid_api_key") {
		t.Fatalf("an API key stopped authenticating after the connection string changed: %d %s",
			recorder.Code, recorder.Body.String())
	}
}

// The keys are wrapped by the recovery key, so losing both that and the
// original connection string is the one case that cannot be recovered — and it
// has to say so in the operator's terms rather than naming a cipher.
func TestAnUnopenableInstallationSaysWhatToDo(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "lost.db")
	dsn := "file:" + file + "?_foreign_keys=on"

	source := newFakeGitLab(map[string]string{"README.md": "# api\n"})
	defer source.Close()

	first := openWithDSN(t, directory, dsn)
	if saved := adminCall(t, first, http.MethodPut, "/api/v1/admin/settings/gitlab",
		`{"baseUrl":"`+source.URL+`","token":"t","webhookSecret":"s"}`); saved.Code != http.StatusOK {
		t.Fatalf("settings status=%d", saved.Code)
	}
	first.Close()

	// A different recovery key and a different connection string: nothing this
	// process holds can open what is stored.
	t.Setenv("GIT_CTX_DB_DSN", "file:"+file+"?_busy_timeout=1000")
	t.Setenv("GIT_CTX_RECOVERY_KEY", strings.Repeat("z", 48))
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupDirectory = filepath.Join(directory, "backups")
	if _, err := New(context.Background(), cfg); err == nil {
		t.Fatal("an installation whose keys are unreachable started anyway")
	} else {
		message := err.Error()
		if strings.Contains(message, "message authentication failed") {
			t.Errorf("the failure names the cipher rather than the cause: %v", err)
		}
		for _, expected := range []string{"GIT_CTX_RECOVERY_KEY", "recovery key"} {
			if !strings.Contains(message, expected) {
				t.Errorf("the failure does not mention %q: %v", expected, err)
			}
		}
	}
}

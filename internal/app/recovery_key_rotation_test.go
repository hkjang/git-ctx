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

// Rotating the recovery key is a documented operational procedure, and since
// the keys began living in the database it is also the one operation that can
// take the installation with it: the stored key material is wrapped by the key
// being rotated away, and so is every backup written before the rotation.
//
// While the connection string was unchanged the rotation happened to work — the
// old DSN-derived pair was still a valid fallback. Once the connection string
// had also changed, which is exactly what storing the keys made possible, the
// rotation left an installation that could not start.

func openWithKeys(t *testing.T, directory, dsn, recoveryKey, previousKey string) (*App, error) {
	t.Helper()
	t.Setenv("GIT_CTX_DB_DSN", dsn)
	t.Setenv("GIT_CTX_RECOVERY_KEY", recoveryKey)
	t.Setenv("GIT_CTX_PREVIOUS_RECOVERY_KEY", previousKey)
	cfg, err := config.FromEnv()
	if err != nil {
		return nil, err
	}
	cfg.BackupDirectory = filepath.Join(directory, "backups")
	return New(context.Background(), cfg)
}

func TestTheRecoveryKeyCanBeRotated(t *testing.T) {
	root := t.TempDir()
	source := newFakeGitLab(map[string]string{"README.md": "# api\n"})
	defer source.Close()
	asCreated := "file:" + filepath.Join(root, "a.db") + "?_foreign_keys=on&_busy_timeout=5000"
	moved := "file:" + filepath.Join(root, "a.db") + "?_busy_timeout=5000&_foreign_keys=on"
	first, second, third := strings.Repeat("a", 48), strings.Repeat("b", 48), strings.Repeat("c", 48)

	original, err := openWithKeys(t, root, asCreated, first, "")
	if err != nil {
		t.Fatal(err)
	}
	if saved := adminCall(t, original, http.MethodPut, "/api/v1/admin/settings/gitlab",
		`{"baseUrl":"`+source.URL+`","token":"a-real-token","webhookSecret":"s3cret"}`); saved.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	created := adminCall(t, original, http.MethodPost, "/api/v1/admin/backups", "")
	if created.Code != http.StatusCreated {
		t.Fatalf("backup status=%d body=%s", created.Code, created.Body.String())
	}
	backupID := strings.SplitN(strings.TrimPrefix(created.Body.String(), `{"id":"`), `"`, 2)[0]
	original.Close()

	// The connection string changes, which storing the keys is what allows.
	rotated, err := openWithKeys(t, root, moved, second, first)
	if err != nil {
		t.Fatalf("the rotation left an installation that cannot start: %v", err)
	}
	read := adminCall(t, rotated, http.MethodGet, "/api/v1/admin/settings/gitlab", "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), source.URL) {
		t.Fatalf("the settings could not be read after the rotation: %d %s", read.Code, read.Body.String())
	}
	// A backup written before the rotation still opens, because the previous key
	// was given. Losing those quietly is the failure this guards.
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/"+backupID+"/restore", nil)
	request.Header.Set("Authorization", "Bearer "+rotated.bootstrapAdminToken())
	request.Header.Set("X-Restore-Confirmation", "RESTORE "+backupID)
	recorder := httptest.NewRecorder()
	rotated.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("a backup written before the rotation could not be restored: %d %s", recorder.Code, recorder.Body.String())
	}
	rotated.Close()

	// The rotation is complete: the new key alone starts the installation.
	alone, err := openWithKeys(t, root, moved, second, "")
	if err != nil {
		t.Fatalf("the rotation did not rewrite the stored keys under the new key: %v", err)
	}
	alone.Close()

	// And a second rotation still needs its own predecessor.
	if _, err = openWithKeys(t, root, moved, third, ""); err == nil {
		t.Fatal("an installation started with a key that opens nothing")
	} else if !strings.Contains(err.Error(), "GIT_CTX_PREVIOUS_RECOVERY_KEY") {
		t.Fatalf("the failure does not name the way through: %v", err)
	}
}

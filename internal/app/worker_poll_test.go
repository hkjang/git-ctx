package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
)

// TestTheConfiguredWorkerPollIntervalReachesTheWorker pins the wiring that the
// test fixtures of this package depend on: the interval the installation is
// configured with has to be the interval the background worker actually waits
// between looking for queued jobs. It is asserted on the pickup latency of a
// real registration through the real admin endpoint, not on a worker built by
// hand — nothing else proves that New passes the value along.
//
// The deadline is far below the worker's own default of two seconds, so this
// test fails whenever the value stops being threaded through.
func TestTheConfiguredWorkerPollIntervalReachesTheWorker(t *testing.T) {
	source := newFakeGitLab(agreementFixture())
	defer source.Close()
	model := newFakeModelServer()
	defer model.Close()

	directory := t.TempDir()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite",
		DatabaseDSN:    "file:" + filepath.Join(directory, "poll.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper:      strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
		WorkerPollInterval: testWorkerPoll,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/gitlab",
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
		t.Fatalf("gitlab settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/model",
		fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":"%s/v1","model":"fake-embed","apiKey":"none","timeoutSeconds":10}`,
			model.URL)); saved.Code != http.StatusOK {
		t.Fatalf("model settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}

	// One second is five times the shortest poll waitFor can observe and forty
	// times the configured interval, but half the worker's unconfigured default.
	waitFor(t, time.Second, "the worker to claim the queued job", func() bool {
		var claimed int
		_ = a.store.DB.QueryRow(a.store.Rebind(
			`SELECT COUNT(*) FROM index_jobs WHERE started_at IS NOT NULL`)).Scan(&claimed)
		return claimed > 0
	})
	// The short interval has to leave a working installation behind, not just a
	// job that was picked up quickly.
	waitFor(t, 90*time.Second, "the repository to finish indexing", func() bool {
		var completed int
		_ = a.store.DB.QueryRow(a.store.Rebind(
			`SELECT COUNT(*) FROM index_jobs WHERE status='completed' AND files_processed>0`)).Scan(&completed)
		return completed > 0
	})
}

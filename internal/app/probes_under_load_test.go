package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
)

// Every request passed through a maintenance-mode lookup before its path was
// checked, and that lookup had no deadline. SQLite serves the whole process
// through one connection, so while a long index write held it, /healthz never
// answered: Kubernetes restarted the container, indexing began again, and the
// probe hung again — a crash loop caused by being busy. /metrics went silent at
// the same time, so the graphs that would have explained it were blank for
// exactly as long as the stall lasted.
//
// The probes have to answer while the database is busy. That is the whole point
// of a probe.
func TestTheProbesAnswerWhileTheDatabaseIsBusy(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:probes-under-load?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// Hold the single connection the way a long index write does.
	tx, err := a.store.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','s','a','')`); err != nil {
		t.Fatal(err)
	}

	answer := func(path string, within time.Duration) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			a.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			close(done)
		}()
		select {
		case <-done:
			return recorder
		case <-time.After(within):
			t.Fatalf("%s did not answer within %v while the database was busy", path, within)
			return nil
		}
	}

	// Liveness must not depend on the database at all. A restart cannot fix a
	// busy database; it can only interrupt the work that is making it busy.
	if live := answer("/healthz", 2*time.Second); live.Code != http.StatusOK {
		t.Errorf("the liveness probe failed while the database was merely busy: %d %s", live.Code, live.Body.String())
	}

	// Readiness may say no — the replica cannot answer promptly — but it has to
	// answer, and it has to say which of the two it is. "unavailable" sends an
	// operator to look at a database that is working.
	ready := answer("/readyz", 6*time.Second)
	if ready.Code != http.StatusServiceUnavailable {
		t.Errorf("readiness=%d while the only connection was held", ready.Code)
	}
	if !strings.Contains(ready.Body.String(), `"busy"`) {
		t.Errorf("readiness does not distinguish a busy database from an unreachable one: %s", ready.Body.String())
	}

	// A partial scrape says more than a missing one, and it must not call a
	// working database down.
	scrape := answer("/metrics", 6*time.Second)
	if scrape.Code != http.StatusOK {
		t.Fatalf("the scrape failed: %d", scrape.Code)
	}
	body := scrape.Body.String()
	if !strings.Contains(body, "git_ctx_database_up 1") {
		t.Errorf("a busy database was reported as down:\n%s", body)
	}
	if !strings.Contains(body, "git_ctx_database_busy 1") {
		t.Errorf("the scrape does not report that no connection was free:\n%s", body)
	}
	if !strings.Contains(body, "git_ctx_go_goroutines") {
		t.Errorf("the process metrics were lost with the database ones:\n%s", body)
	}

	// And when the write finishes, everything goes back to green without help.
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if ready := answer("/readyz", 3*time.Second); ready.Code != http.StatusOK {
		t.Errorf("readiness did not recover: %d %s", ready.Code, ready.Body.String())
	}
}

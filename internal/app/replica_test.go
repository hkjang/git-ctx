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
	"time"

	"git-ctx/internal/config"
	"git-ctx/internal/testsupport"
)

// The deployment manifest asks for two replicas, and nothing had ever run two
// against one database.
//
// Both bring up a worker and both poll the same job queue. A claim that is not
// atomic indexes a repository twice — doubling every chunk, every symbol and
// every dependency row — or drops a job when two workers believe they own it
// and the second overwrites the first's generation. Neither shows up in a
// single-process test.
func TestTwoReplicasIndexEachJobOnceIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("two workers racing over a queue takes real time")
	}
	base := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if reason := testsupport.SkipReason("GIT_CTX_TEST_POSTGRES_DSN", base); reason != "" {
		t.Skip(reason)
	}
	ctx := context.Background()
	dsn, drop, err := testsupport.NewPostgresDatabase(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)

	source := newFakeGitLab(agreementFixture())
	defer source.Close()
	model := newFakeModelServer()
	defer model.Close()

	replica := func(name string) *App {
		t.Helper()
		directory := t.TempDir()
		a, err := New(ctx, config.Config{
			DatabaseDriver: "postgres", DatabaseDSN: dsn,
			KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
			PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
			WorkerIdentity: name,
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	first := replica("first")
	second := replica("second")
	_ = second // it exists to compete for the queue

	call := func(a *App, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if saved := call(first, http.MethodPut, "/api/v1/admin/settings/gitlab",
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
		t.Fatalf("gitlab settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(first, http.MethodPut, "/api/v1/admin/settings/model",
		fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":"%s/v1","model":"fake-embed","apiKey":"none","timeoutSeconds":10}`, model.URL)); saved.Code != http.StatusOK {
		t.Fatalf("model settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	// Several repositories, so the queue has depth and both workers have
	// something to race for. One repository can be finished before the other
	// replica ever polls.
	const repositories = 6
	for id := 1; id <= repositories; id++ {
		registered := call(first, http.MethodPost, "/api/v1/admin/repositories",
			fmt.Sprintf(`{"sourceType":"gitlab","repository":{"id":%d,"projectKey":"core","slug":"api%d","name":"api%d","description":"payment api","defaultBranch":"main"}}`, 4240+id, id, id))
		if registered.Code != http.StatusCreated {
			t.Fatalf("register %d status=%d body=%s", id, registered.Code, registered.Body.String())
		}
	}

	waitFor(t, 180*time.Second, "every repository to finish indexing", func() bool {
		var completed int
		_ = first.store.DB.QueryRow(first.store.Rebind(
			`SELECT COUNT(*) FROM index_jobs WHERE status='completed' AND files_processed>0`)).Scan(&completed)
		return completed >= repositories
	})
	// Let the second replica have every chance to pick the same work up again.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		var running int
		_ = first.store.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs WHERE status='running'`).Scan(&running)
		if running == 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	// One repository indexed once: the chunk count is what a single pass
	// produces, not two passes' worth, and every path appears exactly once.
	var duplicated int
	if err = first.store.DB.QueryRow(`SELECT COUNT(*) FROM (
		SELECT file_path,line_start FROM document_chunks GROUP BY repository_id,ref_name,file_path,line_start HAVING COUNT(*)>1
	) doubled`).Scan(&duplicated); err != nil {
		t.Fatal(err)
	}
	if duplicated > 0 {
		t.Fatalf("%d chunks were indexed more than once: two replicas both did the work", duplicated)
	}
	for _, table := range []string{"code_symbols", "code_dependencies", "repository_files"} {
		var rows, distinct int
		if err = first.store.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		switch table {
		case "repository_files":
			err = first.store.DB.QueryRow(`SELECT COUNT(*) FROM (SELECT DISTINCT repository_id,ref_name,path FROM repository_files) d`).Scan(&distinct)
		default:
			err = first.store.DB.QueryRow(`SELECT COUNT(*) FROM (SELECT DISTINCT id FROM ` + table + `) d`).Scan(&distinct)
		}
		if err != nil {
			t.Fatal(err)
		}
		if rows != distinct {
			t.Errorf("%s holds %d rows for %d distinct entries", table, rows, distinct)
		}
	}

	// A repeated attempt is reported, not failed on.
	//
	// This used to assert that no completed job had attempts>1, reading a second
	// attempt as "two workers both won the claim". It is not: a job whose first
	// pass failed is retried on purpose, and so is one whose lease expired, so
	// under a loaded machine the assertion failed for the queue working exactly
	// as designed. Nor can the recorded error tell the two apart — the indexer
	// overwrites error_message with the successful run's own warning, which is
	// usually empty.
	//
	// Two workers cannot both win a claim here: the claim updates a row that must
	// still be pending, and every later write to that job is conditioned on the
	// lease it started with. That mechanism is proven directly, and
	// deterministically, by TestStaleWorkerCannotOverwriteReclaimedJob. What this
	// test proves is the consequence — no chunk, symbol or file row was written
	// twice, which is what a job two replicas both ran would leave behind.
	repeated, err := first.store.DB.Query(`SELECT id,attempts FROM index_jobs WHERE status='completed' AND attempts>1`)
	if err != nil {
		t.Fatal(err)
	}
	defer repeated.Close()
	for repeated.Next() {
		var id string
		var attempts int
		if err = repeated.Scan(&id, &attempts); err != nil {
			t.Fatal(err)
		}
		t.Logf("job %s needed %d attempts; the duplicate-row checks above are what rule out a double claim", id, attempts)
	}

	// Every repository got indexed. The count of completed jobs is deliberately
	// not compared to the count of registrations: the scheduler enqueues its own
	// refresh for a repository whose poll interval comes round, and one of those
	// landing inside the window is normal rather than a repository indexed
	// twice — which is what the duplicate-row checks above actually rule out.
	var indexed int
	if err = first.store.DB.QueryRow(`SELECT COUNT(*) FROM (
		SELECT DISTINCT repository_id FROM index_jobs WHERE status='completed' AND files_processed>0
	) done`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != repositories {
		t.Errorf("%d of %d repositories have a completed job", indexed, repositories)
	}

	// The race has to have happened, or exactly-once proves nothing: if one
	// replica did all the work, the claim was never contended.
	rows, err := first.store.DB.Query(`SELECT claimed_by,COUNT(*) FROM index_jobs WHERE status='completed' GROUP BY claimed_by ORDER BY claimed_by`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	share := map[string]int{}
	for rows.Next() {
		var who string
		var count int
		if err = rows.Scan(&who, &count); err != nil {
			t.Fatal(err)
		}
		share[who] = count
	}
	t.Logf("jobs per replica: %v", share)
	if len(share) < 2 {
		t.Errorf("only %v claimed anything, so two replicas never contended for the queue and exactly-once was not tested", share)
	}
}

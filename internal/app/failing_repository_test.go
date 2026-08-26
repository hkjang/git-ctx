package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
)

// One repository that cannot be indexed must not stop the others.
//
// A failing job goes back on the queue with an exponential delay and a budget
// of attempts. Without the delay a single unreachable repository would be
// reclaimed the moment it was released and would occupy the worker in a loop,
// and every other repository would wait behind it — which is how one broken
// source takes an estate's indexing down with it.

// newFakeGitLabWithABrokenProject serves every project normally except one,
// whose file listing always fails.
func newFakeGitLabWithABrokenProject(files map[string]string, brokenID int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.EscapedPath(), "/")
		project := func(id int) map[string]any {
			return map[string]any{"id": id, "path_with_namespace": fmt.Sprintf("core/api%d", id),
				"default_branch": "main", "name": fmt.Sprintf("api%d", id), "description": "payment api",
				"visibility": "internal", "repository_access_level": "enabled"}
		}
		// The project id sits in the path as an escaped namespace or a number.
		broken := strings.Contains(path, fmt.Sprintf("api%d", brokenID)) ||
			strings.Contains(path, fmt.Sprintf("/%d/", brokenID)) ||
			strings.HasSuffix(path, fmt.Sprintf("/%d", brokenID))
		if broken && (strings.Contains(path, "/repository/tree") || strings.Contains(path, "/repository/branches")) {
			http.Error(w, `{"message":"500 Internal Server Error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch {
		case r.Method == http.MethodPost || r.Method == http.MethodPut:
			write(map[string]any{"id": 77})
		case strings.HasSuffix(path, "/search"):
			http.Error(w, `{"message":"code search is not enabled on this instance"}`, http.StatusNotImplemented)
		case strings.HasSuffix(path, "/repository/branches"):
			write([]map[string]any{{"name": "main", "commit": map[string]string{"id": "c0ffee"}, "default": true}})
		case strings.HasSuffix(path, "/repository/tags"), strings.HasSuffix(path, "/repository/commits"):
			write([]map[string]any{})
		case strings.HasSuffix(path, "/repository/tree"):
			entries := make([]map[string]string, 0, len(files))
			for name := range files {
				entries = append(entries, map[string]string{"path": name, "type": "blob"})
			}
			write(entries)
		case strings.Contains(path, "/repository/files/"):
			name := strings.TrimSuffix(strings.SplitN(path, "/repository/files/", 2)[1], "/raw")
			if decoded, err := url.PathUnescape(name); err == nil {
				name = decoded
			}
			content, ok := files[name]
			if !ok {
				http.Error(w, `{"message":"404 File Not Found"}`, http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, content)
		case strings.HasSuffix(path, "/members/all"):
			write([]map[string]any{{"id": 11, "state": "active", "access_level": 30}})
		case strings.HasSuffix(path, "/api/v4/groups"):
			write([]map[string]any{{"id": 1, "full_path": "core", "name": "core"}})
		case strings.HasSuffix(path, "/api/v4/projects"), strings.HasSuffix(path, "/projects"):
			write([]map[string]any{project(4241), project(4242), project(4243)})
		case strings.Contains(path, "/api/v4/projects/"):
			write(project(4242))
		default:
			write(map[string]any{})
		}
	}))
}

func TestOneUnindexableRepositoryDoesNotStarveTheOthers(t *testing.T) {
	if testing.Short() {
		t.Skip("the retry budget takes real time")
	}
	ctx := context.Background()
	const brokenID = 4242
	source := newFakeGitLabWithABrokenProject(agreementFixture(), brokenID)
	defer source.Close()
	model := newFakeModelServer()
	defer model.Close()
	directory := t.TempDir()
	a, err := New(ctx, config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(directory, "starve.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
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

	// The broken repository is registered first, so it is the head of the queue
	// and gets every chance to hold it.
	for _, id := range []int{brokenID, 4241, 4243} {
		registered := call(http.MethodPost, "/api/v1/admin/repositories",
			fmt.Sprintf(`{"sourceType":"gitlab","repository":{"id":%d,"projectKey":"core","slug":"api%d","name":"api%d","description":"payment api","defaultBranch":"main"}}`, id, id, id))
		if registered.Code != http.StatusCreated {
			t.Fatalf("register %d status=%d body=%s", id, registered.Code, registered.Body.String())
		}
	}

	waitFor(t, 90*time.Second, "the healthy repositories to finish indexing", func() bool {
		var completed int
		_ = a.store.DB.QueryRow(a.store.Rebind(
			`SELECT COUNT(*) FROM index_jobs j JOIN repositories r ON r.id=j.repository_id
			 WHERE j.status='completed' AND j.files_processed>0 AND r.source_external_id<>?`), fmt.Sprint(brokenID)).Scan(&completed)
		return completed >= 2
	})

	// The broken one is still being retried rather than having taken the worker
	// with it, and it is spacing its attempts out.
	var status, message string
	var attempts int
	var nextRun time.Time
	if err = a.store.DB.QueryRow(a.store.Rebind(
		`SELECT j.status,j.attempts,j.error_message,j.next_run_at FROM index_jobs j JOIN repositories r ON r.id=j.repository_id
		 WHERE r.source_external_id=? ORDER BY j.created_at DESC LIMIT 1`), fmt.Sprint(brokenID)).
		Scan(&status, &attempts, &message, &nextRun); err != nil {
		t.Fatal(err)
	}
	if message == "" {
		t.Error("the failing repository records no reason")
	}
	if status == "completed" {
		t.Fatalf("the repository that cannot be read reported success: %s", message)
	}
	// A source failing with a 5xx is classed as an outage and deliberately does
	// not spend the retry budget: the server is expected back. What must not
	// happen is that this goes unrecorded — an outage with no end is a
	// repository that has silently stopped updating.
	var outageSince sql.NullTime
	if err = a.store.DB.QueryRow(a.store.Rebind(
		`SELECT j.outage_since FROM index_jobs j JOIN repositories r ON r.id=j.repository_id
		 WHERE r.source_external_id=? ORDER BY j.created_at DESC LIMIT 1`), fmt.Sprint(brokenID)).Scan(&outageSince); err != nil {
		t.Fatal(err)
	}
	if !outageSince.Valid {
		t.Error("the job does not record when its source started failing, so an outage that never ends looks like an idle queue")
	}
	// A retry scheduled for the past would be reclaimed immediately, which is
	// the loop this backoff exists to prevent.
	if status == "pending" && !nextRun.After(time.Now().UTC().Add(-time.Second)) {
		t.Errorf("the failed job is queued for %s, which is now or earlier: it would be retried in a tight loop", nextRun)
	}
	t.Logf("broken repository: status=%s attempts=%d next=%s outageSince=%s reason=%s", status, attempts, nextRun.UTC().Format(time.RFC3339), outageSince.Time.UTC().Format(time.RFC3339), first(message))
}

// A query that matches nothing is not a broken platform.
//
// query-docs falls back to the source's own code search when the index has no
// hit. If the source could not be asked — a connector that is paused,
// unconfigured or down — that fallback failing was reported as a failed call.
// An agent whose wording simply did not match the index was told the tool had
// failed, so it retried or gave up rather than asking differently.
func TestAQueryThatMatchesNothingIsNotAnError(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	a, err := New(ctx, config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(directory, "nomatch.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// A repository whose content is indexed, on an installation whose source
	// connector is not configured.
	for _, statement := range []string{
		`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','Payment API','','gitlab','1','/gitlab~core/api','main',1)`,
		`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','group:eng','read')`,
		`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','gitlab:1','main','c0ffee','internal/settlement/handler.go',1,9,'settleInvoice','code','func settleInvoice() error { return nil }','h')`,
	} {
		if _, err = a.store.DB.Exec(a.store.Rebind(statement)); err != nil {
			t.Fatal(err)
		}
	}

	answer := toolAnswer(t, a, "query-docs", `{"libraryId":"/gitlab~core/api","query":"zzzznothingmatchesthis"}`)
	if strings.Contains(answer, "code search API failed") {
		t.Fatalf("a query with no match was reported as a source API failure:\n%s", answer)
	}
	if !strings.Contains(answer, "No indexed documentation matched") {
		t.Fatalf("the answer does not say the index had no match:\n%s", answer)
	}
	// The reason the fallback could not run still has to be there: an answer
	// that hides it looks like a complete search when it was not.
	if !strings.Contains(answer, "was not asked as a fallback") {
		t.Fatalf("the answer does not say the live search was not run:\n%s", answer)
	}
	// And a query that does match still answers from the index.
	found := toolAnswer(t, a, "query-docs", `{"libraryId":"/gitlab~core/api","query":"settleInvoice"}`)
	if !strings.Contains(found, "settleInvoice") {
		t.Fatalf("an indexed match was not returned:\n%s", found)
	}
}

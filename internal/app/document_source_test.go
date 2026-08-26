package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
)

// Confluence and Jira are the least-exercised path in this platform, and the
// only one whose content lives in the index alone. Three things an agent was
// told there were wrong.
//
// A citation carried a synthetic revision token: these connectors have no
// commit, so they put the newest page's timestamp, version and id in that
// column, or the literal "empty" for a space with nothing in it. Both went
// straight into the source URI.
//
// Every answer said no source connector was configured, on an installation
// running two of them.
//
// find-runbook looked for "runbook", "playbook" and "operations". A page titled
// 장애 대응 런북 — the clearest runbook in the corpus, in the language this
// platform answers in — matched none of them.

func documentSourceApp(t *testing.T, confluenceURL, jiraURL, modelURL, name string) *App {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	a, err := New(ctx, config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(directory, name+".db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	call := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	for _, item := range []struct{ category, url string }{{"confluence", confluenceURL}, {"jira", jiraURL}} {
		if saved := call(http.MethodPut, "/api/v1/admin/settings/"+item.category,
			fmt.Sprintf(`{"baseUrl":%q,"authType":"bearer","token":"t","allowedPrincipals":["*"]}`, item.url)); saved.Code != http.StatusOK {
			t.Fatalf("%s settings status=%d body=%s", item.category, saved.Code, saved.Body.String())
		}
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/model",
		fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":"%s/v1","model":"fake-embed","apiKey":"none","dimensions":16,"timeoutSeconds":10}`, modelURL)); saved.Code != http.StatusOK {
		t.Fatalf("model settings status=%d", saved.Code)
	}
	for _, item := range []struct{ sourceType, project, slug, name string }{
		{"confluence", "OPS", "OPS", "Operations space"},
		{"jira", "PAY", "PAY", "Payments project"},
	} {
		created := call(http.MethodPost, "/api/v1/admin/repositories",
			fmt.Sprintf(`{"sourceType":%q,"repository":{"id":1,"projectKey":%q,"slug":%q,"name":%q,"description":"","defaultBranch":"current"}}`,
				item.sourceType, item.project, item.slug, item.name))
		if created.Code != http.StatusCreated {
			t.Fatalf("%s register status=%d body=%s", item.sourceType, created.Code, created.Body.String())
		}
		var repository struct{ ID, LibraryID string }
		_ = json.Unmarshal(created.Body.Bytes(), &repository)
		waitFor(t, 60*time.Second, item.sourceType+" to index", func() bool {
			var chunks int
			_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=?`), repository.ID).Scan(&chunks)
			return chunks > 0
		})
	}
	return a
}

func TestDocumentSourceAnswersAreCorrectIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	confluence := newFakeConfluence()
	defer confluence.Close()
	jira := newFakeJira()
	defer jira.Close()
	model := newFakeModelServer()
	defer model.Close()
	a := documentSourceApp(t, confluence.URL, jira.URL, model.URL, "documents")

	// The stored revision really is the synthetic token, so the citation is the
	// only thing standing between it and the reader.
	var stored string
	if err := a.store.DB.QueryRow(a.store.Rebind(
		`SELECT commit_id FROM document_chunks WHERE file_path LIKE 'pages/%' LIMIT 1`)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "empty" && !strings.Contains(stored, ":") {
		t.Skipf("the connector no longer stores a synthetic revision (%q); this test has nothing to guard", stored)
	}

	answer := toolAnswer(t, a, "search-code", `{"query":"장애 대응"}`)
	if !strings.Contains(answer, "pages/101") {
		t.Fatalf("the Confluence page was not found:\n%s", answer)
	}
	if strings.Contains(answer, "@empty") || strings.Contains(answer, stored) {
		t.Errorf("the citation carries the connector's synthetic revision:\n%s", answer)
	}
	if !strings.Contains(answer, "confluence://OPS/OPS@current/") {
		t.Errorf("the citation does not name the ref these sources use:\n%s", answer)
	}
	if strings.Contains(answer, "no source connector is configured") {
		t.Errorf("an installation running two connectors was told it has none:\n%s", answer)
	}

	// A Jira issue is cited the same way.
	issues := toolAnswer(t, a, "search-code", `{"query":"정산 배치 실패"}`)
	if !strings.Contains(issues, "jira://PAY/PAY@current/") {
		t.Errorf("the Jira citation carries a timestamp instead of a revision:\n%s", issues)
	}
}

func TestARunbookInKoreanIsFoundIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	confluence := newFakeConfluence()
	defer confluence.Close()
	jira := newFakeJira()
	defer jira.Close()
	model := newFakeModelServer()
	defer model.Close()
	a := documentSourceApp(t, confluence.URL, jira.URL, model.URL, "runbooks")

	answer := toolAnswer(t, a, "find-runbook", `{"query":"장애"}`)
	if !strings.Contains(answer, "장애 대응 런북") {
		t.Fatalf("a page titled 장애 대응 런북 was not found by find-runbook:\n%s", answer)
	}
	// An English-named runbook still has to be found.
	if _, err := a.store.DB.Exec(a.store.Rebind(
		`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
		 SELECT 'en-runbook',repository_id,ref_name,commit_id,'docs/RUNBOOK.md',1,3,'Deployment runbook','document','Restart the consumer.','h'
		 FROM document_chunks WHERE file_path LIKE 'pages/%' LIMIT 1`)); err != nil {
		t.Fatal(err)
	}
	english := toolAnswer(t, a, "find-runbook", `{"query":"consumer"}`)
	if !strings.Contains(english, "Deployment runbook") {
		t.Fatalf("an English runbook stopped being found:\n%s", english)
	}
}

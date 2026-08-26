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

	"git-ctx/internal/config"
)

// What every tool says when it has nothing to say.
//
// "library is unavailable or access is denied" covered three situations at
// once: nothing is registered on this platform, this identity can read nothing,
// and the named thing is not there or not permitted. The first two are what a
// new installation and a broken identity mapping look like, and both were
// reported as a denial — sending whoever read it into the permission model when
// the answer was "register a repository" or "map this account".
//
// The third stays vague on purpose. Separating "not there" from "not yours"
// would tell a caller that a repository it may not read exists.

// lookupTools each resolve something by name and fail when they cannot.
var lookupTools = []struct{ tool, arguments string }{
	{"query-docs", `{"libraryId":"/gitlab~core/api","query":"settlement"}`},
	{"get-repository-map", `{"libraryId":"/gitlab~core/api"}`},
	{"get-repository-health", `{"libraryId":"/gitlab~core/api"}`},
	{"explain-search-result", `{"libraryId":"/gitlab~core/api","query":"settlement"}`},
	{"trace-dependencies", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
	{"get-symbol-context", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
	{"compare-refs", `{"libraryId":"/gitlab~core/api","baseRef":"main","headRef":"next"}`},
	{"get-change-impact", `{"libraryId":"/gitlab~core/api","baseRef":"main","headRef":"next"}`},
	{"assess-change-risk", `{"libraryId":"/gitlab~core/api","baseRef":"main","headRef":"next"}`},
	{"export-context", `{"libraryIds":["/gitlab~core/api"],"query":"settlement"}`},
	{"read-file", `{"path":"README.md"}`},
	{"list-directory", `{"path":"internal"}`},
	{"find-code-owner", `{"path":"config/app.yaml"}`},
	{"get-file-history", `{"path":"README.md"}`},
}

func newEmptyApp(t *testing.T, name string) *App {
	t.Helper()
	directory := t.TempDir()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(directory, name+".db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func toolAnswer(t *testing.T, a *App, tool, arguments string) string {
	t.Helper()
	return toolAnswerAs(t, a, "bootstrap", tool, arguments)
}

// toolAnswerAs returns whatever the tool said, error or not: what these tests
// are about is the wording of a failure.
func toolAnswerAs(t *testing.T, a *App, secret, tool, arguments string) string {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, arguments)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	var response struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if response.Error != nil {
		return response.Error.Message
	}
	if len(response.Result.Content) == 0 {
		t.Fatalf("%s returned no content: %s", tool, recorder.Body.String())
	}
	return response.Result.Content[0].Text
}

func TestAnEmptyPlatformSaysItIsEmpty(t *testing.T) {
	a := newEmptyApp(t, "empty")
	defer a.Close()
	for _, ask := range lookupTools {
		answer := toolAnswer(t, a, ask.tool, ask.arguments)
		if strings.Contains(answer, "access is denied") {
			t.Errorf("%s calls an empty platform a denial: %s", ask.tool, first(answer))
		}
		if !strings.Contains(answer, "no repository is registered") {
			t.Errorf("%s does not say the platform has no repositories: %s", ask.tool, first(answer))
		}
	}
}

// A repository that is registered and readable but has never been indexed is
// the third situation, and the one a new installation spends its first minutes
// in. It must not be reported as an empty platform, and it must not be reported
// as a denial.
func TestARegisteredRepositoryThatIsNotIndexedSaysSo(t *testing.T) {
	a := newEmptyApp(t, "unindexed")
	defer a.Close()
	if _, err := a.store.DB.Exec(a.store.Rebind(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/gitlab~core/api','main',1)`)); err != nil {
		t.Fatal(err)
	}
	answer := toolAnswer(t, a, "get-repository-map", `{"libraryId":"/gitlab~core/api"}`)
	if strings.Contains(answer, "no repository is registered") {
		t.Fatalf("a platform with a repository reported itself empty: %s", first(answer))
	}
	if strings.Contains(answer, "access is denied") {
		t.Fatalf("a readable repository was reported as a denial: %s", first(answer))
	}
	if !strings.Contains(answer, "reindex") {
		t.Fatalf("the answer does not say the ref has not been indexed: %s", first(answer))
	}
}

// The vague answer is kept for the one case that has to stay vague: a caller
// restricted to other repositories must not learn whether this one exists.
func TestARestrictedCallerIsNotToldWhatExists(t *testing.T) {
	a := newEmptyApp(t, "restricted")
	defer a.Close()
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := a.store.DB.Exec(a.store.Rebind(query), args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/gitlab~core/api','main',1)`)
	must(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:2','core','other','other','','gitlab','2','/gitlab~core/other','main',1)`)
	must(`INSERT INTO users(id,subject,username,email,status) VALUES('dev','dev','dev','','active')`)
	must(`INSERT INTO user_identities(user_id,bitbucket_user_slug,gitlab_user_id,mapping_source,bitbucket_groups) VALUES('dev','','dev','manual','group:other-team')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:2','group:other-team','read')`)
	_, secret, err := a.keys.Create(context.Background(), "dev", "agent", []string{"get-repository-map"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	answer := toolAnswerAs(t, a, secret, "get-repository-map", `{"libraryId":"/gitlab~core/api"}`)
	for _, forbidden := range []string{"no repository is registered", "can read none of the registered"} {
		if strings.Contains(answer, forbidden) {
			t.Fatalf("a caller who can read a repository was told the platform is empty: %s", first(answer))
		}
	}
	if !strings.Contains(answer, "unavailable or access is denied") {
		t.Fatalf("the answer tells a restricted caller whether the library exists: %s", first(answer))
	}
}

func first(text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if len(line) > 150 {
		return line[:150] + "…"
	}
	return line
}

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
	"git-ctx/internal/testsupport"
)

// The same repository, indexed into both databases, asked the same questions.
//
// An installation picks its database and the agent asking does not know which
// one it got. The chain tests prove each driver works; they cannot show that
// the two agree, because each assertion is checked against whichever driver
// happens to be running. A search for a word in a file path returned four
// files on SQLite and nothing on PostgreSQL for a whole release, and every
// test passed on both.
//
// This indexes one fixture repository into each database through the ordinary
// path — the same fake platform, the same worker — and then calls the read
// tools an agent uses, requiring the answers to match.

// agreementTools is what an agent asks, in the order it usually asks it.
var agreementTools = []struct{ tool, arguments string }{
	{"search-code", `{"query":"settlement"}`},
	{"search-code", `{"query":"settleInvoice"}`},
	{"search-code", `{"query":"SETTLEMENT"}`},
	{"search-code", `{"query":"결제"}`},
	{"search-code", `{"query":"reconcile settlement batch"}`},
	{"search-semantic", `{"query":"settlement retry"}`},
	{"export-context", `{"libraryIds":["/gitlab~core/api"],"query":"settlement"}`},
	{"get-file-history", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
	{"search-repositories", `{"query":"payment"}`},
	{"find-file", `{"pattern":"*.go"}`},
	{"find-file", `{"pattern":"settlement*"}`},
	{"list-directory", `{"libraryId":"/gitlab~core/api","path":"internal"}`},
	{"read-file", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
	{"find-symbol", `{"libraryId":"/gitlab~core/api","query":"settle"}`},
	{"trace-dependencies", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
	{"find-runbook", `{"query":"settlement"}`},
	{"find-code-owner", `{"libraryId":"/gitlab~core/api","path":"config/app.yaml"}`},
	{"find-dependency-usage", `{"name":"express"}`},
	{"get-repository-map", `{"libraryId":"/gitlab~core/api"}`},
	{"explain-search-result", `{"libraryId":"/gitlab~core/api","query":"settlement"}`},
	{"resolve-library-id", `{"libraryName":"api","query":"settlement"}`},
	{"query-docs", `{"libraryId":"/gitlab~core/api","query":"how is settlement retried"}`},
	{"build-context", `{"libraryId":"/gitlab~core/api","query":"settlement"}`},
	{"get-architecture-map", `{}`},
	{"find-dependents", `{"target":"reconcile"}`},
	{"get-repository-health", `{"libraryId":"/gitlab~core/api"}`},
	{"get-symbol-context", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
	{"find-tests", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
}

// Anything an answer carries that is true of the run rather than of the
// corpus. Two runs minutes apart differ on all of it, and so would two runs of
// the same driver.
var agreementNoise = []*regexp.Regexp{
	regexp.MustCompile(`\b[0-9a-f]{8,}\b`),
	regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`),
	regexp.MustCompile(`\b\d+(\.\d+)?\s*(초|분|시간|일|밀리초|ms|s|m|h|second|minute|hour|day)s?\s*(ago|전)?\b`),
	regexp.MustCompile(`(?i)\b(indexed|updated|generated|as of)\b[^\n]*`),
}

func normaliseAnswer(text string) string {
	for _, pattern := range agreementNoise {
		text = pattern.ReplaceAllString(text, "·")
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func agreementFixture() map[string]string {
	return map[string]string{
		"internal/settlement/handler.go":      "package settlement\n\n// settleInvoice reconciles one order.\nfunc settleInvoice(order Order) error {\n\treturn reconcile(order)\n}\n",
		"internal/settlement/retry.go":        "package settlement\n\nfunc RetrySettlement(order Order) error {\n\treturn settleInvoice(order)\n}\n",
		"internal/settlement/handler_test.go": "package settlement\n\nimport \"testing\"\n\nfunc TestSettleInvoice(t *testing.T) {\n\tif settleInvoice(Order{}) != nil {\n\t\tt.Fatal(\"failed\")\n\t}\n}\n",
		"internal/cache/warm.go":              "package cache\n\nfunc warmCache() error { return nil }\n",
		"docs/RUNBOOK.md":                     "# Settlement runbook\n\n결제 정산 배치가 멈추면 settlement-worker 를 다시 시작한다.\n",
		"README.md":                           "# Payment API\n\n결제 처리 서비스. settlement 배치를 운영한다.\n",
		"CODEOWNERS":                          "* @platform-team\n/config/ @ops-team\n",
		"config/app.yaml":                     "database:\n  host: db.internal\n",
		"package.json":                        "{\"name\":\"api\",\"dependencies\":{\"express\":\"^4.18.0\"}}\n",
		"package-lock.json":                   "{\"lockfileVersion\":3,\"packages\":{\"node_modules/express\":{\"version\":\"4.18.2\"}}}\n",
	}
}

// answerEachTool brings up one installation on the given database, indexes the
// fixture into it, and returns what each tool answered.
func answerEachTool(t *testing.T, driver, dsn string, source *httptest.Server, modelURL string) map[string]string {
	t.Helper()
	a := indexedAppOn(t, driver, dsn, source.URL, modelURL)
	answers := map[string]string{}
	for _, ask := range agreementTools {
		answers[ask.tool+" "+ask.arguments] = normaliseAnswer(mcpCall(t, a, ask.tool, ask.arguments))
	}
	return answers
}

// indexedApp brings up an installation with the fixture repository indexed.
func indexedApp(t *testing.T, sourceURL, modelURL, name string) *App {
	t.Helper()
	return indexedAppOn(t, "sqlite",
		"file:"+filepath.Join(t.TempDir(), name+".db")+"?_foreign_keys=on&_busy_timeout=5000", sourceURL, modelURL)
}

func indexedAppOn(t *testing.T, driver, dsn, sourceURL, modelURL string) *App {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	a, err := New(ctx, config.Config{
		DatabaseDriver: driver, DatabaseDSN: dsn,
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatalf("%s: %v", driver, err)
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
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, sourceURL)); saved.Code != http.StatusOK {
		t.Fatalf("%s gitlab settings status=%d body=%s", driver, saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/model",
		fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":"%s/v1","model":"fake-embed","apiKey":"none","dimensions":16,"timeoutSeconds":10,"rerankerEnabled":true,"rerankerProvider":"openai-compatible","rerankerBaseUrl":"%s/v1","rerankerModel":"fake-rerank","rerankerApiKey":"none","rerankerTimeoutSeconds":10}`,
			modelURL, modelURL)); saved.Code != http.StatusOK {
		t.Fatalf("%s model settings status=%d body=%s", driver, saved.Code, saved.Body.String())
	}
	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("%s register status=%d body=%s", driver, registered.Code, registered.Body.String())
	}
	waitFor(t, 90*time.Second, driver+": the repository to finish indexing", func() bool {
		var completed int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM index_jobs WHERE status='completed' AND files_processed>0`)).Scan(&completed)
		return completed > 0
	})

	return a
}

func TestBothDatabasesAnswerTheSameToolsIntegration(t *testing.T) {
	base := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if reason := testsupport.SkipReason("GIT_CTX_TEST_POSTGRES_DSN", base); reason != "" {
		t.Skip(reason)
	}
	source := newFakeGitLab(agreementFixture())
	defer source.Close()
	model := newFakeModelServer()
	defer model.Close()

	dsn, drop, err := testsupport.NewPostgresDatabase(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)

	fromSQLite := answerEachTool(t, "sqlite",
		"file:"+filepath.Join(t.TempDir(), "agreement.db")+"?_foreign_keys=on&_busy_timeout=5000", source, model.URL)
	fromPostgres := answerEachTool(t, "postgres", dsn, source, model.URL)

	var differences []string
	for _, ask := range agreementTools {
		key := ask.tool + " " + ask.arguments
		if fromSQLite[key] == fromPostgres[key] {
			continue
		}
		differences = append(differences, fmt.Sprintf("  %s %s\n%s", ask.tool, ask.arguments,
			sideBySide(fromSQLite[key], fromPostgres[key])))
	}
	if len(differences) > 0 {
		t.Fatalf("the two databases answer %d of %d tool calls differently:\n\n%s",
			len(differences), len(agreementTools), strings.Join(differences, "\n"))
	}
}

// sideBySide shows only the lines that differ, so a long answer does not bury
// the one line that is not the same.
func sideBySide(left, right string) string {
	return sideBySideLabelled("sqlite  ", "postgres", left, right)
}

func sideBySideLabelled(leftName, rightName, left, right string) string {
	leftLines, rightLines := strings.Split(left, "\n"), strings.Split(right, "\n")
	longest := len(leftLines)
	if len(rightLines) > longest {
		longest = len(rightLines)
	}
	var out []string
	for i := 0; i < longest; i++ {
		var l, r string
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		if l == r {
			continue
		}
		out = append(out, "    "+leftName+": "+l, "    "+rightName+": "+r)
		if len(out) >= 12 {
			out = append(out, "    …")
			break
		}
	}
	return strings.Join(out, "\n")
}

// The same repository, published by both platforms, asked the same questions.
//
// This product's whole claim is that an agent asks one question and gets one
// answer whatever the code is hosted on. A Bitbucket Server repository and a
// GitLab one carry different identifiers and different URLs, and that is
// visible in an answer on purpose; nothing else about them should be. The two
// clients are separate code, indexed through separate paths, and every test
// that covers them covers one at a time — so a difference in what an agent is
// told has nowhere to show up.
//
// This indexes the same files as both, then asks the same tools and compares
// the answers with the identifiers folded together.

var sourceAgreementTools = []struct{ tool, arguments string }{
	{"search-code", `{"query":"settlement"}`},
	{"search-code", `{"query":"settleInvoice"}`},
	{"search-code", `{"query":"결제"}`},
	{"search-repositories", `{"query":"payment"}`},
	{"find-file", `{"pattern":"*.go"}`},
	{"list-directory", `{"libraryId":"LIB","path":"internal"}`},
	{"read-file", `{"libraryId":"LIB","path":"README.md"}`},
	{"find-symbol", `{"libraryId":"LIB","query":"settle"}`},
	{"trace-dependencies", `{"libraryId":"LIB","symbol":"settleInvoice"}`},
	{"find-runbook", `{"query":"settlement"}`},
	{"find-code-owner", `{"libraryId":"LIB","path":"config/app.yaml"}`},
	{"find-dependency-usage", `{"name":"express"}`},
	{"get-repository-map", `{"libraryId":"LIB"}`},
	{"explain-search-result", `{"libraryId":"LIB","query":"settlement"}`},
	{"resolve-library-id", `{"libraryName":"api","query":"settlement"}`},
	{"query-docs", `{"libraryId":"LIB","query":"how is settlement retried"}`},
	{"build-context", `{"libraryId":"LIB","query":"settlement"}`},
	{"get-architecture-map", `{}`},
	{"get-repository-health", `{"libraryId":"LIB"}`},
	{"find-tests", `{"libraryId":"LIB","symbol":"settleInvoice"}`},
	{"get-symbol-context", `{"libraryId":"LIB","symbol":"settleInvoice"}`},
}

// foldSourceIdentity removes what the two platforms are entitled to disagree
// on: the library ID, the project key, the slug, the commit and the URL scheme.
func foldSourceIdentity(text string) string {
	// Longest first: a rule that rewrites the library ID would otherwise consume
	// the middle of a URL and leave two answers looking different when they are
	// the same answer about two platforms.
	replacements := []struct{ from, to string }{
		{"gitlab://gitlab~core/api", "REPO"}, {"bitbucket://core/api", "REPO"},
		{"gitlab://core/api", "REPO"}, {"bitbucket://CORE/api", "REPO"},
		{"bitcontext:///gitlab~core/api", "SRC"}, {"bitcontext:///core/api", "SRC"},
		{"/gitlab~core/api", "LIB"}, {"/core/api", "LIB"},
		{"c0ffee1", "COMMIT"},
		{"Bitbucket", "PLATFORM"}, {"GitLab", "PLATFORM"},
		{"bitbucket", "PLATFORM"}, {"gitlab", "PLATFORM"},
		{"CORE", "PROJECTKEY"}, {"core", "PROJECTKEY"},
	}
	for _, r := range replacements {
		text = strings.ReplaceAll(text, r.from, r.to)
	}
	return normaliseAnswer(text)
}

func TestBothPlatformsAnswerTheSameToolsIntegration(t *testing.T) {
	model := newFakeModelServer()
	defer model.Close()
	files := agreementFixture()

	// The two fixtures are made equal on purpose. A repository that differs by
	// name, description or commit makes the two platforms answer differently for
	// reasons that have nothing to do with the platforms.
	const commit = "c0ffee1"
	gitlab := newFakeGitLabProject(&fakeRepository{files: files, commit: commit}, nil, map[string]any{
		"id": 4242, "path_with_namespace": "core/api", "default_branch": "main", "name": "api",
		"description": "payment api", "visibility": "internal", "repository_access_level": "enabled"})
	defer gitlab.Close()
	bitbucket := newFakeBitbucketRepository(files, map[string]any{"id": 7, "slug": "api",
		"name": "api", "description": "payment api", "project": map[string]string{"key": "CORE"},
		"defaultBranch": "refs/heads/main", "archived": false}, commit)
	defer bitbucket.Close()

	fromGitLab := answerEachSource(t, "gitlab", gitlab.URL, model.URL,
		`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`,
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`,
		"/gitlab~core/api")
	fromBitbucket := answerEachSource(t, "bitbucket", bitbucket.URL, model.URL,
		`{"baseUrl":%q,"apiPrefix":"/rest/api/1.0","pat":"token","webhookSecret":"s3cret"}`,
		`{"sourceType":"bitbucket","repository":{"id":7,"projectKey":"CORE","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`,
		"/core/api")

	var differences []string
	for _, ask := range sourceAgreementTools {
		key := ask.tool + " " + ask.arguments
		if fromGitLab[key] == fromBitbucket[key] {
			continue
		}
		differences = append(differences, fmt.Sprintf("  %s %s\n%s", ask.tool, ask.arguments,
			sideBySideLabelled("gitlab  ", "bitbucket", fromGitLab[key], fromBitbucket[key])))
	}
	if len(differences) > 0 {
		t.Fatalf("the two platforms answer %d of %d tool calls differently:\n\n%s",
			len(differences), len(sourceAgreementTools), strings.Join(differences, "\n"))
	}
}

func answerEachSource(t *testing.T, sourceType, sourceURL, modelURL, settings, registration, libraryID string) map[string]string {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	a, err := New(ctx, config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(directory, sourceType+".db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatalf("%s: %v", sourceType, err)
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
	if saved := call(http.MethodPut, "/api/v1/admin/settings/"+sourceType, fmt.Sprintf(settings, sourceURL)); saved.Code != http.StatusOK {
		t.Fatalf("%s settings status=%d body=%s", sourceType, saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/model",
		fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":"%s/v1","model":"fake-embed","apiKey":"none","dimensions":16,"timeoutSeconds":10}`,
			modelURL)); saved.Code != http.StatusOK {
		t.Fatalf("%s model settings status=%d body=%s", sourceType, saved.Code, saved.Body.String())
	}
	if registered := call(http.MethodPost, "/api/v1/admin/repositories", registration); registered.Code != http.StatusCreated {
		t.Fatalf("%s register status=%d body=%s", sourceType, registered.Code, registered.Body.String())
	}
	waitFor(t, 90*time.Second, sourceType+": the repository to finish indexing", func() bool {
		var completed int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM index_jobs WHERE status='completed' AND files_processed>0`)).Scan(&completed)
		return completed > 0
	})

	answers := map[string]string{}
	for _, ask := range sourceAgreementTools {
		arguments := strings.ReplaceAll(ask.arguments, "LIB", libraryID)
		answers[ask.tool+" "+ask.arguments] = foldSourceIdentity(mcpCall(t, a, ask.tool, arguments))
	}
	return answers
}

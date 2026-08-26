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
	{"list-directory", `{"libraryId":"/gitlab~core/api","directory":"internal"}`},
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
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
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

	answers := map[string]string{}
	for _, ask := range agreementTools {
		answers[ask.tool+" "+ask.arguments] = normaliseAnswer(mcpCall(t, a, ask.tool, ask.arguments))
	}
	return answers
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
		out = append(out, "    sqlite:   "+l, "    postgres: "+r)
		if len(out) >= 12 {
			out = append(out, "    …")
			break
		}
	}
	return strings.Join(out, "\n")
}

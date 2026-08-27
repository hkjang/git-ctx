package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/testsupport"
)

// The same corpus, the same questions, both databases.
//
// An installation picks its database, and the agent asking the question does
// not know which one it got. The two indexes are built differently — SQLite
// joins an external-content FTS5 table, PostgreSQL tests a generated tsvector
// column — and everything around them differs too: LIKE folds case in one and
// not the other, and the scan the index falls back to is written once for both.
// Any of that can make one installation answer a question the other cannot.
//
// Rather than assert an expectation per driver, this asks both the same thing
// and requires the same answer, so a divergence shows up as a difference
// instead of as a passing test on the driver someone happened to run.

type corpusChunk struct{ id, path, heading, content string }

var agreementCorpus = []corpusChunk{
	{"c1", "internal/settlement/handler.go", "Settlement", "func settleInvoice(order Order) error { return reconcile(order) }"},
	{"c2", "internal/settlement/Retry.go", "Retry", "func RetrySettlement(order Order) error { return nil }"},
	{"c3", "internal/cache/warm.go", "Cache", "func warmCache() error { return nil }"},
	{"c4", "docs/RUNBOOK.md", "Runbook", "결제 정산 배치가 멈추면 settlement-worker 를 다시 시작한다."},
	{"c5", "internal/ledger/post.go", "Ledger", "// INVOICE totals are posted here.\nfunc PostInvoice() {}"},
	{"c6", "web/vendor/bundle.js", "Bundle", "var a=1;" + strings.Repeat("qz", 1200) + ";var b=2;"},
	{"c7", "internal/billing/tax.go", "Tax", "func computeTax(amount int64) int64 { return amount / 10 }"},
	{"c8", "internal/settlement/README.md", "Settlement notes", "settleInvoice is called by the nightly batch."},
	// The shapes a real catalogue is mostly made of: image tags, package names,
	// chart names and API paths, every one of them carrying punctuation that one
	// query language or the other reads as an operator.
	{"c9", "deploy/values.yaml", "values", "image: registry.company/git-ctx:latest\nchart: spring-boot-starter\n"},
	{"c10", "package.json", "dependencies", `{"dependencies":{"left-pad":"1.0.0","dcgm-exporter":"3.1.8"}}`},
	{"c11", "docs/api.md", "REST API", "GET /api/v1/admin/health returns the effective retrieval mode. Scopes are read:write."},
	{"c12", "src/native/parser.cpp", "Parser", "// written in c++ for speed\nint parse(const char* q) { return 0; }"},
}

var agreementQueries = []string{
	"settleInvoice",
	"settleinvoice",
	"SETTLEINVOICE",
	"settlement",
	"settle",
	"invoice",
	"INVOICE",
	"Retry",
	"retry settlement",
	"warmCache cache",
	"정산",
	"결제 정산",
	"computeTax",
	"nothingmatchesthis",
	strings.Repeat("qz", 1200),
	// Punctuation is where the two query languages are least alike: SQLite takes
	// a quoted phrase with a prefix, PostgreSQL a tsquery of prefixes, and the
	// term is split into pieces before either sees it. A shape that is syntax in
	// one language and text in the other is exactly what this comparison is for.
	"settlement-worker",
	"left-pad",
	"dcgm-exporter",
	"git-ctx",
	"spring-boot-starter",
	"values.yaml",
	"api/v1/admin",
	"read:write",
	"c++",
	"a&b",
	"x|y",
	"--",
	`"quoted"`,
	"(paren)",
	"back\\slash",
}

func seedAgreementCorpus(t *testing.T, ctx context.Context, db *store.Store) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.ExecContext(ctx, db.Rebind(query), args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','payment api','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	for _, chunk := range agreementCorpus {
		exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'gitlab:1','main','abc',?,1,20,?,'code',?,?)`,
			chunk.id, chunk.path, chunk.heading, chunk.content, chunk.id)
	}
}

// answers runs every query and renders what came back as comparable text.
func answers(t *testing.T, ctx context.Context, db *store.Store) map[string]string {
	t.Helper()
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("the platform API is unavailable")
	})
	out := map[string]string{}
	for _, query := range agreementQueries {
		result, err := service.SearchCode(ctx, []string{"alice"}, query, "gitlab", "", "", "", 10)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		paths := make([]string, 0, len(result.Hits))
		for _, hit := range result.Hits {
			paths = append(paths, hit.Path)
		}
		out[query] = strings.Join(paths, " | ")
	}
	return out
}

func TestBothDatabasesAnswerTheSameSearchIntegration(t *testing.T) {
	base := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if reason := testsupport.SkipReason("GIT_CTX_TEST_POSTGRES_DSN", base); reason != "" {
		t.Skip(reason)
	}
	ctx := context.Background()

	lite, err := store.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "agreement.db")+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer lite.DB.Close()
	if !lite.FullTextAvailable() {
		t.Skip("this build has no SQLite full-text index to compare against")
	}
	seedAgreementCorpus(t, ctx, lite)

	dsn, dropDatabase, err := testsupport.NewPostgresDatabase(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dropDatabase)
	full, err := store.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer full.DB.Close()
	if !full.FullTextAvailable() {
		t.Fatal("PostgreSQL must carry a full-text index")
	}
	seedAgreementCorpus(t, ctx, full)

	fromSQLite := answers(t, ctx, lite)
	fromPostgres := answers(t, ctx, full)

	var differences []string
	for _, query := range agreementQueries {
		if fromSQLite[query] != fromPostgres[query] {
			differences = append(differences, fmt.Sprintf("  %-20q\n    sqlite:   %s\n    postgres: %s",
				short(query), or(fromSQLite[query], "(nothing)"), or(fromPostgres[query], "(nothing)")))
		}
	}
	if len(differences) > 0 {
		t.Fatalf("the two databases answer %d of %d searches differently:\n%s",
			len(differences), len(agreementQueries), strings.Join(differences, "\n"))
	}
	// Two databases that find nothing agree about nothing. Some queries here are
	// meant to come back empty — "nothingmatchesthis" is one — but if most of
	// them did, this test would pass while proving that neither index works.
	answered := 0
	for _, query := range agreementQueries {
		if fromSQLite[query] != "" {
			answered++
		}
	}
	if answered*2 < len(agreementQueries) {
		t.Fatalf("only %d of %d searches matched anything, so agreement proves little", answered, len(agreementQueries))
	}
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func short(value string) string {
	if len(value) > 24 {
		return value[:24] + "…"
	}
	return value
}

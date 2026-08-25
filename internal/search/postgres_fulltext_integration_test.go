package search

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/testsupport"
)

// Both supported databases have to answer a content search the same way. The
// index differs — SQLite joins an FTS table, PostgreSQL tests a generated
// column — and a difference in what they find would mean the answer an agent
// gets depends on which database an installation happens to run.
func TestPostgresFullTextIntegration(t *testing.T) {
	base := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if reason := testsupport.SkipReason("GIT_CTX_TEST_POSTGRES_DSN", base); reason != "" {
		t.Skip(reason)
	}
	ctx := context.Background()
	dsn, dropDatabase, err := testsupport.NewPostgresDatabase(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dropDatabase)
	db, err := store.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if !db.FullTextAvailable() {
		t.Fatal("PostgreSQL must carry a full-text index")
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.ExecContext(ctx, db.Rebind(query), args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','gitlab:1','main','abc','internal/settlement/handler.go',1,20,'Settlement','code','func settleInvoice() error { return nil }','h1')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c2','gitlab:1','main','abc','internal/cache/warm.go',1,20,'Cache','code','func warmCache() error { return nil }','h2')`)

	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("gitlab search API is unavailable")
	})
	for _, query := range []string{"settleInvoice", "settlement", "settle", "invoice"} {
		result, err := service.SearchCode(ctx, []string{"alice"}, query, "gitlab", "", "", "", 10)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if len(result.Hits) == 0 || result.Hits[0].Path != "internal/settlement/handler.go" {
			t.Fatalf("%q found %#v", query, result.Hits)
		}
	}
	absent, err := service.SearchCode(ctx, []string{"alice"}, "kubernetes", "gitlab", "", "", "", 10)
	if err == nil && len(absent.Hits) != 0 {
		t.Fatalf("unrelated term matched: %#v", absent.Hits)
	}
	denied, err := service.SearchCode(ctx, []string{"mallory"}, "settleInvoice", "gitlab", "", "", "", 10)
	if err == nil && len(denied.Hits) != 0 {
		t.Fatalf("the lookup leaked past the ACL: %#v", denied.Hits)
	}

	// The generated column follows the row, so a re-indexed chunk is searchable
	// by its new content and not by its old one.
	exec(`UPDATE document_chunks SET content='func settleRefund() error { return nil }' WHERE id='c1'`)
	refund, err := service.SearchCode(ctx, []string{"alice"}, "settleRefund", "gitlab", "", "", "", 10)
	if err != nil || len(refund.Hits) == 0 {
		t.Fatalf("updated content is not searchable: %#v err=%v", refund.Hits, err)
	}
	var vectors int
	if err = db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_chunks WHERE search_vector @@ to_tsquery('simple','settlerefund:*')`).Scan(&vectors); err != nil || vectors != 1 {
		t.Fatalf("generated column=%d err=%v", vectors, err)
	}
	if !strings.Contains(strings.Join(refund.Diagnostics, " "), "index:") {
		t.Fatalf("the answer must say it came from the index: %v", refund.Diagnostics)
	}
}

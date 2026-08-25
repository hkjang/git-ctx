package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"git-ctx/internal/toolcatalog"
)

func TestMCPToolPolicyCatalogCoversGrantableScopes(t *testing.T) {
	s, err := Open(context.Background(), "sqlite", "file:mcp-policy-catalog?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	rows, err := s.DB.Query(`SELECT name FROM mcp_tools`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	configured := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		configured[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range toolcatalog.Names() {
		if !configured[name] {
			t.Errorf("MCP tool %q can be granted to a key but has no administrator policy row", name)
		}
	}
}

func TestRepositoryIdentityIncludesSourceType(t *testing.T) {
	s, err := Open(context.Background(), "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	insert := `INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id) VALUES(?,?,?,?,?,?,?)`
	if _, err = s.DB.Exec(insert, "bitbucket:1", "TEAM", "demo", "Demo", "bitbucket", "1", "/team/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(insert, "gitlab:1", "TEAM", "demo", "Demo", "gitlab", "1", "/gitlab~team/demo"); err != nil {
		t.Fatalf("same project and slug from another source must be accepted: %v", err)
	}
}

func TestLegacyRepositoryUniquenessMigration(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "legacy.db") + "?_foreign_keys=on"
	s, err := Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM schema_migrations WHERE version='020_repository_source_uniqueness.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	legacy := `
CREATE TABLE repositories_legacy (
  id TEXT PRIMARY KEY, project_key TEXT NOT NULL, slug TEXT NOT NULL,
  name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  source_type TEXT NOT NULL DEFAULT 'bitbucket', source_external_id TEXT NOT NULL DEFAULT '',
  library_id TEXT NOT NULL UNIQUE, default_branch TEXT NOT NULL DEFAULT 'main',
  reputation TEXT NOT NULL DEFAULT 'Medium', enabled INTEGER NOT NULL DEFAULT 1,
  indexed_at TIMESTAMP, UNIQUE(project_key,slug)
);
INSERT INTO repositories_legacy SELECT * FROM repositories;
DROP TABLE repositories;
ALTER TABLE repositories_legacy RENAME TO repositories;`
	if _, err = s.DB.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	s, err = Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("upgrade legacy schema: %v", err)
	}
	defer s.DB.Close()
	insert := `INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id) VALUES(?,?,?,?,?,?,?)`
	_, _ = s.DB.Exec(insert, "bitbucket:1", "TEAM", "demo", "Demo", "bitbucket", "1", "/team/demo")
	if _, err = s.DB.Exec(insert, "gitlab:1", "TEAM", "demo", "Demo", "gitlab", "1", "/gitlab~team/demo"); err != nil {
		t.Fatalf("migrated uniqueness still conflicts: %v", err)
	}
}

// The index is only useful if it tracks the table. Chunks are replaced wholesale
// when a ref is re-indexed, so an index that missed deletes would answer with
// files that no longer exist — worse than having no index at all.
func TestFullTextIndexTracksTheChunkTable(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, "sqlite", "file:fts-sync?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if !db.FullTextAvailable() {
		t.Skip("this build has no full-text index")
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('r1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','r1','main','abc','internal/settlement.go',1,9,'Settlement','code','func settleInvoice() error { return nil }','h1')`)

	matches := func(query string) int {
		t.Helper()
		var count int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_fts WHERE document_chunks_fts MATCH ?`, query).Scan(&count); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return count
	}
	if matches(FullTextQuery([]string{"settleinvoice"})) != 1 {
		t.Fatal("an inserted chunk must be searchable")
	}
	exec(`UPDATE document_chunks SET content='func settleRefund() error { return nil }' WHERE id='c1'`)
	if matches(FullTextQuery([]string{"settleinvoice"})) != 0 || matches(FullTextQuery([]string{"settlerefund"})) != 1 {
		t.Fatal("an updated chunk must be re-indexed, not duplicated")
	}
	exec(`DELETE FROM document_chunks WHERE id='c1'`)
	if matches(FullTextQuery([]string{"settlerefund"})) != 0 {
		t.Fatal("a deleted chunk must leave the index")
	}

	// A database upgraded from a build without the index has chunks the index
	// has never seen. Opening the store must make them searchable, or the
	// upgrade would quietly leave the estate unsearchable.
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c2','r1','main','abc','internal/ledger.go',1,9,'Ledger','code','func postLedger() error { return nil }','h2')`)
	exec(`DROP TRIGGER document_chunks_fts_insert`)
	exec(`DROP TRIGGER document_chunks_fts_update`)
	exec(`DROP TRIGGER document_chunks_fts_delete`)
	exec(`DROP TABLE document_chunks_fts`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c3','r1','main','abc','internal/quota.go',1,9,'Quota','code','func applyQuota() error { return nil }','h3')`)
	reopened := &Store{DB: db.DB, driver: "sqlite"}
	reopened.prepareFullText(ctx)
	if !reopened.FullTextAvailable() {
		t.Fatal("the index must be created on open")
	}
	for _, term := range []string{"postledger", "applyquota"} {
		if matches(FullTextQuery([]string{term})) != 1 {
			t.Fatalf("%q was not indexed by the rebuild", term)
		}
	}
}

// Every on-premises installation eventually does the one thing no test covered:
// stop an older binary and start a newer one on the database it left behind.
// Verified by hand against a v0.50.0 database, this keeps it: the migrations
// that came later apply, the rows survive, and the search index — which did not
// exist in that release — is filled with the content that was already there.
// Without that backfill an upgrade leaves the estate silently unsearchable.
func TestUpgradingAnOlderDatabaseKeepsItSearchable(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "upgrade.db") + "?_foreign_keys=on"
	older, err := Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Rewind to the schema an older release left behind: forget the migrations
	// added since, and drop what they created.
	later := []string{"043_dependency_inventory.sql", "044_resolved_versions.sql",
		"045_webhook_visibility.sql", "046_policy_revision.sql"}
	for _, name := range later {
		if _, err = older.DB.Exec(`DELETE FROM schema_migrations WHERE version=?`, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`DROP TABLE IF EXISTS repository_packages`,
		`DROP TABLE IF EXISTS repository_packages_staging`,
		`DROP TRIGGER IF EXISTS document_chunks_fts_insert`,
		`DROP TRIGGER IF EXISTS document_chunks_fts_update`,
		`DROP TRIGGER IF EXISTS document_chunks_fts_delete`,
		`DROP TABLE IF EXISTS document_chunks_fts`,
		`DROP INDEX IF EXISTS idx_webhook_events_received`,
		`ALTER TABLE webhook_events DROP COLUMN detail`,
		`ALTER TABLE repository_ref_states DROP COLUMN policy_revision`,
	} {
		if _, err = older.DB.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	// Content indexed by that older release.
	if _, err = older.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','payment api','gitlab','1','/core/api','main',1)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 5; index++ {
		if _, err = older.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'gitlab:1','main','old',?,1,20,'Payment','code',?,?)`,
			"c"+strconv.Itoa(index), "internal/payment_"+strconv.Itoa(index)+".go",
			"func settleInvoice"+strconv.Itoa(index)+"() error { return nil }", "h"+strconv.Itoa(index)); err != nil {
			t.Fatal(err)
		}
	}
	if err = older.DB.Close(); err != nil {
		t.Fatal(err)
	}

	// The newer binary opens the same file.
	upgraded, err := Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("the upgrade refused to start: %v", err)
	}
	defer upgraded.DB.Close()
	for _, name := range later {
		var applied int
		if err = upgraded.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, name).Scan(&applied); err != nil || applied != 1 {
			t.Fatalf("%s was not applied: %d err=%v", name, applied, err)
		}
	}
	var chunks int
	if err = upgraded.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks`).Scan(&chunks); err != nil || chunks != 5 {
		t.Fatalf("the upgrade lost content: %d err=%v", chunks, err)
	}
	var policyColumn int
	if err = upgraded.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('repository_ref_states') WHERE name='policy_revision'`).Scan(&policyColumn); err != nil || policyColumn != 1 {
		t.Fatalf("the new column is missing: %d err=%v", policyColumn, err)
	}
	if !upgraded.FullTextAvailable() {
		t.Skip("this build has no full-text index")
	}
	// The rows predate the index, so the index has to be filled from them.
	var indexed int
	if err = upgraded.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_fts WHERE document_chunks_fts MATCH ?`, FullTextQuery([]string{"settleinvoice"})).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 5 {
		t.Fatalf("an upgrade left %d of 5 chunks unsearchable", indexed)
	}
}

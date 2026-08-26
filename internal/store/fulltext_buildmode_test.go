package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"git-ctx/internal/store"
)

// A database outlives the binary that made it, and the two binaries this
// project builds do not have the same SQLite. The search index is maintained
// by triggers that need FTS5, and those triggers stay in the schema after the
// binary that created them is replaced by one built without the tag. A trigger
// that cannot run fails the statement that fired it, so every write to
// document_chunks fails and indexing stops entirely — reporting a missing
// SQLite module, which names nothing an operator changed.
//
// These tests cover what a build with the module has to do about that: rebuild
// an index it finds unmaintained, and react only to the columns it indexes. The
// crossover itself needs two binaries and is checked by
// test/store/build-mode.test.sh.

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), "sqlite", "file:"+path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.DB.Close() })
	return s
}

func seedRepository(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO repositories(id,project_key,slug,name,library_id,default_branch) VALUES('r','P','r','R','/gitlab~P/r','main') ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
}

func insertChunk(db *sql.DB, id, content string) error {
	_, err := db.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, "r", "main", "c1", "src/"+id+".go", 1, 9, "H", "code", content, "h", time.Now().UTC())
	return err
}

func TestAnIndexLeftUnmaintainedIsRebuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rebuild.db")
	first := openStore(t, path)
	if !first.FullTextAvailable() {
		t.Skip("this build has no full-text index to leave behind")
	}
	seedRepository(t, first.DB)
	if err := insertChunk(first.DB, "known", "func settleInvoice(order reconciliation) error"); err != nil {
		t.Fatal(err)
	}
	// Leave the database the way a binary without the module leaves it: the
	// triggers gone, and rows written while they were gone.
	for _, trigger := range []string{"document_chunks_fts_insert", "document_chunks_fts_delete", "document_chunks_fts_update"} {
		if _, err := first.DB.Exec(`DROP TRIGGER IF EXISTS ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.DB.Exec(`INSERT INTO index_maintenance(name) VALUES('fulltext_unmaintained') ON CONFLICT(name) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := insertChunk(first.DB, "unseen", "func reconcileSettlement(order ledger) error"); err != nil {
		t.Fatal(err)
	}
	_ = first.DB.Close()

	second := openStore(t, path)
	if !second.FullTextAvailable() {
		t.Fatal("the index was given up rather than rebuilt")
	}
	var found int
	if err := second.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_fts WHERE document_chunks_fts MATCH ?`, `"reconcileSettlement"*`).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("the index still does not describe the table: %d rows match a chunk that is in it", found)
	}
	var noted int
	if err := second.DB.QueryRow(`SELECT COUNT(*) FROM index_maintenance WHERE name='fulltext_unmaintained'`).Scan(&noted); err != nil {
		t.Fatal(err)
	}
	if noted != 0 {
		t.Fatal("the rebuild did not clear the note, so every start from now on rebuilds")
	}
}

func TestStampingACommitDoesNotReindexUnchangedText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stamp.db")
	s := openStore(t, path)
	if !s.FullTextAvailable() {
		t.Skip("no full-text index in this build")
	}
	seedRepository(t, s.DB)
	for i := 0; i < 400; i++ {
		if err := insertChunk(s.DB, fmt.Sprintf("c%d", i), fmt.Sprintf("func settleInvoice%d(order reconciliation) error", i)); err != nil {
			t.Fatal(err)
		}
	}
	// Finishing a ref stamps the commit onto every chunk in it. None of the
	// indexed columns change, so the index has no work to do.
	var sql string
	if err := s.DB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='document_chunks_fts_update'`).Scan(&sql); err != nil {
		t.Fatal(err)
	}
	if !contains(sql, "AFTER UPDATE OF file_path,heading,content") {
		t.Fatalf("the update trigger reacts to every column, so a commit stamp re-indexes the whole ref: %s", sql)
	}
	if _, err := s.DB.Exec(`UPDATE document_chunks SET commit_id='c2',indexed_at=? WHERE repository_id='r' AND ref_name='main'`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// It still has to react when the text does change.
	if _, err := s.DB.Exec(`UPDATE document_chunks SET content='func chargeback(order ledger) error' WHERE id='c1'`); err != nil {
		t.Fatal(err)
	}
	var found int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_fts WHERE document_chunks_fts MATCH ?`, `"chargeback"*`).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("edited text is not searchable: %d matches", found)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_fts WHERE document_chunks_fts MATCH ?`, `"settleInvoice1"`).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != 0 {
		t.Fatalf("the replaced text is still searchable: %d matches", found)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

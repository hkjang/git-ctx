// Open a store the way the server does and write to it, reporting what the
// database allowed. Built twice by test/store/build-mode.test.sh — once with
// SQLite's FTS5 module and once without — against the same database file.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"git-ctx/internal/store"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: probe DATABASE CHUNK-ID")
		os.Exit(2)
	}
	path, id := os.Args[1], os.Args[2]
	s, err := store.Open(context.Background(), "sqlite", "file:"+path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		fmt.Printf("open=failed %v\n", err)
		os.Exit(1)
	}
	defer s.DB.Close()
	fmt.Printf("fulltext=%v\n", s.FullTextAvailable())
	if _, err = s.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,library_id,default_branch) VALUES('r','P','r','R','/gitlab~P/r','main') ON CONFLICT(id) DO NOTHING`); err != nil {
		fmt.Printf("seed=failed %v\n", err)
		os.Exit(1)
	}
	report := func(step string, err error) {
		if err != nil {
			fmt.Printf("%s=failed %v\n", step, err)
			os.Exit(1)
		}
		fmt.Printf("%s=ok\n", step)
	}
	_, err = s.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, "r", "main", "c1", "src/"+id+".go", 1, 9, "H", "code", "func settleInvoice(order reconciliation) error", "h", time.Now().UTC())
	report("insert", err)
	_, err = s.DB.Exec(`UPDATE document_chunks SET content='func chargeback(order ledger) error' WHERE id=?`, id)
	report("update", err)
	_, err = s.DB.Exec(`UPDATE document_chunks SET commit_id='c2' WHERE repository_id='r' AND ref_name='main'`)
	report("stamp", err)
	if s.FullTextAvailable() {
		var found int
		if err = s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks_fts WHERE document_chunks_fts MATCH ?`, `"chargeback"*`).Scan(&found); err != nil {
			fmt.Printf("search=failed %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("search=%d\n", found)
	}
}

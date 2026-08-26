// Write to a database, or read one back, using only what every released
// version of this store has had: Open, Rebind and the tables from the first
// migration. Built from an old tag and from the working tree by
// test/store/upgrade.test.sh, so the two halves must not depend on anything
// that arrived in between.
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
		fmt.Println("usage: upgradeprobe seed|verify DATABASE")
		os.Exit(2)
	}
	mode, path := os.Args[1], os.Args[2]
	s, err := store.Open(context.Background(), "sqlite", "file:"+path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		fmt.Printf("open=failed %v\n", err)
		os.Exit(1)
	}
	defer s.DB.Close()

	var migrations int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		fmt.Printf("migrations=unknown %v\n", err)
	} else {
		fmt.Printf("migrations=%d\n", migrations)
	}

	switch mode {
	case "seed":
		steps := []struct {
			name  string
			query string
			args  []any
		}{
			{"repository", `INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','payment api','gitlab','1','/gitlab~core/api','main',1)`, nil},
			{"permission", `INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','group:eng','read')`, nil},
			{"chunk", `INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES('c1','gitlab:1','main','c0ffee','internal/settlement/handler.go',1,9,'settleInvoice','code','func settleInvoice(order Order) error { return reconcile(order) }','h1',?)`, []any{time.Now().UTC()}},
			{"chunk2", `INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES('c2','gitlab:1','main','c0ffee','docs/RUNBOOK.md',1,4,'장애 대응 런북','document','컨슈머를 재시작한다.','h2',?)`, []any{time.Now().UTC()}},
		}
		for _, step := range steps {
			if _, err := s.DB.Exec(s.Rebind(step.query), step.args...); err != nil {
				fmt.Printf("%s=failed %v\n", step.name, err)
				os.Exit(1)
			}
			fmt.Printf("%s=ok\n", step.name)
		}
	case "verify":
		var chunks int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM document_chunks`).Scan(&chunks); err != nil {
			fmt.Printf("chunks=failed %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("chunks=%d\n", chunks)
		var content string
		if err := s.DB.QueryRow(s.Rebind(`SELECT content FROM document_chunks WHERE id=?`), "c1").Scan(&content); err != nil {
			fmt.Printf("read=failed %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("read=%q\n", content)
		// Writing after the upgrade is the half that broke when a schema change
		// left a trigger the new binary could not run.
		if _, err := s.DB.Exec(s.Rebind(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES('after','gitlab:1','main','c0ffee','internal/new.go',1,2,'New','code','func New() {}','h3',?)`), time.Now().UTC()); err != nil {
			fmt.Printf("write=failed %v\n", err)
			os.Exit(1)
		}
		fmt.Println("write=ok")
	default:
		fmt.Println("unknown mode " + mode)
		os.Exit(2)
	}
}

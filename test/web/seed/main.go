// Fill a database with the kind of estate an operator actually looks at.
//
// The console render sweep has only ever run against an empty installation, so
// every screen was checked in the one state where it has nothing to draw. This
// writes repositories, indexed content, symbols, jobs, calls, webhook events,
// notifications and a backup record straight into the tables the console reads,
// which is what those screens render.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"git-ctx/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: seed DATABASE")
		os.Exit(2)
	}
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:"+os.Args[1]+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		fmt.Printf("open: %v\n", err)
		os.Exit(1)
	}
	defer s.DB.Close()

	now := time.Now().UTC()
	exec := func(what, query string, args ...any) {
		if _, err := s.DB.ExecContext(ctx, s.Rebind(query), args...); err != nil {
			fmt.Printf("%s: %v\n", what, err)
			os.Exit(1)
		}
	}

	exec("user", `INSERT INTO users(id,subject,username,email,status) VALUES('dev','dev-subject','dev','dev@example.internal','active') ON CONFLICT(id) DO NOTHING`)
	exec("identity", `INSERT INTO user_identities(user_id,bitbucket_user_slug,gitlab_user_id,mapping_source,bitbucket_groups) VALUES('dev','dev','11','manual','group:platform') ON CONFLICT(user_id) DO NOTHING`)

	repositories := []struct {
		id, project, slug, name, description, source, library string
	}{
		{"gitlab:1", "core", "api", "Payment API", "결제 처리 서비스", "gitlab", "/gitlab~core/api"},
		{"gitlab:2", "core", "ledger", "Ledger", "원장", "gitlab", "/gitlab~core/ledger"},
		{"bitbucket:1", "OPS", "tooling", "Tooling", "운영 도구", "bitbucket", "/ops/tooling"},
		{"confluence:1", "OPS", "OPS", "Operations space", "", "confluence", "/confluence~ops/ops"},
		{"jira:1", "PAY", "PAY", "Payments project", "", "jira", "/jira~pay/pay"},
	}
	for index, repository := range repositories {
		exec("repository", `INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled,indexed_at) VALUES(?,?,?,?,?,?,?,?,'main',1,?)`,
			repository.id, repository.project, repository.slug, repository.name, repository.description,
			repository.source, fmt.Sprint(index+1), repository.library, now.Add(-time.Duration(index)*36*time.Hour))
		exec("permission", `INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,'group:platform','read')`, repository.id)
		exec("ref state", `INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at) VALUES(?,'main',?,?)`,
			repository.id, fmt.Sprintf("c0ffee%d", index), now.Add(-time.Duration(index)*36*time.Hour))
		for chunk := 0; chunk < 4; chunk++ {
			exec("chunk", `INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES(?,?,'main',?,?,?,?,?,?,?,?,?)`,
				fmt.Sprintf("%s-c%d", repository.id, chunk), repository.id, fmt.Sprintf("c0ffee%d", index),
				fmt.Sprintf("internal/settlement/handler%d.go", chunk), 1+chunk*10, 9+chunk*10,
				fmt.Sprintf("settleInvoice%d", chunk), "code",
				fmt.Sprintf("func settleInvoice%d(order Order) error { return reconcile(order) }", chunk),
				fmt.Sprintf("h%d", chunk), now.Add(-time.Duration(index)*36*time.Hour))
			exec("file", `INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES(?,'main',?,?,?,?,?)`,
				repository.id, fmt.Sprintf("internal/settlement/handler%d.go", chunk),
				fmt.Sprintf("handler%d.go", chunk), 400+chunk, 1, fmt.Sprintf("c0ffee%d", index))
			exec("symbol", `INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash,indexed_at) VALUES(?,?,'main',?,?,?,?,'function','go',?,?,?,?,?,?)`,
				fmt.Sprintf("%s-s%d", repository.id, chunk), repository.id, fmt.Sprintf("c0ffee%d", index),
				fmt.Sprintf("internal/settlement/handler%d.go", chunk),
				fmt.Sprintf("settleInvoice%d", chunk), fmt.Sprintf("settlement.settleInvoice%d", chunk),
				fmt.Sprintf("func settleInvoice%d(order Order) error", chunk), "정산을 처리한다.",
				1+chunk*10, 9+chunk*10, fmt.Sprintf("s%d", chunk), now)
		}
		exec("package", `INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id,indexed_at) VALUES(?,'main','npm','express','express','^4.18.0','direct','package.json',?,?)`,
			repository.id, fmt.Sprintf("c0ffee%d", index), now)
		exec("map", `INSERT INTO repository_maps(repository_id,ref_name,commit_id,summary_json,generated_at) VALUES(?,'main',?,?,?)`,
			repository.id, fmt.Sprintf("c0ffee%d", index),
			`{"languages":{"go":4},"symbols":{"function":4},"directories":["internal"],"keyFiles":["README.md"],"entryPoints":["internal/settlement/handler0.go:settleInvoice0"]}`, now)
	}

	// Jobs in every state the operator can see, including one that failed.
	for index, item := range []struct {
		status, message string
		files           int
	}{{"completed", "", 12}, {"completed", "", 8}, {"failed", "gitlab API 503 Service Unavailable", 0}, {"running", "", 0}, {"queued", "", 0}} {
		exec("job", `INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,files_processed,error_message,created_at,started_at,completed_at) VALUES(?,?,'main','full',?,?,?,?,?,?)`,
			fmt.Sprintf("job-%d", index), repositories[index%len(repositories)].id, item.status, item.files, item.message,
			now.Add(-time.Duration(index)*time.Hour), now.Add(-time.Duration(index)*time.Hour), now.Add(-time.Duration(index)*time.Hour+time.Minute))
	}

	for index, item := range []struct {
		tool, outcome, library string
		duration               int
	}{
		{"search-code", "success", "/gitlab~core/api", 42},
		{"query-docs", "success", "/gitlab~core/api", 120},
		{"read-file", "error", "/gitlab~core/ledger", 8},
		{"find-symbol", "empty", "/ops/tooling", 15},
		{"search-code", "tool_not_permitted", "", 2},
	} {
		exec("call", `INSERT INTO mcp_calls(id,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,result_count,cache_hit,retrieval_mode,occurred_at) VALUES(?,'dev','bctx_live_ab',?,?,?,?,'10.0.0.5',2048,0,3,0,'hybrid-fallback',?)`,
			fmt.Sprintf("call-%d", index), item.tool, item.library, item.outcome, item.duration, now.Add(-time.Duration(index)*time.Minute))
	}

	exec("webhook", `INSERT INTO webhook_events(id,source_type,external_event_id,repository_id,event_type,payload_hash,status,received_at,processed_at) VALUES('wh-1','gitlab','evt-1','gitlab:1','push','h1','processed',?,?)`, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	exec("webhook", `INSERT INTO webhook_events(id,source_type,external_event_id,repository_id,event_type,payload_hash,status,received_at) VALUES('wh-2','bitbucket','evt-2','bitbucket:1','repo:refs_changed','h2','failed',?)`, now.Add(-time.Hour))
	exec("security", `INSERT INTO index_security_events(id,repository_id,ref_name,file_path,finding_type,action) VALUES('sec-1','gitlab:1','main','config/app.yaml','credential','masked') ON CONFLICT(id) DO NOTHING`)
	exec("notification", `INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message,created_at) VALUES('n-1','dev','index.failed','gitlab:1','색인 실패','gitlab:1 main 색인이 실패했습니다.',?) ON CONFLICT(id) DO NOTHING`, now.Add(-30*time.Minute))
	exec("backup", `INSERT INTO backup_records(id,filename,trigger_type,status,size_bytes,sha256,created_by,created_at,completed_at) VALUES('backup-seed','backup-seed.gctxbak','manual','completed',4096,'0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef','operator',?,?) ON CONFLICT(id) DO NOTHING`, now.Add(-6*time.Hour), now.Add(-6*time.Hour))
	exec("audit", `INSERT INTO audit_logs(id,actor_id,action,resource_type,resource_id,outcome,ip_address,occurred_at) VALUES('audit-1','operator','settings.update','settings','gitlab','success','10.0.0.5',?)`, now.Add(-3*time.Hour))

	fmt.Println("seeded")
}

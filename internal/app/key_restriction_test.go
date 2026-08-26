package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
)

// An API key restricted to some repositories is a security boundary, and the
// only way to know it holds is to put the same content in a repository the key
// may read and one it may not, then ask every tool.
//
// A test that finds no leak because there was nothing to leak proves nothing,
// so each tool is asked twice: once with the restricted key, once with an
// unrestricted one. The unrestricted answer has to contain the forbidden
// repository — otherwise that tool is not exercising the boundary at all and
// the test says so rather than passing quietly.

const allowedLibrary = "/gitlab~core/api"
const forbiddenLibrary = "/gitlab~core/other"

// restrictionTools are the read tools a key holder can reach.
var restrictionTools = []struct{ tool, arguments string }{
	{"search-code", `{"query":"settleInvoice"}`},
	{"search-semantic", `{"query":"settleInvoice"}`},
	{"find-file", `{"pattern":"*.go"}`},
	{"find-symbol", `{"query":"settleInvoice"}`},
	{"search-repositories", `{"query":"settlement"}`},
	{"find-dependents", `{"target":"reconcile"}`},
	{"find-dependency-usage", `{"name":"express"}`},
	{"get-architecture-map", `{}`},
	{"find-runbook", `{"query":"settlement"}`},
	{"resolve-library-id", `{"libraryName":"settlement","query":"settleInvoice"}`},
	{"build-context", `{"query":"settleInvoice"}`},
}

func seedTwoRepositories(t *testing.T, a *App) {
	t.Helper()
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := a.store.DB.Exec(a.store.Rebind(query), args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	must(`INSERT INTO users(id,subject,username,email,status) VALUES('dev','dev','dev','','active')`)
	must(`INSERT INTO user_identities(user_id,bitbucket_user_slug,gitlab_user_id,mapping_source,bitbucket_groups) VALUES('dev','','dev','manual','group:eng')`)
	now := time.Now().UTC()
	for _, repository := range []struct{ id, slug, library string }{
		{"gitlab:1", "api", allowedLibrary},
		{"gitlab:2", "other", forbiddenLibrary},
	} {
		must(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled,indexed_at) VALUES(?,'core',?,?,'settlement service','gitlab',?,?,'main',1,?)`,
			repository.id, repository.slug, repository.slug, repository.id, repository.library, now)
		must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,'group:eng','read')`, repository.id)
		must(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at) VALUES(?,'main','c0ffee',?)`, repository.id, now)
		must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES(?,?,'main','c0ffee','internal/settlement/handler.go',1,9,'settleInvoice','code','func settleInvoice(order Order) error { return reconcile(order) }','h',?)`,
			repository.id+"-c1", repository.id, now)
		must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES(?,?,'main','c0ffee','docs/RUNBOOK.md',1,4,'Settlement runbook','document','settlement 배치가 멈추면 재시작한다.','h',?)`,
			repository.id+"-c2", repository.id, now)
		must(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES(?,'main','internal/settlement/handler.go','handler.go',400,1,'c0ffee')`, repository.id)
		must(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES(?,'main','CODEOWNERS','CODEOWNERS',40,1,'c0ffee')`, repository.id)
		must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,indexed_at) VALUES(?,?,'main','c0ffee','CODEOWNERS',1,1,'CODEOWNERS','document','* @platform-team','h',?)`,
			repository.id+"-c3", repository.id, now)
		must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash,indexed_at) VALUES(?,?,'main','c0ffee','internal/settlement/handler.go','settleInvoice','settlement.settleInvoice','function','go','func settleInvoice(order Order) error','정산을 처리한다.',1,9,'h',?)`,
			repository.id+"-s1", repository.id, now)
		must(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number,indexed_at) VALUES(?,?,'main','c0ffee','internal/settlement/handler.go','settleInvoice','reconcile','call',3,?)`,
			repository.id+"-d1", repository.id, now)
		must(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id,indexed_at) VALUES(?,'main','npm','express','express','^4.18.0','direct','package.json','c0ffee',?)`,
			repository.id, now)
		must(`INSERT INTO repository_maps(repository_id,ref_name,commit_id,summary_json,generated_at) VALUES(?,'main','c0ffee',?,?)`,
			repository.id, `{"languages":{"go":1},"symbols":{"function":1},"directories":["internal"],"keyFiles":[],"entryPoints":["internal/settlement/handler.go:settlement.settleInvoice"]}`, now)
	}
}

func TestARestrictedKeyNeverSeesTheOtherRepositoryIntegration(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	t.Setenv("GIT_CTX_DB_DSN", "file:"+filepath.Join(directory, "restrict.db")+"?_foreign_keys=on&_busy_timeout=5000")
	t.Setenv("GIT_CTX_RECOVERY_KEY", strings.Repeat("r", 48))
	t.Setenv("GIT_CTX_PREVIOUS_RECOVERY_KEY", "")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupDirectory = filepath.Join(directory, "backups")
	a, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	seedTwoRepositories(t, a)

	scopes := make([]string, 0, len(restrictionTools))
	for _, ask := range restrictionTools {
		scopes = append(scopes, ask.tool)
	}
	_, restricted, err := a.keys.Create(ctx, "dev", "one-repo", scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, unrestricted, err := a.keys.Create(ctx, "dev", "everything", scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	var keyID string
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT id FROM api_keys WHERE name='one-repo'`)).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.DB.Exec(a.store.Rebind(
		`INSERT INTO api_key_restrictions(api_key_id,allowed_repositories) VALUES(?,?)
		 ON CONFLICT(api_key_id) DO UPDATE SET allowed_repositories=excluded.allowed_repositories`),
		keyID, allowedLibrary); err != nil {
		t.Fatal(err)
	}

	answer := func(secret, tool, arguments string) string {
		t.Helper()
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, arguments)
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tool, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}

	for _, ask := range restrictionTools {
		open := answer(unrestricted, ask.tool, ask.arguments)
		if !strings.Contains(open, forbiddenLibrary) {
			t.Errorf("%s does not exercise the boundary: an unrestricted key does not see %s either, so a restricted key finding nothing proves nothing",
				ask.tool, forbiddenLibrary)
			continue
		}
		guarded := answer(restricted, ask.tool, ask.arguments)
		if strings.Contains(guarded, forbiddenLibrary) {
			t.Errorf("%s returned %s to a key restricted to %s", ask.tool, forbiddenLibrary, allowedLibrary)
		}
		if !strings.Contains(guarded, allowedLibrary) {
			t.Errorf("%s returned nothing about %s, which the key may read", ask.tool, allowedLibrary)
		}
	}
}

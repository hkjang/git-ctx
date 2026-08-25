package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"git-ctx/internal/auth"
	"git-ctx/internal/search"
	"git-ctx/internal/store"
)

func fixture(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(context.Background(), "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	must := func(q string, args ...any) {
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO users(id,subject,username,email) VALUES('u1','alice','alice','')`)
	must(`INSERT INTO repositories(id,project_key,slug,name,description,library_id,default_branch,reputation) VALUES('r1','KCB','clustara','Clustara','Kubernetes GPU platform','/kcb/clustara','main','High')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','alice','read')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','r1','main','4fa21bd','docs/gpu.md',20,30,'GPU metrics','document','Use DCGM exporter for Pod GPU metrics.','h1')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c2','r1','main','4fa21bd','service.go',20,30,'Service.GetGPU','code','func (s *Service) GetGPU() error { return nil }','h2')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c3','r1','main','4fa21bd','docs/runbooks/gpu.md',1,12,'GPU Alert Runbook','document','Restart the DCGM exporter when GPU metrics are missing.','h3')`)
	must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash) VALUES('s1','r1','main','4fa21bd','service.go','GetGPU','Service.GetGPU','method','go','func (s *Service) GetGPU() error','Returns GPU metrics.',20,30,'sh1')`)
	must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash) VALUES('s2','r1','release','5ba31ce','service.go','GetGPU','Service.GetGPU','method','go','func (s *Service) GetGPU(ctx context.Context) error','Returns GPU metrics.',20,31,'sh2')`)
	must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash) VALUES('s3','r1','release','5ba31ce','handler.go','HandleGPU','HandleGPU','function','go','func HandleGPU() error','Handles GPU requests.',10,15,'sh3')`)
	must(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES('d1','r1','release','5ba31ce','handler.go','HandleGPU','Service.GetGPU','call',12)`)
	must(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('r1','main','4fa21bd')`)
	must(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('r1','release','5ba31ce')`)
	must(`INSERT INTO repository_maps(repository_id,ref_name,commit_id,summary_json) VALUES('r1','main','4fa21bd','{"languages":{"go":1},"symbols":{"method":1},"directories":["docs"],"keyFiles":["README.md"],"entryPoints":["service.go:Service.GetGPU"]}')`)
	must(`INSERT INTO context_packs(id,slug,name,description,created_by) VALUES('p1','gpu-platform','GPU Platform','GPU operations and APIs','u1')`)
	must(`INSERT INTO context_pack_items(pack_id,library_id,ref_name,query_hint,position) VALUES('p1','/kcb/clustara','main','GPU',0)`)
	return New(search.New(s), s)
}

func call(t *testing.T, s *Server, body string) map[string]any {
	return callAs(t, s, auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice"}, body)
}

func callAs(t *testing.T, s *Server, principal auth.Principal, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline chan time.Time
}

func (r *deadlineRecorder) SetWriteDeadline(value time.Time) error {
	r.deadline <- value
	return nil
}

func (r *deadlineRecorder) Flush() {}

func TestRepositorySearchAndAdministratorTools(t *testing.T) {
	s := fixture(t)
	s.SetEmbeddingHealthLoader(func(context.Context) string {
		return "- Requested Mode: hybrid-fallback\n- Operational Mode: keyword-only\n- Degraded Reason: embedding circuit is open"
	})
	found := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-repositories","arguments":{"query":"GPU","sourceType":"bitbucket"}}}`)
	text := found["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "/kcb/clustara") {
		t.Fatalf("repository search=%s", text)
	}
	admin := auth.Principal{
		UserID: "u1", Subject: "alice", ACLPrincipal: "alice", KeyID: "admin-key", KeyPrefix: "ADMIN1",
		Roles: []string{"source-admin"}, Scopes: []string{"get-platform-status", "list-index-jobs", "reindex-repository"},
	}
	list := callAs(t, s, admin, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("administrator tools=%#v", tools)
	}
	status := callAs(t, s, admin, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-platform-status","arguments":{}}}`)
	text = status["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Metadata Database: connected") || !strings.Contains(text, "Embedding Retrieval") || !strings.Contains(text, "embedding circuit is open") {
		t.Fatalf("platform status=%s", text)
	}
	queued := callAs(t, s, admin, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"reindex-repository","arguments":{"libraryId":"/kcb/clustara"}}}`)
	text = queued["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Reindex queued") {
		t.Fatalf("reindex=%s", text)
	}
	var jobs int
	if err := s.store.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs WHERE repository_id='r1' AND kind='manual' AND status='pending'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs=%d err=%v", jobs, err)
	}
	developer := admin
	developer.Roles = []string{"developer"}
	hidden := callAs(t, s, developer, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)
	if got := len(hidden["result"].(map[string]any)["tools"].([]any)); got != 0 {
		t.Fatalf("developer saw %d management tools", got)
	}
}

// A coding agent must be able to tell repository matches from file matches, and
// must never be left with a repository list when it asked about code.
func TestCodeSearchResponseGuidesTheClient(t *testing.T) {
	s := fixture(t)
	repositories := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-repositories","arguments":{"query":"GPU"}}}`)
	text := repositories["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "search-code") {
		t.Fatalf("a repository-only result must point at the code search tool: %s", text)
	}

	code := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU"}}}`)
	text = code["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, expected := range []string{"### Repository Matches", "### Source Matches", "/kcb/clustara"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("code search response is missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "Source Matches (0)") && !strings.Contains(text, "Next step") {
		t.Fatalf("an empty code result must state the next step: %s", text)
	}

	// The tool catalog must describe search-code as the primary entry point so a
	// client does not settle for repository names.
	var searchCode, searchRepositories string
	for _, tool := range Catalog() {
		switch tool["name"] {
		case "search-code":
			searchCode, _ = tool["description"].(string)
		case "search-repositories":
			searchRepositories, _ = tool["description"].(string)
		}
	}
	if !strings.Contains(searchCode, "file contents") || !strings.Contains(searchCode, "Primary code search") {
		t.Fatalf("search-code description=%q", searchCode)
	}
	if !strings.Contains(searchRepositories, "never returns file contents") {
		t.Fatalf("search-repositories description=%q", searchRepositories)
	}
}

// The file triad and history must work end to end through MCP, and initialize
// must teach the client which tool to reach for.
func TestFileTriadHistoryAndServerInstructions(t *testing.T) {
	s := fixture(t)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','main','docs/gpu.md','gpu.md',80,1,'4fa21bd')`)
	must(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','main','docs/runbooks/gpu.md','gpu.md',60,1,'4fa21bd')`)
	must(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','main','service.go','service.go',40,1,'4fa21bd')`)

	files := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find-file","arguments":{"pattern":"*.md"}}}`)
	text := files["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "docs/gpu.md") || !strings.Contains(text, "docs/runbooks/gpu.md") {
		t.Fatalf("find-file=%s", text)
	}

	read := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read-file","arguments":{"path":"docs/gpu.md"}}}`)
	text = read["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "DCGM exporter") || !strings.Contains(text, "Source: `") {
		t.Fatalf("read-file=%s", text)
	}

	listing := call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list-directory","arguments":{"path":"docs"}}}`)
	text = listing["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "runbooks/") || !strings.Contains(text, "gpu.md") {
		t.Fatalf("list-directory must show folders first and files after: %s", text)
	}

	// No source connector is configured in the fixture, so history reports that
	// instead of pretending the file has none.
	history := call(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get-file-history","arguments":{"path":"docs/gpu.md"}}}`)
	if history["result"].(map[string]any)["isError"] != true {
		t.Fatalf("history without a connector must report an error: %#v", history)
	}

	initialize := call(t, s, `{"jsonrpc":"2.0","id":5,"method":"initialize","params":{}}`)
	instructions, _ := initialize["result"].(map[string]any)["instructions"].(string)
	for _, expected := range []string{"search-code", "find-file", "read-file", "get-file-history", "Notes"} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("initialize instructions must mention %q: %q", expected, instructions)
		}
	}
}

func TestToolsListExtendedAndStrictCompatibility(t *testing.T) {
	s := fixture(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	// Assert the rule rather than a number: an anonymous caller sees every tool
	// that is not administrative. A hardcoded count only says that someone
	// added a tool, which is not what this is checking.
	expected, core := 0, 0
	for i := range registry {
		if len(registry[i].adminRoles) == 0 {
			expected++
		}
		if registry[i].core {
			core++
		}
	}
	if len(tools) != expected {
		t.Fatalf("got %d tools, want the %d non-administrative ones", len(tools), expected)
	}
	if tools[0].(map[string]any)["name"] != "resolve-library-id" || tools[1].(map[string]any)["name"] != "query-docs" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	s.SetStrictCompatibilityLoader(func(context.Context) bool { return true })
	out = call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools = out["result"].(map[string]any)["tools"].([]any)
	// Strict Context7 compatibility exposes only the two core tools, so adding
	// an extension must never change this.
	if len(tools) != core || core != 2 {
		t.Fatalf("strict mode got %d tools, want the %d core ones", len(tools), core)
	}
}

// An answer that overflows the agent's context is as useless as no answer, so
// every response is bounded, cut on a result boundary, and says what it left
// out. The diagnostics in Notes must survive the cut.
func TestResponseBudgetTruncatesOnResultBoundaryAndKeepsNotes(t *testing.T) {
	var b strings.Builder
	b.WriteString("## Source Matches\n")
	for index := 0; index < 40; index++ {
		fmt.Fprintf(&b, "\n### /kcb/clustara · internal/service_%02d.go\n\n%s\n", index, strings.Repeat("x", 500))
	}
	b.WriteString("\n### Notes\n- Repository /kcb/legacy is still indexing; answered live.\n")
	full := b.String()

	clamped := clampResponse(full, 8000)
	if len(clamped) > 8000 {
		t.Fatalf("clamped to %d bytes, over the 8000 budget", len(clamped))
	}
	if !strings.Contains(clamped, "still indexing") {
		t.Fatalf("the Notes section must survive truncation: %s", clamped)
	}
	if !strings.Contains(clamped, "### Truncated") || !strings.Contains(clamped, "of 40 result sections are included") {
		t.Fatalf("truncation must be stated with counts: %s", clamped)
	}
	// The cut lands between results, so the last one shown is whole.
	body := clamped[:strings.Index(clamped, "### Truncated")]
	if strings.Count(body, "### ") != strings.Count(body, ".go\n") {
		t.Fatalf("a result was cut in half: %s", body)
	}
	// Most of the budget has to be spent on content. A cut that keeps a header
	// and nothing else is technically within budget and useless.
	if len(clamped) < 6000 {
		t.Fatalf("the cut wasted the budget: %d of 8000 bytes used", len(clamped))
	}
	if unchanged := clampResponse(full, len(full)+1); unchanged != full {
		t.Fatal("an answer within budget must not be touched")
	}

	// The budget applies to a real call, and a caller may ask for less.
	s := fixture(t)
	if _, err := s.store.DB.Exec(`UPDATE mcp_tools SET max_response_bytes=2000 WHERE name='search-code'`); err != nil {
		t.Fatal(err)
	}
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","maxBytes":2500}}}`)
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if len(text) > 2000 {
		t.Fatalf("the configured budget must win over a larger maxBytes: %d bytes", len(text))
	}
	// Every tool advertises maxBytes, otherwise a strict client would refuse it.
	for _, tool := range Catalog() {
		schema := tool["inputSchema"].(map[string]any)
		if _, ok := schema["properties"].(map[string]any)["maxBytes"]; !ok {
			t.Fatalf("%v does not accept maxBytes", tool["name"])
		}
	}
}

func TestRepositoryMapAndSymbolTools(t *testing.T) {
	s := fixture(t)
	repositoryMap := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-repository-map","arguments":{"libraryId":"/kcb/clustara"}}}`)
	text := repositoryMap["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"go": 1`) || !strings.Contains(text, "Service.GetGPU") {
		t.Fatalf("repository map=%s", text)
	}
	found := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"find-symbol","arguments":{"libraryId":"/kcb/clustara","query":"GetGPU"}}}`)
	text = found["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Service.GetGPU") || !strings.Contains(text, "service.go#L20-L30") {
		t.Fatalf("symbol search=%s", text)
	}
	contextResult := call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-symbol-context","arguments":{"libraryId":"/kcb/clustara","symbol":"Service.GetGPU"}}}`)
	text = contextResult["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Returns GPU metrics") || !strings.Contains(text, "func (s *Service) GetGPU") {
		t.Fatalf("symbol context=%s", text)
	}
}

func TestDependencyAndChangeAnalysisTools(t *testing.T) {
	s := fixture(t)
	traced := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trace-dependencies","arguments":{"libraryId":"/kcb/clustara","ref":"release","symbol":"Service.GetGPU"}}}`)
	text := traced["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "HandleGPU") || !strings.Contains(text, "--call-->") {
		t.Fatalf("dependency trace=%s", text)
	}
	compared := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"compare-refs","arguments":{"libraryId":"/kcb/clustara","baseRef":"main","headRef":"release"}}}`)
	text = compared["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "MODIFIED `Service.GetGPU`") || !strings.Contains(text, "ADDED `HandleGPU`") {
		t.Fatalf("comparison=%s", text)
	}
	impact := call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-change-impact","arguments":{"libraryId":"/kcb/clustara","baseRef":"main","headRef":"release"}}}`)
	text = impact["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Potentially Affected") || !strings.Contains(text, "HandleGPU") {
		t.Fatalf("impact=%s", text)
	}
}

func TestContextPackRunbookAndExportTools(t *testing.T) {
	s := fixture(t)
	pack := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-context-pack","arguments":{"pack":"gpu-platform","query":"GPU metrics"}}}`)
	text := pack["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Context Pack: GPU Platform") || !strings.Contains(text, "/kcb/clustara") {
		t.Fatalf("context pack=%s", text)
	}
	runbook := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"find-runbook","arguments":{"query":"GPU metrics"}}}`)
	text = runbook["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "GPU Alert Runbook") || !strings.Contains(text, "Restart the DCGM") {
		t.Fatalf("runbook=%s", text)
	}
	exported := call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"export-context","arguments":{"libraryIds":["/kcb/clustara"],"query":"GPU metrics"}}}`)
	text = exported["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "untrusted reference data") || !strings.Contains(text, "/kcb/clustara") {
		t.Fatalf("export=%s", text)
	}
}

func TestSearchExplanationTool(t *testing.T) {
	s := fixture(t)
	result := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"explain-search-result","arguments":{"libraryId":"/kcb/clustara","query":"GPU metrics"}}}`)
	text := result["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Search Explanation") || !strings.Contains(text, "normalized query terms matched") || !strings.Contains(text, "docs/gpu.md") {
		t.Fatalf("explanation=%s", text)
	}
}

func TestInitializeNegotiatesProtocolAndSessionLifecycle(t *testing.T) {
	s := fixture(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "u1", ACLPrincipal: "alice"}))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("initialize=%d %s", rec.Code, rec.Body.String())
	}
	session := rec.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("session header missing")
	}
	var initialized map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initialized)
	if initialized["result"].(map[string]any)["protocolVersion"] != "2024-11-05" {
		t.Fatalf("response=%s", rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", session)
	s.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", session)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted session status=%d", rec.Code)
	}
}

func TestSessionIsValidAcrossServerInstances(t *testing.T) {
	first := fixture(t)
	second := New(search.New(first.store), first.store)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	first.ServeHTTP(rec, req)
	sessionID := rec.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("missing session")
	}
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)
	rec = httptest.NewRecorder()
	second.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second instance rejected shared session: %d %s", rec.Code, rec.Body.String())
	}
}

func TestACLChangeInvalidatesCachedSearch(t *testing.T) {
	s := fixture(t)
	_, _ = s.store.DB.Exec(`UPDATE mcp_tools SET cache_seconds=300 WHERE name='resolve-library-id'`)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"resolve-library-id","arguments":{"libraryName":"clustara","query":"GPU"}}}`
	first := call(t, s, body)
	if !strings.Contains(first["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "/kcb/clustara") {
		t.Fatal("fixture result missing")
	}
	_, _ = s.store.DB.Exec(`DELETE FROM repository_permissions WHERE repository_id='r1'`)
	second := call(t, s, body)
	if strings.Contains(second["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "/kcb/clustara") {
		t.Fatal("revoked repository was returned from cache")
	}
}

func TestSearchSettingChangeInvalidatesToolCache(t *testing.T) {
	s := fixture(t)
	_, _ = s.store.DB.Exec(`UPDATE mcp_tools SET cache_seconds=300 WHERE name='resolve-library-id'`)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"resolve-library-id","arguments":{"libraryName":"clustara","query":"GPU"}}}`
	first := call(t, s, body)
	firstText := first["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(firstText, "Name: Clustara") {
		t.Fatalf("fixture result=%s", firstText)
	}
	_, _ = s.store.DB.Exec(`UPDATE repositories SET name='Clustara Changed' WHERE id='r1'`)
	cached := call(t, s, body)
	cachedText := cached["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(cachedText, "Clustara Changed") {
		t.Fatal("expected the unchanged settings revision to use the cached response")
	}
	_, err := s.store.DB.Exec(`INSERT INTO system_settings(category,version,value_encrypted,updated_by) VALUES('search',1,X'01','admin')`)
	if err != nil {
		t.Fatal(err)
	}
	changed := call(t, s, body)
	changedText := changed["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(changedText, "Clustara Changed") {
		t.Fatalf("settings revision did not invalidate the cache: %s", changedText)
	}
}

func TestStreamableHTTPGETKeepsSSEOpenUntilSessionDelete(t *testing.T) {
	s := fixture(t)
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	initialize, err := http.Post(httpServer.URL, "application/json", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, initialize.Body)
	initialize.Body.Close()
	sessionID := initialize.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("missing session id")
	}
	request, _ := http.NewRequest(http.MethodGet, httpServer.URL, nil)
	request.Header.Set("Mcp-Session-Id", sessionID)
	request.Header.Set("Accept", "text/event-stream")
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") || stream.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status=%d headers=%v", stream.StatusCode, stream.Header)
	}
	line, err := bufio.NewReader(stream.Body).ReadString('\n')
	if err != nil || line != ": git-ctx stream ready\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	deleteRequest, _ := http.NewRequest(http.MethodDelete, httpServer.URL, nil)
	deleteRequest.Header.Set("Mcp-Session-Id", sessionID)
	deleted, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleted.Body.Close()
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("delete=%d", deleted.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, httpServer.URL, nil)
	request.Header.Set("Mcp-Session-Id", sessionID)
	request.Header.Set("Accept", "text/event-stream")
	rejected, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	rejected.Body.Close()
	if rejected.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted session GET=%d", rejected.StatusCode)
	}
}

func TestStreamableHTTPGETClearsServerWriteDeadline(t *testing.T) {
	s := fixture(t)
	initialize := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	initialize.Header.Set("Content-Type", "application/json")
	initialized := httptest.NewRecorder()
	s.ServeHTTP(initialized, initialize)
	sessionID := initialized.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("missing session id")
	}

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/mcp", nil).WithContext(ctx)
	request.Header.Set("Mcp-Session-Id", sessionID)
	request.Header.Set("Accept", "text/event-stream")
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), deadline: make(chan time.Time, 1)}
	served := make(chan struct{})
	go func() {
		defer close(served)
		s.ServeHTTP(recorder, request)
	}()

	select {
	case deadline := <-recorder.deadline:
		if !deadline.IsZero() {
			t.Fatalf("SSE write deadline=%v, want zero", deadline)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not clear the write deadline")
	}
	cancel()
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after cancellation")
	}
}

func TestResolveThenQuery(t *testing.T) {
	s := fixture(t)
	resolve := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"resolve-library-id","arguments":{"libraryName":"clustara","query":"GPU metrics"}}}`)
	text := resolve["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !bytes.Contains([]byte(text), []byte("/kcb/clustara")) {
		t.Fatalf("resolve output: %s", text)
	}
	query := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query-docs","arguments":{"libraryId":"/kcb/clustara/main","query":"GPU metrics"}}}`)
	text = query["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !bytes.Contains([]byte(text), []byte("bitbucket://kcb/clustara@4fa21bd/docs/gpu.md#L20-L30")) {
		t.Fatalf("query output: %s", text)
	}
}

func TestACLDoesNotRevealRepository(t *testing.T) {
	s := fixture(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query-docs","arguments":{"libraryId":"/kcb/clustara","query":"GPU"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "u2", Subject: "mallory", ACLPrincipal: "mallory"}))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if bytes.Contains(bytes.ToLower(rec.Body.Bytes()), []byte("clustara")) {
		t.Fatalf("repository existence leaked: %s", rec.Body.String())
	}
}

func TestAPIKeyToolAndRepositoryRestrictions(t *testing.T) {
	s := fixture(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query-docs","arguments":{"libraryId":"/kcb/clustara","query":"GPU"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice", KeyID: "key", Scopes: []string{"resolve-library-id"}, AllowedRepositories: []string{"/kcb/clustara"}}))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if !bytes.Contains(rec.Body.Bytes(), []byte("unavailable for this credential")) {
		t.Fatalf("tool restriction not enforced: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice", KeyID: "key", Scopes: []string{"query-docs"}, AllowedRepositories: []string{"/other/repo"}}))
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if bytes.Contains(bytes.ToLower(rec.Body.Bytes()), []byte("clustara")) {
		t.Fatalf("repository restriction leaked library: %s", rec.Body.String())
	}
}

// The per-stage trace is what turns "the answer was empty" into "the ACL saw
// two repositories and the index matched nothing in them". It must be written
// for every call, ordered, and attributed to that call only.
func TestCallTraceRecordsEveryStage(t *testing.T) {
	s := fixture(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU"}}}`)
	if out["result"] == nil {
		t.Fatalf("call failed: %#v", out)
	}
	var callID, tool, summary string
	if err := s.store.DB.QueryRow(`SELECT id,tool,trace_summary FROM mcp_calls ORDER BY id DESC LIMIT 1`).Scan(&callID, &tool, &summary); err != nil {
		t.Fatal(err)
	}
	if tool != "search-code" {
		t.Fatalf("tool=%s", tool)
	}
	rows, err := s.store.DB.Query(`SELECT sequence,stage,status,candidates,results FROM mcp_call_steps WHERE call_id=? ORDER BY sequence`, callID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stages := map[string]bool{}
	previous := 0
	for rows.Next() {
		var sequence, candidates, results int
		var stage, status string
		if err = rows.Scan(&sequence, &stage, &status, &candidates, &results); err != nil {
			t.Fatal(err)
		}
		if sequence != previous+1 {
			t.Fatalf("steps must be ordered without gaps: got %d after %d", sequence, previous)
		}
		previous = sequence
		if status == "" {
			t.Fatalf("stage %s has no status", stage)
		}
		stages[stage] = true
	}
	for _, expected := range []string{"acl", "index-repositories", "source-query", "acl-candidates"} {
		if !stages[expected] {
			t.Fatalf("stage %q was not traced; recorded %v", expected, stages)
		}
	}

	// A second call keeps its own trace rather than appending to the first.
	call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"find-file","arguments":{"pattern":"*.md"}}}`)
	var traces int
	if err := s.store.DB.QueryRow(`SELECT COUNT(DISTINCT call_id) FROM mcp_call_steps`).Scan(&traces); err != nil || traces != 2 {
		t.Fatalf("distinct traces=%d err=%v", traces, err)
	}
}

// Korean is the normal case here, and every stored preview, detail and error
// message is length-limited. A byte-wise cut would split a three-byte character
// and write invalid UTF-8 into the audit trail.
func TestTruncationKeepsTextValid(t *testing.T) {
	korean := "실패한 웹훅을 다시 보내는 코드를 찾아 주세요. 결제 서비스의 재시도 정책이 어디에 있는지 알고 싶습니다."
	for _, limit := range []int{1, 7, 20, 61, 100, 151} {
		for name, got := range map[string]string{"clip": clip(korean, limit), "truncate": truncate(korean, limit)} {
			if !utf8.ValidString(got) {
				t.Fatalf("%s(%d) produced invalid UTF-8: %q", name, limit, got)
			}
			if len(got) > limit+len("…") {
				t.Fatalf("%s(%d) returned %d bytes", name, limit, len(got))
			}
		}
	}
	preview, hash := argumentDigest("search-code", map[string]any{"query": korean})
	if !utf8.ValidString(preview) || hash == "" {
		t.Fatalf("preview=%q hash=%q", preview, hash)
	}

	// A clamped answer must also stay valid, including the pathological case
	// where a single unbroken line has to be cut mid-way.
	dense := "## 결과\n" + strings.Repeat("가나다라마바사아자차카타파하", 400)
	clamped := clampResponse(dense, 3000)
	if !utf8.ValidString(clamped) {
		t.Fatal("clampResponse produced invalid UTF-8")
	}
	if len(clamped) > 3000 {
		t.Fatalf("clamped=%d bytes", len(clamped))
	}
}

// The dispatcher measured how large an answer was before the budget cut it and
// then recorded only whether a cut happened. An operator could see that a tool
// truncates often but not whether those calls lost five percent or eighty --
// the difference between a budget that is fine and a tool that is unusable at
// it, and the number that says whether compressing answers is worth building.
func TestTruncatedCallsRecordWhatTheyDiscarded(t *testing.T) {
	s := fixture(t)
	// A budget small enough to force a cut on a tool that returns a list.
	if _, err := s.store.DB.Exec(`INSERT INTO mcp_tools(name,enabled,max_response_bytes) VALUES('search-code',1,?)
ON CONFLICT(name) DO UPDATE SET max_response_bytes=excluded.max_response_bytes`, MinResponseBytes); err != nil {
		t.Fatalf("set the per-tool budget: %v", err)
	}
	call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"clustara"}}}`)

	var produced, sent, truncated int64
	err := s.store.DB.QueryRow(`SELECT COALESCE(produced_bytes,0),COALESCE(response_bytes,0),COALESCE(truncated,0)
FROM mcp_calls WHERE tool='search-code' ORDER BY occurred_at DESC LIMIT 1`).Scan(&produced, &sent, &truncated)
	if err != nil {
		t.Fatalf("read the call record: %v", err)
	}
	if produced <= 0 {
		t.Fatalf("produced_bytes = %d; the size before the cut was not recorded", produced)
	}
	if truncated == 1 && produced <= sent {
		t.Errorf("a truncated call recorded produced=%d sent=%d; the discarded amount is not derivable", produced, sent)
	}
	if truncated == 0 && produced != sent {
		t.Errorf("an untruncated call recorded produced=%d sent=%d, which should be equal", produced, sent)
	}
}

// An advisory asks "who is on the affected version", which no import graph can
// answer. The tool must group by version and must never report a repository the
// key is not allowed to see, including inside that grouping.
func TestFindDependencyUsageGroupsVersionsAndHonoursKeyRestrictions(t *testing.T) {
	s := fixture(t)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO repositories(id,project_key,slug,name,description,library_id,default_branch) VALUES('r2','KCB','billing','Billing','Billing service','/kcb/billing','main')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r2','alice','read')`)
	add := func(repository, name, version, path string) {
		t.Helper()
		must(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES(?,'main','maven',?,?,?,'direct',?,'4fa21bd')`,
			repository, name, strings.ToLower(name), version, path)
	}
	add("r1", "org.apache.logging.log4j:log4j-core", "2.14.1", "pom.xml")
	add("r2", "org.apache.logging.log4j:log4j-core", "2.17.1", "pom.xml")

	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find-dependency-usage","arguments":{"name":"org.apache.logging.log4j:log4j-core"}}}`)
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, expected := range []string{"2.14.1", "2.17.1", "/kcb/clustara", "/kcb/billing", "pom.xml"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("response is missing %q:\n%s", expected, text)
		}
	}

	// A key restricted to one repository must not learn that the other one exists.
	restricted := auth.Principal{
		UserID: "u1", Subject: "alice", ACLPrincipal: "alice", KeyID: "k1", KeyPrefix: "KEY123",
		Scopes: []string{"find-dependency-usage"}, AllowedRepositories: []string{"/kcb/billing"},
	}
	scoped := callAs(t, s, restricted, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"find-dependency-usage","arguments":{"name":"org.apache.logging.log4j:log4j-core"}}}`)
	text = scoped["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(text, "/kcb/clustara") || strings.Contains(text, "2.14.1") {
		t.Fatalf("a restricted key saw another repository's inventory:\n%s", text)
	}
	if !strings.Contains(text, "Repositories: 1") {
		t.Fatalf("the summary must be recounted after the restriction:\n%s", text)
	}
}

// An API key's repository restriction narrows access below the user's own ACL,
// and every other tool honours it. A context pack bundles several repositories,
// so a pack is exactly where an unfiltered read hands a restricted key the
// content it was restricted away from.
func TestContextPackHonoursKeyRepositoryRestriction(t *testing.T) {
	s := fixture(t)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO repositories(id,project_key,slug,name,description,library_id,default_branch) VALUES('r3','KCB','payments','Payments','Payment service','/kcb/payments','main')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r3','alice','read')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('p1','r3','main','9ab','settlement.go',1,20,'Settlement','code','func Settle() { /* GPU billing secret sauce */ }','ph1')`)
	must(`INSERT INTO context_pack_items(pack_id,library_id,ref_name,query_hint,position) VALUES('p1','/kcb/payments','main','GPU',1)`)

	// Without a key restriction the pack spans both repositories.
	full := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-context-pack","arguments":{"pack":"gpu-platform","query":"GPU"}}}`)
	text := full["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "/kcb/payments") && !strings.Contains(text, "settlement.go") {
		t.Skipf("pack fixture did not include the second repository: %s", text)
	}

	restricted := auth.Principal{
		UserID: "u1", Subject: "alice", ACLPrincipal: "alice", KeyID: "k1", KeyPrefix: "KEY123",
		Scopes: []string{"get-context-pack"}, AllowedRepositories: []string{"/kcb/clustara"},
	}
	scoped := callAs(t, s, restricted, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get-context-pack","arguments":{"pack":"gpu-platform","query":"GPU"}}}`)
	text = scoped["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(text, "/kcb/payments") || strings.Contains(text, "settlement.go") || strings.Contains(text, "billing secret sauce") {
		t.Fatalf("a restricted key received content from a repository outside its allowlist:\n%s", text)
	}
	if !strings.Contains(text, "clustara") && !strings.Contains(text, "GPU") {
		t.Fatalf("the allowed repository must still be returned:\n%s", text)
	}
}

// A key's repository allowlist is enforced tool by tool, which is exactly the
// kind of rule a new tool forgets. This guard calls every non-administrative
// tool with a restricted key against a repository holding a distinctive marker
// and fails if the marker comes back — and it fails just as loudly when a tool
// is missing from the table, so adding a tool forces a decision about it.
func TestNoToolLeaksOutsideTheKeyAllowlist(t *testing.T) {
	s := fixture(t)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const marker = "ZZTOPSECRETMARKER"
	must(`INSERT INTO repositories(id,project_key,slug,name,description,library_id,default_branch) VALUES('r9','KCB','vault','Vault','GPU ` + marker + `','/kcb/vault','main')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r9','alice','read')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('z1','r9','main','9ab','` + marker + `.go',1,9,'GPU ` + marker + `','code','func GPU() { // ` + marker + ` }','zh1')`)
	must(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r9','main','` + marker + `.go','` + marker + `.go',20,1,'9ab')`)
	must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash) VALUES('zs1','r9','main','9ab','` + marker + `.go','GPU','GPU','function','go','func GPU()','` + marker + `',1,9,'zsh1')`)
	must(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES('zd1','r9','main','9ab','` + marker + `.go','GPU','Service.GetGPU','call',3)`)
	must(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES('r9','main','npm','gpu-lib','gpu-lib','1.0.0','direct','` + marker + `/package.json','9ab')`)
	must(`INSERT INTO repository_maps(repository_id,ref_name,commit_id,summary_json) VALUES('r9','main','9ab','{"languages":{"go":1},"keyFiles":["` + marker + `.go"]}')`)
	must(`INSERT INTO context_pack_items(pack_id,library_id,ref_name,query_hint,position) VALUES('p1','/kcb/vault','main','GPU',2)`)
	must(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('r9','main','9ab')`)

	// Every non-administrative tool, with arguments that would surface the marked
	// repository if the allowlist were ignored.
	calls := map[string]string{
		"resolve-library-id":    `{"libraryName":"vault","query":"GPU"}`,
		"query-docs":            `{"libraryId":"/kcb/vault","query":"GPU"}`,
		"search-repositories":   `{"query":"GPU"}`,
		"search-source":         `{"query":"GPU"}`,
		"search-code":           `{"query":"GPU"}`,
		"find-file":             `{"pattern":"*.go"}`,
		"read-file":             `{"libraryId":"/kcb/vault","path":"` + marker + `.go"}`,
		"search-semantic":       `{"query":"GPU"}`,
		"find-dependents":       `{"target":"Service.GetGPU"}`,
		"search-merge-requests": `{"query":"GPU"}`,
		"get-file-history":      `{"libraryId":"/kcb/vault","path":"` + marker + `.go"}`,
		"list-directory":        `{"libraryId":"/kcb/vault","path":""}`,
		"get-repository-map":    `{"libraryId":"/kcb/vault"}`,
		"find-symbol":           `{"query":"GPU"}`,
		"get-symbol-context":    `{"libraryId":"/kcb/vault","symbol":"GPU"}`,
		"trace-dependencies":    `{"libraryId":"/kcb/vault","symbol":"GPU"}`,
		"compare-refs":          `{"libraryId":"/kcb/vault","baseRef":"main","headRef":"main"}`,
		"get-change-impact":     `{"libraryId":"/kcb/vault","baseRef":"main","headRef":"main"}`,
		"get-context-pack":      `{"pack":"gpu-platform","query":"GPU"}`,
		"find-runbook":          `{"query":"GPU"}`,
		"export-context":        `{"libraryIds":["/kcb/vault"],"query":"GPU"}`,
		"explain-search-result": `{"libraryId":"/kcb/vault","query":"GPU"}`,
		"build-context":         `{"task":"GPU"}`,
		"find-code-owner":       `{"path":"` + marker + `.go"}`,
		"find-tests":            `{"symbol":"GPU"}`,
		"find-dependency-usage": `{"name":"gpu-lib"}`,
		"get-architecture-map":  `{}`,
		"assess-change-risk":    `{"libraryId":"/kcb/vault","paths":["` + marker + `.go"]}`,
		"get-repository-health": `{}`,
	}
	restricted := auth.Principal{
		UserID: "u1", Subject: "alice", ACLPrincipal: "alice", KeyID: "k1", KeyPrefix: "KEY123",
		AllowedRepositories: []string{"/kcb/clustara"},
	}
	for index := range registry {
		entry := &registry[index]
		if len(entry.adminRoles) > 0 {
			continue
		}
		arguments, ok := calls[entry.name]
		if !ok {
			t.Fatalf("tool %q is not covered by the allowlist leak guard; add it with arguments that would surface a foreign repository", entry.name)
		}
		restricted.Scopes = []string{entry.name}
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, entry.name, arguments)
		out := callAs(t, s, restricted, body)
		result, ok := out["result"].(map[string]any)
		if !ok {
			continue // an error answer cannot leak content
		}
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		if strings.Contains(text, marker) {
			t.Fatalf("%s returned content from a repository outside the key allowlist:\n%s", entry.name, text)
		}
	}
}

// The libraries a repository already uses decide how new code in it should be
// written. An agent orienting itself gets them with the map rather than having
// to know to ask a second question.
func TestRepositoryMapCarriesTheStack(t *testing.T) {
	s := fixture(t)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	add := func(name, version, scope, path string) {
		must(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES('r1','main','go',?,?,?,?,?,'4fa21bd')`,
			name, strings.ToLower(name), version, scope, path)
	}
	add("github.com/gin-gonic/gin", "v1.10.0", "direct", "go.mod")
	add("github.com/stretchr/testify", "v1.9.0", "direct", "go.mod")
	add("golang.org/x/sync", "v0.10.0", "transitive", "go.mod")
	// The same package resolved in the lock file: it must not appear twice.
	add("github.com/gin-gonic/gin", "v1.10.0", "resolved", "go.sum")

	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-repository-map","arguments":{"libraryId":"/kcb/clustara"}}}`)
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Stack (2 direct dependencies)") {
		t.Fatalf("the stack must be counted from direct declarations only:\n%s", text)
	}
	if !strings.Contains(text, "github.com/gin-gonic/gin") || !strings.Contains(text, "v1.10.0") {
		t.Fatalf("a direct dependency is missing:\n%s", text)
	}
	// A transitive package and a lock-file entry are noise in an orientation.
	if strings.Contains(text, "golang.org/x/sync") {
		t.Fatalf("a transitive dependency must not be listed as the stack:\n%s", text)
	}
	if strings.Count(text, "github.com/gin-gonic/gin") != 1 {
		t.Fatalf("the resolved copy must not duplicate the declaration:\n%s", text)
	}

	// A repository with no inventory yet keeps the previous output shape. The
	// tool cache is cleared first: this asserts the rendering, not the cache.
	must(`DELETE FROM repository_packages WHERE repository_id='r1'`)
	s.cacheMu.Lock()
	s.cache = map[string]cacheEntry{}
	s.cacheMu.Unlock()
	bare := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get-repository-map","arguments":{"libraryId":"/kcb/clustara"}}}`)
	text = bare["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(text, "Stack (") {
		t.Fatalf("an uninventoried repository must not claim an empty stack:\n%s", text)
	}
}

// A credential reaching for a tool it was never granted is the first thing a
// security review looks for, and it used to leave no trace at all: the refusal
// was answered and forgotten. The address recorded with it has to be the one
// the HTTP layer resolved, without the ephemeral port, or it cannot be matched
// against the CIDR restrictions the same key is checked against.
func TestARefusedToolCallIsAudited(t *testing.T) {
	s := fixture(t)
	restricted := auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice",
		KeyID: "k1", KeyPrefix: "ABCDEF", Scopes: []string{"search-code"}}
	request := httptest.NewRequest(http.MethodPost, "/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read-file","arguments":{"libraryId":"/kcb/clustara","path":"service.go"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.7:51234"
	request = request.WithContext(auth.WithClientIP(auth.WithPrincipal(request.Context(), restricted), "203.0.113.7"))
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	if !bytes.Contains(recorder.Body.Bytes(), []byte("unavailable for this credential")) {
		t.Fatalf("the caller must still be told: %s", recorder.Body.String())
	}

	var tool, outcome, code, prefix, ip string
	if err := s.store.DB.QueryRow(`SELECT tool,outcome,error_code,api_key_prefix,client_ip FROM mcp_calls ORDER BY occurred_at DESC LIMIT 1`).
		Scan(&tool, &outcome, &code, &prefix, &ip); err != nil {
		t.Fatalf("the refusal was not audited at all: %v", err)
	}
	if tool != "read-file" || code != "tool_not_permitted" || prefix != "ABCDEF" {
		t.Fatalf("audit row=%s/%s/%s/%s", tool, outcome, code, prefix)
	}
	if ip != "203.0.113.7" {
		t.Fatalf("the audited address must be the resolved one, without its port: %q", ip)
	}

	// A permitted call records the same resolved address rather than re-deriving
	// one from the connection.
	allowed := auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice",
		KeyID: "k1", KeyPrefix: "ABCDEF", Scopes: []string{"search-code"}}
	ok := httptest.NewRequest(http.MethodPost, "/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU"}}}`))
	ok.Header.Set("Content-Type", "application/json")
	ok.RemoteAddr = "203.0.113.7:51999"
	// A forwarding header that the HTTP layer did not believe must not reach the
	// audit trail: otherwise any caller writes its own address into it.
	ok.Header.Set("X-Forwarded-For", "198.51.100.99")
	ok = ok.WithContext(auth.WithClientIP(auth.WithPrincipal(ok.Context(), allowed), "203.0.113.7"))
	s.ServeHTTP(httptest.NewRecorder(), ok)
	if err := s.store.DB.QueryRow(`SELECT tool,client_ip FROM mcp_calls ORDER BY occurred_at DESC LIMIT 1`).Scan(&tool, &ip); err != nil {
		t.Fatal(err)
	}
	if tool != "search-code" || ip != "203.0.113.7" {
		t.Fatalf("permitted call recorded %s from %q", tool, ip)
	}
}

// Alerts that never arrive are the failure an operator is least likely to
// notice, because the thing that would have told them is the thing that broke.
// Asking an agent for platform status has to surface it.
func TestPlatformStatusReportsUndeliveredAlerts(t *testing.T) {
	s := fixture(t)
	admin := auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice",
		KeyID: "admin-key", KeyPrefix: "ADMIN1",
		Roles: []string{"source-admin"}, Scopes: []string{"get-platform-status"}}
	statusText := func() string {
		t.Helper()
		s.cache = map[string]cacheEntry{}
		out := callAs(t, s, admin, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-platform-status","arguments":{}}}`)
		return out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	if text := statusText(); !strings.Contains(text, "Notifications: no failed deliveries") {
		t.Fatalf("a healthy platform must say so:\n%s", text)
	}

	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES('n1','u1','api_key_expiring','k1','MCP API key expires soon','곧 만료됩니다.')`)
	must(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,status,attempts,next_attempt_at) VALUES('d1','n1','webhook','hash','failed',1,CURRENT_TIMESTAMP)`)
	if text := statusText(); !strings.Contains(text, "1 delivery(ies) failed and are being retried") {
		t.Fatalf("a retrying delivery must be reported:\n%s", text)
	}

	// A delivery that gave up is the one that needs an instruction, not a count.
	must(`UPDATE notification_deliveries SET status='dead' WHERE id='d1'`)
	text := statusText()
	if !strings.Contains(text, "gave up after their retries") || !strings.Contains(text, "Alerts are not reaching their destination") {
		t.Fatalf("a dead delivery must say what it means:\n%s", text)
	}
}

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestRepositorySearchAndAdministratorTools(t *testing.T) {
	s := fixture(t)
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
	if !strings.Contains(text, "Metadata Database: connected") {
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

func TestToolsListExtendedAndStrictCompatibility(t *testing.T) {
	s := fixture(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 17 {
		t.Fatalf("got %d tools", len(tools))
	}
	if tools[0].(map[string]any)["name"] != "resolve-library-id" || tools[1].(map[string]any)["name"] != "query-docs" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	s.SetStrictCompatibilityLoader(func(context.Context) bool { return true })
	out = call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools = out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("strict mode got %d tools", len(tools))
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

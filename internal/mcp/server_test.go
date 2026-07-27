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

func TestToolsListExtendedAndStrictCompatibility(t *testing.T) {
	s := fixture(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
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

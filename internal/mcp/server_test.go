package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice"}))
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

func TestToolsListIsContext7Compatible(t *testing.T) {
	out := call(t, fixture(t), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("got %d tools", len(tools))
	}
	if tools[0].(map[string]any)["name"] != "resolve-library-id" || tools[1].(map[string]any)["name"] != "query-docs" {
		t.Fatalf("unexpected tools: %#v", tools)
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
	if !bytes.Contains(rec.Body.Bytes(), []byte("not allowed to call")) {
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

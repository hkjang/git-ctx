package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git-ctx/internal/config"
	"git-ctx/internal/embedding"
)

func mcpFixture(t *testing.T) (*App, string) {
	t.Helper()
	a, err := New(context.Background(), config.Config{ListenAddress: ":0", DatabaseDriver: "sqlite", DatabaseDSN: "file:mcp-integration?mode=memory&cache=shared&_foreign_keys=on&_busy_timeout=5000", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "https://git-ctx.company"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := a.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO users(id,subject,username,email) VALUES('u1','kc-alice','alice','alice@example.test')`)
	must(`INSERT INTO user_identities(user_id,bitbucket_user_slug,mapping_source) VALUES('u1','alice.bb','test')`)
	must(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,reputation) VALUES('bitbucket:1','KCB','demo','Demo','GPU platform','bitbucket','1','/kcb/demo','main','High')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('bitbucket:1','alice.bb','read')`)
	vector := embedding.Encode(embedding.Embed("GPU Guide Pod DCGM exporter"))
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c1','bitbucket:1','main','abc123','docs/gpu.md',1,8,'GPU Guide','document','Pod GPU metrics use DCGM exporter.','hash',?)`, vector)
	_, raw, err := a.keys.Create(context.Background(), "u1", "integration", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, raw
}
func mcpRequest(a *App, key, session, body, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("CONTEXT7_API_KEY", key)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}
func TestMCPHTTPContractAndConcurrentCalls(t *testing.T) {
	a, key := mcpFixture(t)
	init := mcpRequest(a, key, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"contract","version":"1"}}}`, "")
	if init.Code != 200 {
		t.Fatalf("initialize=%d %s", init.Code, init.Body.String())
	}
	session := init.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("missing session ID")
	}
	list := mcpRequest(a, key, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "resolve-library-id" || listed.Result.Tools[1].Name != "query-docs" {
		t.Fatalf("tools=%s", list.Body.String())
	}
	adminReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/mcp/tools/resolve-library-id", bytes.NewBufferString(`{"enabled":false,"timeoutMs":1000,"cacheSeconds":0}`))
	adminReq.Header.Set("Authorization", "Bearer bootstrap")
	adminReq.Header.Set("Content-Type", "application/json")
	adminRec := httptest.NewRecorder()
	a.Handler().ServeHTTP(adminRec, adminReq)
	if adminRec.Code != 200 {
		t.Fatalf("disable tool=%d %s", adminRec.Code, adminRec.Body.String())
	}
	list = mcpRequest(a, key, session, `{"jsonrpc":"2.0","id":20,"method":"tools/list"}`, "")
	listed.Result.Tools = nil
	_ = json.Unmarshal(list.Body.Bytes(), &listed)
	if len(listed.Result.Tools) != 1 || listed.Result.Tools[0].Name != "query-docs" {
		t.Fatalf("disabled catalog=%s", list.Body.String())
	}
	adminReq = httptest.NewRequest(http.MethodPut, "/api/v1/admin/mcp/tools/resolve-library-id", bytes.NewBufferString(`{"enabled":true,"timeoutMs":30000,"cacheSeconds":0}`))
	adminReq.Header.Set("Authorization", "Bearer bootstrap")
	adminReq.Header.Set("Content-Type", "application/json")
	a.Handler().ServeHTTP(httptest.NewRecorder(), adminReq)
	resolve := mcpRequest(a, key, session, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"resolve-library-id","arguments":{"libraryName":"demo","query":"GPU metrics"}}}`, "")
	if !strings.Contains(resolve.Body.String(), "/kcb/demo") {
		t.Fatalf("resolve=%s", resolve.Body.String())
	}
	queryBody := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"query-docs","arguments":{"libraryId":"/kcb/demo/main","query":"Pod GPU metrics"}}}`
	query := mcpRequest(a, key, session, queryBody, "")
	if !strings.Contains(query.Body.String(), "bitbucket://kcb/demo@abc123/docs/gpu.md#L1-L8") {
		t.Fatalf("query=%s", query.Body.String())
	}
	rejected := mcpRequest(a, key, "", queryBody, "https://evil.example")
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("origin status=%d", rejected.Code)
	}

	const calls = 50
	var wg sync.WaitGroup
	errorsCh := make(chan error, calls)
	for n := 0; n < calls; n++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			body := strings.Replace(queryBody, `"id":4`, fmt.Sprintf(`"id":%d`, 100+id), 1)
			rec := mcpRequest(a, key, "", body, "")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"isError":false`) {
				errorsCh <- fmt.Errorf("call %d: status=%d body=%s", id, rec.Code, rec.Body.String())
			}
		}(n)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	var recorded int
	if err := a.store.DB.QueryRow(`SELECT COUNT(*) FROM mcp_calls WHERE user_id='u1' AND tool='query-docs'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != calls+1 {
		t.Fatalf("recorded=%d want=%d", recorded, calls+1)
	}
}

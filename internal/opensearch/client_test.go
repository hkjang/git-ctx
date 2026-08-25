package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

func TestHTTPErrorCarriesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("cluster is restarting"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Validate(context.Background())
	if source.StatusOf(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "opensearch returned HTTP 503: cluster is restarting") {
		t.Fatalf("status=%d err=%v", source.StatusOf(err), err)
	}
}

func TestValidateSyncAndACLSearch(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests[r.Method+" "+r.URL.Path] = string(body)
		mu.Unlock()
		if r.Header.Get("Authorization") != "ApiKey test-key" {
			http.Error(w, "missing authentication", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			io.WriteString(w, `{"version":{"number":"2.19.0"}}`)
		case r.Method == http.MethodHead && r.URL.Path == "/chunks":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/chunks":
			io.WriteString(w, `{"acknowledged":true}`)
		case strings.HasSuffix(r.URL.Path, "/_delete_by_query"):
			io.WriteString(w, `{"deleted":0}`)
		case r.URL.Path == "/_bulk":
			io.WriteString(w, `{"errors":false,"items":[]}`)
		case strings.HasSuffix(r.URL.Path, "/_search"):
			io.WriteString(w, `{"hits":{"hits":[{"_id":"c1","_score":4.2}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db, err := store.Open(context.Background(), "sqlite", "file:opensearch-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	must := func(query string, args ...any) {
		if _, e := db.DB.Exec(query, args...); e != nil {
			t.Fatal(e)
		}
	}
	must(`INSERT INTO repositories(id,source_type,source_external_id,project_key,slug,name,library_id,default_branch) VALUES('r1','gitlab','1','P','r','Repo','/p/r','main')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','alice','REPO_READ')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','group:dev','developer')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','r1','main','abc','docs/a.md',1,3,'GPU API','document','safe indexed text','h')`)

	client, err := New(Config{BaseURL: server.URL, Index: "chunks", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = client.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = client.SyncRef(ctx, db, "r1", "main"); err != nil {
		t.Fatal(err)
	}
	hits, err := client.Search(ctx, "r1", "main", []string{"alice"}, "GPU", 10)
	if err != nil || len(hits) != 1 || hits[0].ID != "c1" {
		t.Fatalf("unexpected search: %#v %v", hits, err)
	}

	mu.Lock()
	bulk := requests["POST /_bulk"]
	searchBody := requests["POST /chunks/_search"]
	mu.Unlock()
	if !strings.Contains(bulk, `"principals":["alice","group:dev"]`) || !strings.Contains(bulk, "safe indexed text") {
		t.Fatalf("bulk projection is missing ACL or content: %s", bulk)
	}
	var query map[string]any
	if err = json.Unmarshal([]byte(searchBody), &query); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(searchBody, `"principals":["alice","*"]`) || !strings.Contains(searchBody, `"repository_id":"r1"`) || !strings.Contains(searchBody, `"ref_name":"main"`) {
		t.Fatalf("ACL filters must be in the OpenSearch query: %s", searchBody)
	}
}

func TestSearchWithoutPrincipalsDoesNotCallServer(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := client.Search(context.Background(), "r", "main", nil, "secret", 10)
	if err != nil || len(hits) != 0 || calls != 0 {
		t.Fatalf("fail-closed search made a request: hits=%v calls=%d err=%v", hits, calls, err)
	}
}

func TestIncrementalProjectionOnlyDeletesAndUpsertsChangedChunks(t *testing.T) {
	var deleteQueries int
	var bulks []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/chunks":
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/_delete_by_query"):
			deleteQueries++
			io.WriteString(w, `{"deleted":0}`)
		case r.URL.Path == "/_bulk":
			bulks = append(bulks, string(body))
			io.WriteString(w, `{"errors":false,"items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	db, err := store.Open(context.Background(), "sqlite", "file:opensearch-incremental?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	must := func(query string, args ...any) {
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO repositories(id,project_key,slug,name,library_id) VALUES('r1','P','r','Repo','/p/r')`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','alice','read')`)
	must(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('r1','main','c1')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES
('old','r1','main','c1','docs/a.md',1,2,'A','document','old','h1'),
('keep','r1','main','c1','docs/keep.md',1,2,'Keep','document','same','h2')`)
	client, err := New(Config{BaseURL: server.URL, Index: "chunks"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = client.SyncRef(ctx, db, "r1", "main"); err != nil {
		t.Fatal(err)
	}
	must(`DELETE FROM document_chunks WHERE id='old'`)
	must(`UPDATE document_chunks SET commit_id='c2' WHERE id='keep'`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('new','r1','main','c2','docs/a.md',1,2,'A','document','new','h3')`)
	must(`UPDATE repository_ref_states SET commit_id='c2' WHERE repository_id='r1' AND ref_name='main'`)
	must(`INSERT INTO repository_ref_changes(repository_id,ref_name,commit_id,previous_commit_id,file_path,action,deleted_chunk_ids) VALUES
('r1','main','c2','c1','docs/a.md','delete','["old"]'),
('r1','main','c2','c1','docs/a.md','upsert','[]')`)
	if err = client.SyncRef(ctx, db, "r1", "main"); err != nil {
		t.Fatal(err)
	}
	if deleteQueries != 1 {
		t.Fatalf("incremental projection executed full delete %d times", deleteQueries)
	}
	if len(bulks) != 2 || !strings.Contains(bulks[1], `"_id":"old"`) || !strings.Contains(bulks[1], `"delete"`) || !strings.Contains(bulks[1], `"_id":"new"`) || strings.Contains(bulks[1], `"_id":"keep"`) {
		t.Fatalf("incremental bulk=%q", bulks)
	}
	var projected string
	if err = db.DB.QueryRow(`SELECT commit_id FROM search_projection_states WHERE repository_id='r1' AND ref_name='main'`).Scan(&projected); err != nil || projected != "c2" {
		t.Fatalf("projection state=%q err=%v", projected, err)
	}
	if err = client.SyncRef(ctx, db, "r1", "main"); err != nil {
		t.Fatal(err)
	}
	if deleteQueries != 1 || len(bulks) != 2 {
		t.Fatalf("unchanged projection was not a no-op: deletes=%d bulks=%d", deleteQueries, len(bulks))
	}
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','group:new','read')`)
	if err = client.SyncRef(ctx, db, "r1", "main"); err != nil {
		t.Fatal(err)
	}
	if deleteQueries != 2 || len(bulks) != 3 || !strings.Contains(bulks[2], `"group:new"`) {
		t.Fatalf("ACL change did not force projection refresh: deletes=%d bulks=%q", deleteQueries, bulks)
	}
}

func TestIncrementalBulkRetryToleratesAlreadyDeletedDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"errors":true,"items":[{"delete":{"status":404,"error":{"type":"document_missing_exception"}}},{"index":{"status":201}}]}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Index: "chunks"})
	if err != nil {
		t.Fatal(err)
	}
	var bulk strings.Builder
	bulk.WriteString("{\"delete\":{}}\n{\"index\":{}}\n{}\n")
	buffer := bytes.NewBufferString(bulk.String())
	if err = client.sendBulk(context.Background(), buffer); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
}

package opensearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git-ctx/internal/store"
)

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

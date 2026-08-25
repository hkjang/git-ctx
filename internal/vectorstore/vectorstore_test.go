package vectorstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git-ctx/internal/source"
)

// The default deployment has no vector database, and that must be reported as a
// state rather than an error the caller has to special-case incorrectly.
func TestDisabledProviderIsNotAnError(t *testing.T) {
	for _, provider := range []string{"", "none", "disabled"} {
		cfg := FromMap(map[string]any{"provider": provider})
		if cfg.Enabled() {
			t.Fatalf("provider %q must not be enabled", provider)
		}
		if _, err := Open(cfg, ""); err != ErrNotConfigured {
			t.Fatalf("provider %q err=%v", provider, err)
		}
	}
	if _, err := Open(FromMap(map[string]any{"provider": "chroma"}), ""); err == nil {
		t.Fatal("an unknown provider must be rejected")
	}
}

// The collection name is interpolated into SQL and into a request body, so it
// must be restricted to safe characters.
func TestCollectionNameIsValidated(t *testing.T) {
	if _, err := identifier("git_ctx_chunk_vectors"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"drop table x", "a;b", "a-b", `a"b`} {
		if _, err := identifier(name); err == nil {
			t.Fatalf("%q must be rejected", name)
		}
	}
	if name, err := identifier(""); err != nil || name != defaultCollection {
		t.Fatalf("name=%s err=%v", name, err)
	}
}

func TestMilvusHTTPErrorCarriesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("milvus is starting"))
	}))
	defer server.Close()
	vectorStore, err := Open(FromMap(map[string]any{
		"provider": "milvus", "baseUrl": server.URL, "dimensions": float64(4),
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	defer vectorStore.Close()
	_, err = vectorStore.Status(context.Background())
	if source.StatusOf(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "milvus 503 Service Unavailable: milvus is starting") {
		t.Fatalf("status=%d err=%v", source.StatusOf(err), err)
	}
}

func TestVectorLiteralMatchesPostgresFormat(t *testing.T) {
	if got := literal([]float32{0.5, -1, 0}); got != "[0.5,-1,0]" {
		t.Fatalf("literal=%s", got)
	}
	if got := quoteIdentifier(`vector"schema`); got != `"vector""schema"` {
		t.Fatalf("quoted identifier=%s", got)
	}
}

// Milvus reports failures inside a 200 response, so the client must read the
// envelope rather than trusting the status code.
func TestMilvusClientCreatesSearchesAndSurfacesAPIErrors(t *testing.T) {
	var paths []string
	fail := false
	upsertRevision := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer user:secret" {
			t.Errorf("missing auth header: %v", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["dbName"] != "default" {
			t.Errorf("dbName=%v", payload["dbName"])
		}
		switch {
		case fail:
			_, _ = w.Write([]byte(`{"code":1100,"message":"collection not loaded"}`))
		case strings.HasSuffix(r.URL.Path, "/collections/list"):
			_, _ = w.Write([]byte(`{"code":0,"data":["git_ctx_chunk_vectors"]}`))
		case strings.HasSuffix(r.URL.Path, "/entities/search"):
			if payload["annsField"] != "vector" || payload["limit"] != float64(5) || !strings.Contains(payload["filter"].(string), `embedding_revision == "model-v2"`) {
				t.Errorf("search payload=%v", payload)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":"c1","distance":0.91},{"id":"c2","distance":0.4}]}`))
		case strings.HasSuffix(r.URL.Path, "/entities/upsert"):
			data, _ := payload["data"].([]any)
			if len(data) > 0 {
				item, _ := data[0].(map[string]any)
				upsertRevision = item["embedding_revision"] == "model-v2"
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer server.Close()
	store, err := Open(FromMap(map[string]any{
		"provider": "milvus", "baseUrl": server.URL, "username": "user", "password": "secret", "dimensions": float64(4),
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Name() != "milvus" {
		t.Fatalf("name=%s", store.Name())
	}
	if err = store.Upsert(context.Background(), []Chunk{{ID: "c1", RepositoryID: "r", Ref: "main", Revision: "model-v2", Vector: []float32{1, 0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	if !upsertRevision {
		t.Fatal("embedding revision was not projected to Milvus")
	}
	matches, err := store.Search(context.Background(), "r", "main", "model-v2", []float32{1, 0, 0, 0}, 5)
	if err != nil || len(matches) != 2 || matches[0].ID != "c1" || matches[0].Score < 0.9 {
		t.Fatalf("matches=%#v err=%v", matches, err)
	}
	if err = store.DeleteRef(context.Background(), "r", "main"); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err = store.Search(context.Background(), "r", "main", "model-v2", []float32{1, 0, 0, 0}, 5); err == nil || !strings.Contains(err.Error(), "collection not loaded") {
		t.Fatalf("an API level error must surface: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no request was made")
	}
}

// SearchGlobal is the path search-semantic takes when the caller named no
// library, and Status is what the administration screen shows after an operator
// points the platform at Milvus. Neither was covered.
func TestMilvusSearchGlobalAndStatus(t *testing.T) {
	var searchPayload map[string]any
	collectionExists := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch {
		case strings.HasSuffix(r.URL.Path, "/entities/search"):
			searchPayload = payload
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":"a","distance":0.88},{"id":"b","distance":0.42}]}`))
		case strings.HasSuffix(r.URL.Path, "/collections/list"):
			if collectionExists {
				_, _ = w.Write([]byte(`{"code":0,"data":["git_ctx_chunk_vectors","other"]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":["other"]}`))
		case strings.HasSuffix(r.URL.Path, "/collections/get_stats"):
			// Milvus reports rowCount as a string on some builds.
			_, _ = w.Write([]byte(`{"code":0,"data":{"rowCount":"1234"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer server.Close()

	store, err := Open(FromMap(map[string]any{
		"provider": "milvus", "baseUrl": server.URL, "dimensions": float64(4),
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	matches, err := store.SearchGlobal(context.Background(), "model-v2", []float32{1, 0, 0, 0}, 7)
	if err != nil {
		t.Fatalf("SearchGlobal: %v", err)
	}
	if len(matches) != 2 || matches[0].ID != "a" || matches[0].Score < 0.87 {
		t.Fatalf("matches=%#v", matches)
	}
	// A global search must not scope itself to one repository, but it must still
	// honour the embedding revision or it mixes vectors from two models.
	if got, ok := searchPayload["filter"].(string); !ok || !strings.Contains(got, `embedding_revision == "model-v2"`) {
		t.Errorf("filter = %v, want the revision constraint", searchPayload["filter"])
	}
	if got := searchPayload["filter"].(string); strings.Contains(got, "repository_id") {
		t.Errorf("filter = %q, want no repository scope on a global search", got)
	}
	if searchPayload["limit"] != float64(7) {
		t.Errorf("limit = %v, want 7", searchPayload["limit"])
	}

	// Without a revision the filter is omitted entirely rather than sent empty.
	if _, err = store.SearchGlobal(context.Background(), "", []float32{1, 0, 0, 0}, 3); err != nil {
		t.Fatalf("SearchGlobal without revision: %v", err)
	}
	if _, present := searchPayload["filter"]; present {
		t.Errorf("filter = %v, want none when no revision is configured", searchPayload["filter"])
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Ready || status.Provider != "milvus" || status.Collection != "git_ctx_chunk_vectors" {
		t.Fatalf("status=%#v", status)
	}
	if status.Vectors != 1234 {
		t.Errorf("Vectors = %d, want the string rowCount parsed as 1234", status.Vectors)
	}
	if status.Dimensions != 4 {
		t.Errorf("Dimensions = %d, want 4", status.Dimensions)
	}

	// Before the first projection the collection does not exist yet. That is a
	// normal state, not a connection failure.
	collectionExists = false
	status, err = store.Status(context.Background())
	if err != nil {
		t.Fatalf("Status without collection: %v", err)
	}
	if !status.Ready || status.Vectors != 0 || !strings.Contains(status.Detail, "not created yet") {
		t.Fatalf("status=%#v", status)
	}
}

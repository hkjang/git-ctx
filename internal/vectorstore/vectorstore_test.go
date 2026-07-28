package vectorstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestVectorLiteralMatchesPostgresFormat(t *testing.T) {
	if got := literal([]float32{0.5, -1, 0}); got != "[0.5,-1,0]" {
		t.Fatalf("literal=%s", got)
	}
}

// Milvus reports failures inside a 200 response, so the client must read the
// envelope rather than trusting the status code.
func TestMilvusClientCreatesSearchesAndSurfacesAPIErrors(t *testing.T) {
	var paths []string
	fail := false
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
			if payload["annsField"] != "vector" || payload["limit"] != float64(5) {
				t.Errorf("search payload=%v", payload)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":"c1","distance":0.91},{"id":"c2","distance":0.4}]}`))
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
	if err = store.Upsert(context.Background(), []Chunk{{ID: "c1", RepositoryID: "r", Ref: "main", Vector: []float32{1, 0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	matches, err := store.Search(context.Background(), "r", "main", []float32{1, 0, 0, 0}, 5)
	if err != nil || len(matches) != 2 || matches[0].ID != "c1" || matches[0].Score < 0.9 {
		t.Fatalf("matches=%#v err=%v", matches, err)
	}
	if err = store.DeleteRef(context.Background(), "r", "main"); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err = store.Search(context.Background(), "r", "main", []float32{1, 0, 0, 0}, 5); err == nil || !strings.Contains(err.Error(), "collection not loaded") {
		t.Fatalf("an API level error must surface: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no request was made")
	}
}

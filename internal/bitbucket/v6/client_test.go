package v6

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git-ctx/internal/source"
)

func TestBitbucket69EndpointsAndPagination(t *testing.T) {
	var starts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat" {
			t.Errorf("missing PAT")
		}
		switch r.URL.Path {
		case "/rest/api/1.0/projects":
			starts = append(starts, r.URL.Query().Get("start"))
			if r.URL.Query().Get("start") == "0" {
				w.Write([]byte(`{"values":[{"key":"KCB","name":"Core"}],"isLastPage":false,"nextPageStart":1}`))
				return
			}
			w.Write([]byte(`{"values":[{"key":"OPS","name":"Ops"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/repos/demo/files":
			if r.URL.Query().Get("at") != "main" {
				t.Errorf("missing ref")
			}
			w.Write([]byte(`{"values":["README.md","docs/guide.md"],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/repos/demo/raw/docs/guide.md":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("# Guide"))
		case "/rest/search/latest/search":
			if r.Method != http.MethodPost {
				t.Error("search must use POST")
			}
			var body struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Query != "project:KCB repo:demo gpu usage" {
				t.Errorf("query=%q", body.Query)
			}
			w.Write([]byte(`{"code":{"values":[{"file":"docs/gpu.md","lines":[{"line":7,"segments":[{"text":"GPU "},{"text":"usage"}]}]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := c.ListProjects(context.Background())
	if err != nil || len(projects) != 2 {
		t.Fatalf("projects=%#v err=%v", projects, err)
	}
	if len(starts) != 2 || starts[1] != "1" {
		t.Fatalf("pagination starts=%v", starts)
	}
	ref := source.RepositoryRef{ProjectKey: "KCB", Slug: "demo"}
	files, err := c.ListFiles(context.Background(), ref, "main")
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%#v err=%v", files, err)
	}
	body, err := c.GetFile(context.Background(), ref, "main", "docs/guide.md")
	if err != nil || string(body) != "# Guide" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	hits, err := c.SearchQuery(context.Background(), ref, "main", "gpu usage", 5)
	if err != nil || len(hits) != 1 || hits[0].Path != "docs/gpu.md" || hits[0].LineStart != 7 || hits[0].Snippet != "GPU usage" {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
}

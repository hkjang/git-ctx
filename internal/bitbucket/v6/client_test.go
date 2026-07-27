package v6

import (
	"context"
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
}

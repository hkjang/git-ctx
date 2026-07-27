package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git-ctx/internal/source"
)

func TestGitLabAdapterPaginationFilesAndPermissions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "pat" {
			t.Error("token missing")
		}
		switch r.URL.EscapedPath() {
		case "/api/v4/groups":
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				w.Write([]byte(`[{"id":1,"name":"Core","path":"core"}]`))
				return
			}
			w.Write([]byte(`[{"id":2,"name":"Ops","path":"ops"}]`))
		case "/api/v4/projects/core%2Fdemo/repository/tree":
			if r.URL.Query().Get("recursive") != "true" || r.URL.Query().Get("ref") != "main" {
				t.Error("tree query missing")
			}
			w.Write([]byte(`[{"path":"docs","type":"tree"},{"path":"README.md","type":"blob"}]`))
		case "/api/v4/projects/core%2Fdemo/repository/files/docs%2Fguide.md/raw":
			w.Write([]byte("# Guide"))
		case "/api/v4/projects/core%2Fdemo/members/all":
			w.Write([]byte(`[{"id":42,"username":"alice","access_level":30}]`))
		case "/api/v4/projects/core%2Fdemo/search":
			if r.URL.Query().Get("scope") != "blobs" || r.URL.Query().Get("search") != "gpu usage" || r.URL.Query().Get("ref") != "main" {
				t.Errorf("search query=%s", r.URL.RawQuery)
			}
			w.Write([]byte(`[{"path":"docs/gpu.md","data":"GPU usage API","ref":"main","startline":12}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := c.ListProjects(context.Background())
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
	ref := source.RepositoryRef{ProjectKey: "core", Slug: "demo"}
	files, err := c.ListFiles(context.Background(), ref, "main")
	if err != nil || len(files) != 1 || files[0].Path != "README.md" {
		t.Fatalf("files=%#v err=%v", files, err)
	}
	body, err := c.GetFile(context.Background(), ref, "main", "docs/guide.md")
	if err != nil || string(body) != "# Guide" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	perms, err := c.GetPermissions(context.Background(), ref)
	if err != nil || len(perms) != 1 || perms[0].Principal != "gitlab:42" || perms[0].Permission != "developer" {
		t.Fatalf("permissions=%#v err=%v", perms, err)
	}
	hits, err := c.SearchQuery(context.Background(), ref, "main", "gpu usage", 5)
	if err != nil || len(hits) != 1 || hits[0].Path != "docs/gpu.md" || hits[0].LineStart != 12 {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
}

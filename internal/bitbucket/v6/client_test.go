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
		case "/rest/api/1.0/projects/KCB/repos/demo/changes":
			if r.URL.Query().Get("since") != "old" || r.URL.Query().Get("until") != "new" {
				t.Errorf("changes query=%s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"values":[{"type":"MODIFY","path":{"toString":"docs/guide.md"}},{"type":"MOVE","path":{"toString":"docs/new.md"},"srcPath":{"toString":"docs/old.md"}},{"type":"DELETE","path":{"toString":"docs/gone.md"}}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/repos/demo/permissions/users":
			w.Write([]byte(`{"values":[{"user":{"name":"repo-user","slug":"repo-user"},"permission":"REPO_READ"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/repos/demo/permissions/groups":
			w.Write([]byte(`{"values":[{"group":{"name":"repo-group"},"permission":"REPO_WRITE"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/permissions/users":
			w.Write([]byte(`{"values":[{"user":{"name":"project-user","slug":"project-user"},"permission":"PROJECT_READ"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/permissions/groups":
			w.Write([]byte(`{"values":[{"group":{"name":"project-group"},"permission":"PROJECT_ADMIN"}],"isLastPage":true}`))
		case "/rest/api/1.0/admin/permissions/users":
			w.Write([]byte(`{"values":[{"user":{"name":"global-admin","slug":"global-admin"},"permission":"ADMIN"},{"user":{"name":"licensed","slug":"licensed"},"permission":"LICENSED_USER"}],"isLastPage":true}`))
		case "/rest/api/1.0/admin/permissions/groups":
			w.Write([]byte(`{"values":[{"group":{"name":"sysadmins"},"permission":"SYS_ADMIN"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/permissions/PROJECT_READ/all":
			w.Write([]byte(`{"permitted":false}`))
		case "/rest/api/1.0/projects/KCB/permissions/PROJECT_WRITE/all":
			w.Write([]byte(`{"permitted":true}`))
		case "/rest/api/1.0/projects/KCB/permissions/PROJECT_ADMIN/all":
			t.Error("default permission lookup should stop after a match")
			w.Write([]byte(`{"permitted":false}`))
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
	changes, err := c.Changes(context.Background(), ref, "old", "new")
	if err != nil || len(changes) != 3 || changes[1].Type != "move" || changes[1].OldPath != "docs/old.md" || changes[2].Type != "delete" {
		t.Fatalf("changes=%#v err=%v", changes, err)
	}
	hits, err := c.SearchQuery(context.Background(), ref, "main", "gpu usage", 5)
	if err != nil || len(hits) != 1 || hits[0].Path != "docs/gpu.md" || hits[0].LineStart != 7 || hits[0].Snippet != "GPU usage" {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
	permissions, err := c.GetPermissions(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, permission := range permissions {
		got[permission.Kind+":"+permission.Principal] = true
	}
	for _, want := range []string{"user:repo-user", "group:repo-group", "user:project-user", "group:project-group", "user:global-admin", "group:sysadmins", "all:bitbucket:licensed"} {
		if !got[want] {
			t.Fatalf("missing inherited permission %q in %#v", want, permissions)
		}
	}
	if got["user:licensed"] {
		t.Fatalf("LICENSED_USER must not imply repository access: %#v", permissions)
	}
}

func TestConfigurableSearchAPIPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/code-search" {
			t.Errorf("path=%s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"code":{"values":[]}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: "pat", SearchAPIPath: "/custom/code-search"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "P", Slug: "r"}, "main", "term", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = New(Config{BaseURL: server.URL, Token: "pat", SearchAPIPath: "https://evil.example/search"}); err == nil {
		t.Fatal("absolute search URL was accepted")
	}
}

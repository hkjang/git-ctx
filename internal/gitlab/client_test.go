package gitlab

import (
	"context"
	"errors"
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
		case "/api/v4/projects/core%2Fdemo/repository/compare":
			if r.URL.Query().Get("from") != "old" || r.URL.Query().Get("to") != "new" || r.URL.Query().Get("straight") != "true" {
				t.Errorf("compare query=%s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"diffs":[{"old_path":"docs/a.md","new_path":"docs/a.md"},{"old_path":"docs/old.md","new_path":"docs/new.md","renamed_file":true},{"old_path":"docs/gone.md","new_path":"docs/gone.md","deleted_file":true}]}`))
		case "/api/v4/projects/core%2Fdemo/members/all":
			w.Write([]byte(`[{"id":42,"username":"alice","access_level":30}]`))
		case "/api/v4/projects/core%2Fdemo":
			w.Write([]byte(`{"visibility":"internal"}`))
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
	changes, err := c.Changes(context.Background(), ref, "old", "new")
	if err != nil || len(changes) != 3 || changes[1].Type != "renamed" || changes[1].OldPath != "docs/old.md" || changes[2].Type != "deleted" {
		t.Fatalf("changes=%#v err=%v", changes, err)
	}
	perms, err := c.GetPermissions(context.Background(), ref)
	if err != nil || len(perms) != 2 || perms[0].Principal != "gitlab:42" || perms[0].Permission != "developer" || perms[1].Principal != "gitlab:authenticated" {
		t.Fatalf("permissions=%#v err=%v", perms, err)
	}
	hits, err := c.SearchQuery(context.Background(), ref, "main", "gpu usage", 5)
	if err != nil || len(hits) != 1 || hits[0].Path != "docs/gpu.md" || hits[0].LineStart != 12 {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
}

// Subgroup projects must carry the full namespace path. Using only the direct
// parent path produced /projects/sub%2Fproject URLs that GitLab answers with 404
// for every later search, ACL and content call.
func TestGitLabRepositoriesUseFullNamespacePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/groups/platform/projects":
			w.Write([]byte(`[{"id":7,"path":"agent","name":"Agent","default_branch":"main","path_with_namespace":"platform/ai/agent","namespace":{"path":"ai","full_path":"platform/ai"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := c.ListRepositories(context.Background(), "platform")
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%#v err=%v", repositories, err)
	}
	if repositories[0].ProjectKey != "platform/ai" || repositories[0].Slug != "agent" {
		t.Fatalf("unexpected namespace mapping: %#v", repositories[0])
	}
}

func TestGitLabGlobalSearchResolvesProjectAndReportsUnsupported(t *testing.T) {
	advanced := true
	projectCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/search":
			if !advanced {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"scope does not have a valid value"}`))
				return
			}
			if r.URL.Query().Get("scope") != "blobs" || r.URL.Query().Get("search") != "NewOIDCVerifier" {
				t.Errorf("search query=%s", r.URL.RawQuery)
			}
			w.Write([]byte(`[{"path":"internal/auth/oidc.go","data":"func NewOIDCVerifier(","ref":"main","startline":80,"project_id":7}]`))
		case "/api/v4/projects/7":
			projectCalls++
			w.Write([]byte(`{"id":7,"path":"agent","name":"Agent","default_branch":"main","path_with_namespace":"platform/ai/agent","namespace":{"path":"ai","full_path":"platform/ai"}}`))
		case "/api/v4/projects":
			if r.URL.Query().Get("search") != "agent" || r.URL.Query().Get("search_namespaces") != "true" {
				t.Errorf("project search query=%s", r.URL.RawQuery)
			}
			w.Write([]byte(`[{"id":7,"path":"agent","name":"Agent","default_branch":"main","namespace":{"full_path":"platform/ai"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := c.SearchGlobalQuery(context.Background(), "NewOIDCVerifier", 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	if results[0].ProjectKey != "platform/ai" || results[0].Slug != "agent" || results[0].LineStart != 80 {
		t.Fatalf("unexpected global hit: %#v", results[0])
	}
	if _, err = c.SearchRepositories(context.Background(), "agent", 10); err != nil {
		t.Fatalf("repository search: %v", err)
	}
	// The project lookup must be cached so a page of hits from one project does
	// not issue one API call per hit.
	if _, err = c.SearchGlobalQuery(context.Background(), "NewOIDCVerifier", 10); err != nil {
		t.Fatal(err)
	}
	if projectCalls != 1 {
		t.Fatalf("project lookups=%d, want 1 cached lookup", projectCalls)
	}
	advanced = false
	if _, err = c.SearchGlobalQuery(context.Background(), "NewOIDCVerifier", 10); !errors.Is(err, source.ErrGlobalSearchUnsupported) {
		t.Fatalf("expected unsupported sentinel, got %v", err)
	}
}

// GitLab rejects a search that names a ref the remote no longer has. The client
// retries on the default branch instead of returning nothing.
func TestGitLabSearchRetriesWithoutMissingRef(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v4/projects/core%2Fdemo/search" {
			http.NotFound(w, r)
			return
		}
		attempts++
		if r.URL.Query().Get("ref") != "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"ref not found"}`))
			return
		}
		w.Write([]byte(`[{"path":"README.md","data":"hello","ref":"main","startline":1}]`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := c.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "core", Slug: "demo"}, "deleted-branch", "hello", 5)
	if err != nil || len(hits) != 1 || attempts != 2 {
		t.Fatalf("hits=%#v attempts=%d err=%v", hits, attempts, err)
	}
}

package gitlab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
			w.Write([]byte(`[{"id":42,"username":"alice","state":"active","access_level":30}]`))
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

func TestGitLabPermissionsExcludeNonActiveMembers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/core%2Fdemo/members/all":
			w.Write([]byte(`[
				{"id":1,"username":"active","state":"active","access_level":30},
				{"id":2,"username":"case-variant","state":" ACTIVE ","access_level":20},
				{"id":3,"username":"blocked","state":"blocked","access_level":50},
				{"id":4,"username":"inactive","state":"deactivated","access_level":40},
				{"id":5,"username":"unknown","state":"banned","access_level":40},
				{"id":6,"username":"missing-state","access_level":40},
				{"id":7,"username":"guest","state":"active","access_level":10},
				{"id":8,"username":"planner","state":"active","access_level":15},
				{"id":0,"username":"missing-id","state":"active","access_level":40}
			]`))
		case "/api/v4/projects/core%2Fdemo":
			w.Write([]byte(`{"visibility":"private"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := c.GetPermissions(context.Background(), source.RepositoryRef{ProjectKey: "core", Slug: "demo"})
	if err != nil || len(permissions) != 3 {
		t.Fatalf("permissions=%#v err=%v", permissions, err)
	}
	if permissions[0].Principal != "gitlab:1" || permissions[0].Permission != "developer" ||
		permissions[1].Principal != "gitlab:2" || permissions[1].Permission != "reporter" ||
		permissions[2].Principal != "gitlab:8" || permissions[2].Permission != "read" {
		t.Fatalf("unexpected active permissions: %#v", permissions)
	}
}

func TestGitLabPermissionsHonorRepositoryAccessLevel(t *testing.T) {
	tests := []struct {
		name        string
		projectJSON string
		principals  []string
	}{
		{
			name:        "public repository enabled",
			projectJSON: `{"visibility":"public","repository_access_level":"enabled"}`,
			principals:  []string{"gitlab:1", "*"},
		},
		{
			name:        "public project members-only repository",
			projectJSON: `{"visibility":"public","repository_access_level":"private"}`,
			principals:  []string{"gitlab:1"},
		},
		{
			name:        "internal project members-only repository",
			projectJSON: `{"visibility":"internal","repository_access_level":"private"}`,
			principals:  []string{"gitlab:1"},
		},
		{
			name:        "repository disabled",
			projectJSON: `{"visibility":"public","repository_access_level":"disabled"}`,
		},
		{
			name:        "legacy response omits feature level",
			projectJSON: `{"visibility":"internal"}`,
			principals:  []string{"gitlab:1", "gitlab:authenticated"},
		},
		{
			name:        "unknown feature level fails closed",
			projectJSON: `{"visibility":"public","repository_access_level":"future-value"}`,
			principals:  []string{"gitlab:1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case "/api/v4/projects/core%2Fdemo/members/all":
					w.Write([]byte(`[{"id":1,"username":"reader","state":"active","access_level":20}]`))
				case "/api/v4/projects/core%2Fdemo":
					w.Write([]byte(tc.projectJSON))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
			if err != nil {
				t.Fatal(err)
			}
			permissions, err := c.GetPermissions(context.Background(), source.RepositoryRef{ProjectKey: "core", Slug: "demo"})
			if err != nil || len(permissions) != len(tc.principals) {
				t.Fatalf("permissions=%#v err=%v", permissions, err)
			}
			for index, principal := range tc.principals {
				if permissions[index].Principal != principal {
					t.Fatalf("permissions[%d]=%#v, want principal %q", index, permissions[index], principal)
				}
			}
		})
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
	if _, err = c.SearchGlobalQuery(context.Background(), "NewOIDCVerifier", 10); !errors.Is(err, source.ErrGlobalSearchUnsupported) || source.StatusOf(err) != http.StatusBadRequest {
		t.Fatalf("expected unsupported sentinel, got %v", err)
	}
}

func TestGitLabEndpointDropsBaseQueryAndFragment(t *testing.T) {
	c, err := New(Config{BaseURL: "https://gitlab.example.test/root?tenant=wrong#settings", Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		query url.Values
		want  string
	}{
		{name: "nil query"},
		{name: "request query replaces base query", query: url.Values{"search": []string{"gpu usage"}}, want: "search=gpu+usage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, parseErr := url.Parse(c.endpoint("/projects/7", tc.query))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if endpoint.EscapedPath() != "/root/api/v4/projects/7" ||
				endpoint.RawQuery != tc.want ||
				endpoint.Fragment != "" ||
				endpoint.RawFragment != "" ||
				endpoint.ForceQuery {
				t.Fatalf("unexpected endpoint: %#v", endpoint)
			}
		})
	}
}

// A result from another branch must never be labeled as if it came from the
// requested ref. In particular, a missing ref must not trigger an unscoped
// default-branch retry.
func TestGitLabSearchDoesNotChangeRequestedRef(t *testing.T) {
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
		t.Error("search unexpectedly retried without the requested ref")
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := c.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "core", Slug: "demo"}, "deleted-branch", "hello", 5)
	if err == nil || len(hits) != 0 || attempts != 1 || source.StatusOf(err) != http.StatusBadRequest {
		t.Fatalf("hits=%#v attempts=%d err=%v", hits, attempts, err)
	}
}

func TestGitLabSearchFollowsPaginationPastDuplicateFiles(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v4/projects/platform%2Fai%2Fdemo/search" {
			http.NotFound(w, r)
			return
		}
		pages++
		if r.URL.Query().Get("search") != "gpu usage + filename:*.md" ||
			r.URL.Query().Get("ref") != "release/v2" ||
			r.URL.Query().Get("scope") != "blobs" ||
			r.URL.Query().Get("per_page") != "2" {
			t.Errorf("search query=%s", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", `</api/v4/projects/platform%2Fai%2Fdemo/search?page=2>; rel="next"`)
			w.Write([]byte(`[
				{"path":"README.md","data":"first","ref":"release/v2","startline":4},
				{"path":"README.md","data":"second occurrence","ref":"release/v2","startline":18}
			]`))
		case "2":
			// Older GitLab versions used filename as the full path. Keep that
			// response shape readable while preferring path on current versions.
			w.Write([]byte(`[{"filename":"docs/legacy.md","data":"legacy","ref":"release/v2","startline":9}]`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := c.SearchQuery(
		context.Background(),
		source.RepositoryRef{ProjectKey: "platform/ai", Slug: "demo"},
		"release/v2",
		"gpu usage + filename:*.md",
		2,
	)
	if err != nil || pages != 2 || len(hits) != 2 {
		t.Fatalf("hits=%#v pages=%d err=%v", hits, pages, err)
	}
	if hits[0].Path != "README.md" || hits[1].Path != "docs/legacy.md" || hits[1].LineStart != 9 {
		t.Fatalf("unexpected hits: %#v", hits)
	}
}

func TestGitLabListPaginationUsesLinkHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v4/groups" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", `</api/v4/groups?page=2&per_page=100>; rel="next"`)
			w.Write([]byte(`[{"id":1,"name":"Core","path":"core"}]`))
		case "2":
			w.Write([]byte(`[{"id":2,"name":"Ops","path":"ops"}]`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := c.ListProjects(context.Background())
	if err != nil || len(groups) != 2 || groups[1].Key != "2" {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
}

func TestGitLabPaginationLimitFailsClosed(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 {
				t.Errorf("invalid page %q", r.URL.Query().Get("page"))
				page = 1
			}
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
			w.Write([]byte(`[{"id":1,"name":"Core","path":"core"}]`))
		}))
		defer srv.Close()
		c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		groups, err := c.ListProjects(context.Background())
		if !errors.Is(err, errPaginationLimitExceeded) || len(groups) != 0 || calls != maxPages {
			t.Fatalf("groups=%#v calls=%d err=%v", groups, calls, err)
		}
	})

	t.Run("blob search", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 {
				t.Errorf("invalid page %q", r.URL.Query().Get("page"))
				page = 1
			}
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
			w.Write([]byte(`[{"path":"README.md","data":"needle","ref":"main","startline":1}]`))
		}))
		defer srv.Close()
		c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		hits, err := c.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "core", Slug: "demo"}, "main", "needle", 2)
		if !errors.Is(err, errPaginationLimitExceeded) || len(hits) != 0 || calls != maxPages {
			t.Fatalf("hits=%#v calls=%d err=%v", hits, calls, err)
		}
	})
}

type repeatingReader byte

func (r repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestGitLabGetFileRejectsOversizedResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/core%2Fdemo/repository/files/known-large.bin/raw":
			w.Header().Set("Content-Length", strconv.FormatInt(maxRawFileBytes+1, 10))
			w.WriteHeader(http.StatusOK)
		case "/api/v4/projects/core%2Fdemo/repository/files/stream-large.bin/raw":
			// Flush before writing so the client cannot rely on Content-Length;
			// the limit+1 read must independently detect the overflow.
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			_, _ = io.CopyN(w, repeatingReader('x'), maxRawFileBytes+1)
		case "/api/v4/projects/core%2Fdemo/repository/files/exact.bin/raw":
			w.Header().Set("Content-Length", strconv.FormatInt(maxRawFileBytes, 10))
			w.WriteHeader(http.StatusOK)
			_, _ = io.CopyN(w, repeatingReader('x'), maxRawFileBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	repo := source.RepositoryRef{ProjectKey: "core", Slug: "demo"}
	for _, path := range []string{"known-large.bin", "stream-large.bin"} {
		body, getErr := c.GetFile(context.Background(), repo, "main", path)
		if !errors.Is(getErr, errFileTooLarge) || len(body) != 0 {
			t.Fatalf("path=%s bytes=%d err=%v", path, len(body), getErr)
		}
	}
	body, err := c.GetFile(context.Background(), repo, "main", "exact.bin")
	if err != nil || int64(len(body)) != maxRawFileBytes || body[0] != 'x' || body[len(body)-1] != 'x' {
		t.Fatalf("exact boundary bytes=%d err=%v", len(body), err)
	}
}

func TestGitLabGlobalSearchDoesNotMaskForbiddenAsUnsupported(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"403 Forbidden"}`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "expired"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SearchGlobalQuery(context.Background(), "needle", 5)
	if err == nil || errors.Is(err, source.ErrGlobalSearchUnsupported) || source.StatusOf(err) != http.StatusForbidden || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestGitLabGlobalSearchPropagatesProjectLookupFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/search":
			w.Write([]byte(`[{"path":"README.md","data":"needle","ref":"main","startline":1,"project_id":7}]`))
		case "/api/v4/projects/7":
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"401 Unauthorized"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "expired"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := c.SearchGlobalQuery(context.Background(), "needle", 5)
	if err == nil || len(results) != 0 || source.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("results=%#v err=%v", results, err)
	}
}

func TestGlobalSearchUnsupportedRequiresFeatureSpecificError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "basic search rejects blobs scope",
			err:  &source.APIError{Source: "gitlab", StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Body: `{"error":"scope does not have a valid value"}`},
			want: true,
		},
		{
			name: "invalid query",
			err:  &source.APIError{Source: "gitlab", StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Body: `{"error":"invalid search query"}`},
		},
		{
			name: "missing scope",
			err:  &source.APIError{Source: "gitlab", StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Body: `{"error":"scope is missing"}`},
		},
		{
			name: "forbidden",
			err:  &source.APIError{Source: "gitlab", StatusCode: http.StatusForbidden, Status: "403 Forbidden", Body: `{"message":"403 Forbidden"}`},
		},
		{
			name: "not implemented",
			err:  &source.APIError{Source: "gitlab", StatusCode: http.StatusNotImplemented, Status: "501 Not Implemented"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := globalSearchUnsupported(tc.err); got != tc.want {
				t.Fatalf("globalSearchUnsupported()=%v, want %v for %v", got, tc.want, tc.err)
			}
		})
	}
}

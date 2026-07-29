package v6

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
			w.Write([]byte(`{"values":[{"user":{"name":"repo-user","slug":"repo-user","active":true},"permission":"REPO_READ"},{"user":{"name":"disabled-user","slug":"disabled-user","active":false},"permission":"REPO_ADMIN"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/repos/demo/permissions/groups":
			w.Write([]byte(`{"values":[{"group":{"name":"repo-group"},"permission":"REPO_WRITE"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/permissions/users":
			w.Write([]byte(`{"values":[{"user":{"name":"project-user","slug":"project-user","active":true},"permission":"PROJECT_READ"}],"isLastPage":true}`))
		case "/rest/api/1.0/projects/KCB/permissions/groups":
			w.Write([]byte(`{"values":[{"group":{"name":"project-group"},"permission":"PROJECT_ADMIN"}],"isLastPage":true}`))
		case "/rest/api/1.0/admin/permissions/users":
			w.Write([]byte(`{"values":[{"user":{"name":"global-admin","slug":"global-admin","active":true},"permission":"ADMIN"},{"user":{"name":"licensed","slug":"licensed","active":true},"permission":"LICENSED_USER"}],"isLastPage":true}`))
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
	if got["user:disabled-user"] {
		t.Fatalf("inactive users must not receive repository access: %#v", permissions)
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

func TestSearchRepositoriesPreservesQueryAcrossPages(t *testing.T) {
	var starts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/1.0/repos" {
			t.Errorf("path=%q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("name"); got != "GPU tools" {
			t.Errorf("name=%q", got)
		}
		starts = append(starts, r.URL.Query().Get("start"))
		switch r.URL.Query().Get("start") {
		case "0":
			w.Write([]byte(`{"values":[{"id":1,"slug":"gpu","name":"GPU","project":{"key":"AI"},"defaultBranch":"main"}],"isLastPage":false,"nextPageStart":7}`))
		case "7":
			w.Write([]byte(`{"values":[{"id":2,"slug":"tools","name":"Tools","project":{"key":"AI"},"defaultBranch":{"displayId":"release"}}],"isLastPage":true}`))
		default:
			t.Errorf("unexpected start=%q", r.URL.Query().Get("start"))
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := client.SearchRepositories(context.Background(), " GPU tools ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 || repositories[0].DefaultBranch != "main" || repositories[1].DefaultBranch != "release" {
		t.Fatalf("repositories=%#v", repositories)
	}
	if len(starts) != 2 || starts[0] != "0" || starts[1] != "7" {
		t.Fatalf("starts=%v", starts)
	}
}

func TestPaginationLimitAndIncompleteCap(t *testing.T) {
	t.Run("requested repository limit stops paging", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if got := r.URL.Query().Get("limit"); got != "1" {
				t.Errorf("limit=%q", got)
			}
			w.Write([]byte(`{"values":[{"id":1,"slug":"gpu","project":{"key":"AI"}}],"isLastPage":false,"nextPageStart":1}`))
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		repositories, err := client.SearchRepositories(context.Background(), "gpu", 1)
		if err != nil || len(repositories) != 1 || calls != 1 {
			t.Fatalf("repositories=%#v calls=%d err=%v", repositories, calls, err)
		}
	})

	t.Run("remaining page after cap is an error", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wantStart := strconv.Itoa(calls)
			if got := r.URL.Query().Get("start"); got != wantStart {
				t.Errorf("start=%q want=%q", got, wantStart)
			}
			calls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"values": []int{calls}, "isLastPage": false, "nextPageStart": calls,
			})
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		items, err := pageAll[int](context.Background(), client, "/items")
		if err == nil || !strings.Contains(err.Error(), "pagination incomplete") || len(items) != 0 || calls != maxPages {
			t.Fatalf("items=%v calls=%d err=%v", items, calls, err)
		}
	})
}

func TestAPIPathsAreEscapedExactlyOnce(t *testing.T) {
	const expectedPath = "/bitbucket%20context/rest/api/1.0/projects/CORE%20TEAM/repos/repo%231/raw/dir/a%20b%25%23%3F.go"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != expectedPath {
			t.Errorf("escaped path=%q want=%q", got, expectedPath)
		}
		if got := r.URL.Query().Get("at"); got != "main" {
			t.Errorf("at=%q", got)
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/bitbucket%20context", Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.GetFile(context.Background(), source.RepositoryRef{ProjectKey: "CORE TEAM", Slug: "repo#1"}, "main", "dir/a b%#?.go")
	if err != nil || string(body) != "ok" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestGetFileRejectsUnsafePathBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	ref := source.RepositoryRef{ProjectKey: "KCB", Slug: "demo"}
	for _, filePath := range []string{"../permissions", "dir/../permissions", "/permissions", "", "./file.go", "dir//file.go", "dir/."} {
		body, err := client.GetFile(context.Background(), ref, "main", filePath)
		if err == nil || body != nil {
			t.Errorf("path=%q body=%q err=%v", filePath, body, err)
		}
	}
	if requests != 0 {
		t.Fatalf("unsafe paths made %d server requests", requests)
	}
}

func TestGetFileRejectsOversizeResponses(t *testing.T) {
	ref := source.RepositoryRef{ProjectKey: "KCB", Slug: "demo"}
	t.Run("declared content length", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.FormatInt(maxFileBytes+1, 10))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		body, err := client.GetFile(context.Background(), ref, "main", "large.bin")
		if err == nil || !strings.Contains(err.Error(), "exceeds") || body != nil {
			t.Fatalf("bytes=%d err=%v", len(body), err)
		}
	})

	t.Run("chunked body", func(t *testing.T) {
		payload := bytes.Repeat([]byte{'x'}, int(maxFileBytes)+1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			_, _ = w.Write(payload)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		body, err := client.GetFile(context.Background(), ref, "main", "large.bin")
		if err == nil || !strings.Contains(err.Error(), "exceeds") || body != nil {
			t.Fatalf("bytes=%d err=%v", len(body), err)
		}
	})
}

func TestSearchCodeParsesHitContextsAndNextStart(t *testing.T) {
	var starts, limits []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Entities struct {
				Code struct {
					Start int `json:"start"`
					Limit int `json:"limit"`
				} `json:"code"`
			} `json:"entities"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		starts = append(starts, body.Entities.Code.Start)
		limits = append(limits, body.Entities.Code.Limit)
		switch body.Entities.Code.Start {
		case 0:
			w.Write([]byte(`{"code":{"values":[{"file":"src/gpu.go","repository":{"id":3,"slug":"demo","name":"Demo","project":{"key":"KCB"},"defaultBranch":"main"},"hitContexts":[[{"line":6,"text":"before &amp;"},{"line":7,"text":"<em>GPU</em> usage"},{"line":0,"text":"separator"}]]}],"isLastPage":false,"nextStart":7}}`))
		case 7:
			w.Write([]byte(`{"code":{"values":[{"file":"src/cpu.go","repository":{"id":3,"slug":"demo","name":"Demo","project":{"key":"KCB"},"defaultBranch":{"displayId":"main"}},"lines":[{"line":12,"segments":[{"text":"CPU "},{"text":"usage"}]}]}],"isLastPage":true}}`))
		default:
			t.Errorf("unexpected start=%d", body.Entities.Code.Start)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := client.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "KCB", Slug: "demo"}, "main", "usage", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits=%#v", hits)
	}
	if hits[0].Snippet != "before &\nGPU usage" || hits[0].LineStart != 6 || hits[0].LineEnd != 7 || hits[0].CommitID != "main" {
		t.Fatalf("first hit=%#v", hits[0])
	}
	if hits[1].Snippet != "CPU usage" || hits[1].LineStart != 12 || hits[1].CommitID != "main" {
		t.Fatalf("second hit=%#v", hits[1])
	}
	if len(starts) != 2 || starts[0] != 0 || starts[1] != 7 || limits[0] != 2 || limits[1] != 1 {
		t.Fatalf("starts=%v limits=%v", starts, limits)
	}
}

func TestSearchPaginationCapIsIncompleteError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Entities struct {
				Code struct {
					Start int `json:"start"`
				} `json:"code"`
			} `json:"entities"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Entities.Code.Start != calls {
			t.Errorf("start=%d want=%d", body.Entities.Code.Start, calls)
		}
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": map[string]any{
				"values": []map[string]any{{
					"file": "result.go",
					"repository": map[string]any{
						"id": 1, "slug": "demo", "project": map[string]any{"key": "KCB"}, "defaultBranch": "main",
					},
					"hitContexts": []any{[]map[string]any{{"line": calls, "text": "term"}}},
				}},
				"isLastPage": false,
				"nextStart":  calls,
			},
		})
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := client.SearchGlobalQuery(context.Background(), "term", 50)
	if err == nil || !strings.Contains(err.Error(), "pagination incomplete") || len(hits) != 0 || calls != maxPages {
		t.Fatalf("hits=%#v calls=%d err=%v", hits, calls, err)
	}
}

func TestSearchQueryDoesNotRelabelDefaultBranchHit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":{"values":[
			{"file":"legacy.go","hitContexts":[[{"line":1,"text":"term"}]]},
			{"file":"foreign.go","repository":{"slug":"other","project":{"key":"KCB"},"defaultBranch":"release"},"hitContexts":[[{"line":1,"text":"term"}]]},
			{"file":"main.go","repository":{"slug":"demo","project":{"key":"KCB"},"defaultBranch":"main"},"hitContexts":[[{"line":2,"text":"term"}]]}
		],"isLastPage":true}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := client.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "KCB", Slug: "demo"}, "release", "term", 5)
	if !errors.Is(err, source.ErrCodeSearchRefUnsupported) || len(hits) != 0 {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
}

func TestSearchErrorsAreClassified(t *testing.T) {
	t.Run("retry and typed status", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				http.Error(w, "temporary", http.StatusInternalServerError)
				return
			}
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "KCB", Slug: "demo"}, "main", "term", 1)
		if source.StatusOf(err) != http.StatusForbidden || calls != 2 {
			t.Fatalf("status=%d calls=%d err=%v", source.StatusOf(err), calls, err)
		}
	})

	t.Run("successful response error envelope", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"errors":[{"message":"search index is unavailable"}]}`))
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SearchQuery(context.Background(), source.RepositoryRef{ProjectKey: "KCB", Slug: "demo"}, "main", "term", 1)
		if err == nil || !strings.Contains(err.Error(), "search index is unavailable") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("global endpoint missing", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Token: "pat"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SearchGlobalQuery(context.Background(), "term", 1)
		if !errors.Is(err, source.ErrGlobalSearchUnsupported) || source.StatusOf(err) != http.StatusNotFound {
			t.Fatalf("err=%v status=%d", err, source.StatusOf(err))
		}
	})
}

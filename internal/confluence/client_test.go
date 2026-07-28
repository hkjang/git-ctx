package confluence

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git-ctx/internal/source"
)

func TestClientDiscoveryContentACLAndQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/space":
			_, _ = w.Write([]byte(`{"results":[{"id":"1","key":"OPS","name":"Operations","description":"Runbooks"}]}`))
		case "/rest/api/space/OPS":
			_, _ = w.Write([]byte(`{"id":"1","key":"OPS","name":"Operations","description":"Runbooks"}`))
		case "/rest/api/content":
			_, _ = w.Write([]byte(`{"results":[{"id":"42","title":"GPU Runbook"}]}`))
		case "/rest/api/content/42":
			_, _ = w.Write([]byte(`{"id":"42","title":"GPU Runbook","body":{"storage":{"value":"<p>Restart <strong>DCGM</strong>.</p>"}}}`))
		case "/rest/api/search":
			if !strings.Contains(r.URL.Query().Get("cql"), "GPU") {
				t.Errorf("cql=%q", r.URL.Query().Get("cql"))
			}
			_, _ = w.Write([]byte(`{"results":[{"content":{"id":"42","title":"GPU Runbook"},"excerpt":"<p>Restart DCGM</p>"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: "token", AllowedPrincipals: []string{"group:ops"}})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := client.ListProjects(context.Background())
	if err != nil || len(projects) != 1 || projects[0].Key != "OPS" {
		t.Fatalf("projects=%#v err=%v", projects, err)
	}
	repositories, err := client.ListRepositories(context.Background(), "OPS")
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%#v err=%v", repositories, err)
	}
	ref := source.RepositoryRef{ProjectKey: "confluence", Slug: "ops"}
	files, _ := client.ListFiles(context.Background(), ref, "current")
	content, _ := client.GetFile(context.Background(), ref, "current", files[0].Path)
	if !strings.Contains(string(content), "# GPU Runbook") || !strings.Contains(string(content), "Restart DCGM") {
		t.Fatalf("content=%s", content)
	}
	permissions, _ := client.GetPermissions(context.Background(), ref)
	if permissions[0].Principal != "group:ops" {
		t.Fatalf("permissions=%#v", permissions)
	}
	hits, _ := client.SearchQuery(context.Background(), ref, "current", "GPU", 10)
	if len(hits) != 1 || hits[0].Path != files[0].Path {
		t.Fatalf("hits=%#v", hits)
	}
}

func TestClientRequiresFailClosedACL(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://confluence.example"}); err == nil {
		t.Fatal("missing allowed principals was accepted")
	}
}

func TestClientSupportsExplicitBasicAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
		if r.Header.Get("Authorization") != want {
			t.Errorf("authorization=%q want=%q", r.Header.Get("Authorization"), want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthType: "basic", Username: "alice", Password: "secret", AllowedPrincipals: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ListProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
}

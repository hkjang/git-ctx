package jira

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
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/2/project":
			_, _ = w.Write([]byte(`[{"id":"10","key":"OPS","name":"Operations"}]`))
		case "/rest/api/2/project/OPS":
			_, _ = w.Write([]byte(`{"id":"10","key":"OPS","name":"Operations"}`))
		case "/rest/api/2/search":
			_, _ = w.Write([]byte(`{"total":1,"issues":[{"id":"20","key":"OPS-1","fields":{"summary":"GPU alert","description":"Restart exporter","comment":{"comments":[]}}}]}`))
		case "/rest/api/2/issue/OPS-1":
			_, _ = w.Write([]byte(`{"id":"20","key":"OPS-1","fields":{"summary":"GPU alert","description":"Restart exporter","comment":{"comments":[{"body":"Verify metrics"}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: "token", AllowedPrincipals: []string{"group:ops"}})
	if err != nil {
		t.Fatal(err)
	}
	projects, _ := client.ListProjects(context.Background())
	repositories, _ := client.ListRepositories(context.Background(), "OPS")
	if len(projects) != 1 || len(repositories) != 1 {
		t.Fatalf("projects=%#v repositories=%#v", projects, repositories)
	}
	ref := source.RepositoryRef{ProjectKey: "jira", Slug: "ops"}
	files, _ := client.ListFiles(context.Background(), ref, "current")
	content, _ := client.GetFile(context.Background(), ref, "current", files[0].Path)
	if !strings.Contains(string(content), "Restart exporter") || !strings.Contains(string(content), "Verify metrics") {
		t.Fatalf("content=%s", content)
	}
	hits, _ := client.SearchQuery(context.Background(), ref, "current", "GPU", 10)
	if len(hits) != 1 || hits[0].Path != "issues/OPS-1.md" {
		t.Fatalf("hits=%#v", hits)
	}
}

func TestClientRequiresFailClosedACL(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://jira.example"}); err == nil {
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
		_, _ = w.Write([]byte(`[]`))
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

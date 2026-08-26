package v6

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git-ctx/internal/source"
)

// Bitbucket Server answers an instance-wide code search only when the search
// plugin is installed and enabled. Without it the server says so, and how it
// says so depends on the deployment: 501 from the API, 404 when the endpoint is
// not routed, 403 through a proxy that blocks it, 400 with a message.
//
// Only 404 used to be recognised. Every other way of saying "this instance has
// no code search" was reported to the caller as a failed search, so the
// per-repository fallback — which GitLab has had all along — never ran, and an
// agent was told the search broke when the platform could have answered it the
// slow way.
func TestSearchWithoutThePluginFallsBackInsteadOfFailing(t *testing.T) {
	for _, refusal := range []struct {
		name   string
		status int
		body   string
	}{
		{"not implemented", http.StatusNotImplemented, `{"errors":[{"message":"code search is not enabled on this instance"}]}`},
		{"not routed", http.StatusNotFound, `{"errors":[{"message":"not found"}]}`},
		{"blocked by a proxy", http.StatusForbidden, `{"errors":[{"message":"Repository search is not available"}]}`},
		{"refused with a reason", http.StatusBadRequest, `{"errors":[{"message":"code search is disabled"}]}`},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, refusal.body, refusal.status)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, APIPrefix: "/rest/api/1.0", Token: "t"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SearchGlobalQuery(context.Background(), "settlement", 10)
			if err == nil {
				t.Fatal("a refused search reported success")
			}
			if !errors.Is(err, source.ErrGlobalSearchUnsupported) {
				t.Fatalf("a refusal was reported as a failed search, so nothing falls back to searching repository by repository: %v", err)
			}
		})
	}
}

// A server that tried and broke is not a server without the feature, and
// falling back would hide a real outage behind a slow answer.
func TestSearchThatBreaksIsNotReportedAsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"message":"internal error"}]}`, http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIPrefix: "/rest/api/1.0", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SearchGlobalQuery(context.Background(), "settlement", 10)
	if err == nil {
		t.Fatal("a broken search reported success")
	}
	if errors.Is(err, source.ErrGlobalSearchUnsupported) {
		t.Fatal("a server that broke was reported as a server without the feature")
	}
}

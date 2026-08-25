package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/source"
)

func TestOpenAIRerankMapsScoresByIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Model, Query string
			Documents    []string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "ranker" || len(body.Documents) != 2 {
			t.Fatalf("unexpected body: %#v %v", body, err)
		}
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`))
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "ranker", APIKey: "secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	scores, err := provider.Rerank(context.Background(), "gpu", []string{"a", "b"})
	if err != nil || len(scores) != 2 || scores[0] != 0.1 || scores[1] != 0.9 {
		t.Fatalf("scores=%v err=%v", scores, err)
	}
}

func TestOpenAIRerankRejectsIncompleteResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":1}]}`))
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "ranker", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Rerank(context.Background(), "q", []string{"a", "b"}); err == nil {
		t.Fatal("expected incomplete response error")
	}
}

func TestOpenAIRerankHTTPErrorCarriesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("ranker is loading"))
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "ranker"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Rerank(context.Background(), "q", []string{"document"})
	if source.StatusOf(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "reranker API 503 Service Unavailable: ranker is loading") {
		t.Fatalf("status=%d err=%v", source.StatusOf(err), err)
	}
}

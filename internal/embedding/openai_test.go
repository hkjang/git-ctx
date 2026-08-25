package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git-ctx/internal/source"
)

func TestOpenAICompatibleEmbedding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("request path/auth")
		}
		w.Write([]byte(`{"data":[{"embedding":[3,4]}]}`))
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "embed", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	vector, err := provider.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != 2 || vector[0] < .59 || vector[1] < .79 {
		t.Fatalf("vector=%v", vector)
	}
}

// A rate limited or briefly failing endpoint must not abort an index job, and a
// batch request must return the vectors in input order even when the server
// reports them out of order.
func TestOpenAIBatchEmbeddingRetriesAndKeepsInputOrder(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		var payload struct {
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload.Input) != 2 {
			t.Errorf("expected a two input batch, got %#v", payload.Input)
		}
		w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "embed"})
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := EmbedAll(context.Background(), provider, []string{"first", "second"})
	if err != nil || len(vectors) != 2 {
		t.Fatalf("vectors=%v err=%v", vectors, err)
	}
	if vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("batch results were not aligned with the inputs: %v", vectors)
	}
	if attempts != 2 {
		t.Fatalf("expected one retry after 429, attempts=%d", attempts)
	}
}

// A permanent client error must fail immediately instead of retrying.
func TestOpenAIEmbeddingDoesNotRetryClientErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"unknown model"}`))
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "embed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Fatalf("client errors must not be retried, attempts=%d", attempts)
	}
}

func TestOpenAIEmbeddingHTTPErrorCarriesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("model is loading"))
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "embed"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.(*OpenAI).embedOnce(context.Background(), []string{"hello"})
	if source.StatusOf(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "embedding API 503: model is loading") {
		t.Fatalf("status=%d err=%v", source.StatusOf(err), err)
	}
}

// An embedding endpoint under load answers 429 and names the window. Retrying
// on the local backoff instead burned all three attempts inside that window and
// failed the batch, which fails an index job for a rate limit that had already
// told the client how long to wait.
func TestEmbedBatchWaitsOutTheWindowTheServerNamed(t *testing.T) {
	const window = 2 * time.Second
	var attempts atomic.Int64
	start := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if time.Since(start) < window {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer server.Close()

	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	vectors, err := provider.(BatchEmbedder).EmbedBatch(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedBatch gave up inside the window the server named: %v", err)
	}
	if len(vectors) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vectors))
	}
	if got := attempts.Load(); got > 2 {
		t.Errorf("%d attempts for one named window, want the retry to wait it out", got)
	}
}

// Without a window the client keeps its own backoff.
func TestEmbedBatchFallsBackToLocalBackoff(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer server.Close()

	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	start := time.Now()
	if _, err := provider.(BatchEmbedder).EmbedBatch(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("a 500 without a window waited %s, want the short local backoff", elapsed)
	}
}

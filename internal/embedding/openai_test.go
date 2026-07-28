package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

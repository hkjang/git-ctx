package embedding

import (
	"context"
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

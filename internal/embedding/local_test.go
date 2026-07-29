package embedding

import (
	"strings"
	"testing"
)

func TestEmbeddingRoundTripAndSimilarity(t *testing.T) {
	query := Embed("Kubernetes pod GPU metrics")
	same := Decode(Encode(Embed("Pod GPU metrics on Kubernetes")))
	other := Embed("database transaction rollback")
	if len(query) != Dimensions || len(same) != Dimensions {
		t.Fatal("dimension mismatch")
	}
	if Cosine(query, same) <= Cosine(query, other) {
		t.Fatalf("related=%f unrelated=%f", Cosine(query, same), Cosine(query, other))
	}
}

func TestMetadataIdentityIsStableDatabaseSafeAndRevisionSpecific(t *testing.T) {
	first := (Metadata{Provider: "openai-compatible", Model: "embed-v2", Revision: "https://ai.internal/v1/embeddings"}).Identity()
	same := (Metadata{Provider: "openai-compatible", Model: "embed-v2", Revision: "https://ai.internal/v1/embeddings"}).Identity()
	changed := (Metadata{Provider: "openai-compatible", Model: "embed-v3", Revision: "https://ai.internal/v1/embeddings"}).Identity()
	if first != same || first == changed || !strings.HasPrefix(first, "sha256:") || strings.ContainsRune(first, '\x00') {
		t.Fatalf("first=%q same=%q changed=%q", first, same, changed)
	}
	if got := (Metadata{}).Identity(); got != "" {
		t.Fatalf("empty metadata identity=%q", got)
	}
}

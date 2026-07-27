package embedding

import "testing"

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

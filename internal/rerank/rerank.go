package rerank

import "context"

// Provider scores documents for a query. Scores correspond to documents by index.
// Callers must only pass content that has already passed authorization filters.
type Provider interface {
	Rerank(ctx context.Context, query string, documents []string) ([]float64, error)
}

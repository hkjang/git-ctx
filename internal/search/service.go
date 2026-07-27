package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"git-ctx/internal/embedding"
	"git-ctx/internal/rerank"
	"git-ctx/internal/store"
)

type Config struct {
	KeywordWeight  float64
	VectorWeight   float64
	FinalK         int
	CandidateLimit int
	RerankLimit    int
}
type ConfigLoader func(context.Context) Config
type EmbeddingLoader func(context.Context) embedding.Provider
type RerankerLoader func(context.Context) rerank.Provider
type Service struct {
	store    *store.Store
	load     ConfigLoader
	embedder EmbeddingLoader
	reranker RerankerLoader
}

func (s *Service) SetRerankerLoader(loader RerankerLoader) {
	if loader != nil {
		s.reranker = loader
	}
}

func New(s *store.Store) *Service {
	return &Service{store: s, load: func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 8, CandidateLimit: 5000}
	}, embedder: func(context.Context) embedding.Provider { return embedding.Local{} }}
}
func (s *Service) SetEmbeddingLoader(loader EmbeddingLoader) {
	if loader != nil {
		s.embedder = loader
	}
}
func (s *Service) SetConfigLoader(loader ConfigLoader) {
	if loader != nil {
		s.load = loader
	}
}

type Library struct {
	Name, ID, Description, Reputation string
	Snippets                          int
	Versions                          []string
}

func (s *Service) Resolve(ctx context.Context, principals []string, name, query string) ([]Library, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(query) == "" {
		return nil, errors.New("libraryName and query are required")
	}
	if len(principals) == 0 {
		return []Library{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := make([]any, len(principals))
	for i := range principals {
		args[i] = principals[i]
	}
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`
SELECT r.id,r.name,r.library_id,r.description,r.reputation
FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.enabled=1 AND (p.principal IN (`+placeholders+`) OR p.principal='*')`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		Library
		score  int
		repoID string
	}
	var found []scored
	terms := strings.Fields(strings.ToLower(name + " " + query))
	for rows.Next() {
		var x scored
		if err := rows.Scan(&x.repoID, &x.Name, &x.ID, &x.Description, &x.Reputation); err != nil {
			return nil, err
		}
		hay := strings.ToLower(x.Name + " " + x.ID + " " + x.Description)
		for _, term := range terms {
			if strings.Contains(hay, term) {
				x.score++
			}
		}
		if strings.EqualFold(x.Name, name) || strings.HasSuffix(x.ID, "/"+strings.ToLower(name)) {
			x.score += 10
		}
		cr, _ := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT DISTINCT ref_name FROM document_chunks WHERE repository_id=?`), x.repoID)
		for cr != nil && cr.Next() {
			var ref string
			_ = cr.Scan(&ref)
			x.Versions = append(x.Versions, ref)
		}
		if cr != nil {
			cr.Close()
		}
		_ = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=?`), x.repoID).Scan(&x.Snippets)
		if x.score > 0 {
			found = append(found, x)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].score > found[j].score })
	if len(found) > 10 {
		found = found[:10]
	}
	out := make([]Library, len(found))
	for i := range found {
		out[i] = found[i].Library
	}
	return out, rows.Err()
}

func (s *Service) Query(ctx context.Context, principals []string, libraryID, query string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(libraryID, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || strings.TrimSpace(query) == "" {
		return "", errors.New("libraryId must use /organization/project[/version] and query is required")
	}
	baseID := "/" + parts[0] + "/" + parts[1]
	if len(principals) == 0 {
		return "", errors.New("library is unavailable or access is denied")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := []any{baseID}
	for _, principal := range principals {
		args = append(args, principal)
	}
	var repoID, name, defaultRef, sourceType string
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`
SELECT r.id,r.name,r.default_branch,r.source_type FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id
WHERE r.library_id=? AND r.enabled=1 AND (p.principal IN (`+placeholders+`) OR p.principal='*') LIMIT 1`), args...).Scan(&repoID, &name, &defaultRef, &sourceType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("library is unavailable or access is denied")
	}
	if err != nil {
		return "", err
	}
	ref := defaultRef
	if len(parts) == 3 {
		ref = parts[2]
	}
	terms := unique(embedding.Tokens(query))
	cfg := s.load(ctx)
	if cfg.KeywordWeight < 0 {
		cfg.KeywordWeight = 1
	}
	if cfg.VectorWeight < 0 {
		cfg.VectorWeight = .35
	}
	if cfg.FinalK < 1 || cfg.FinalK > 50 {
		cfg.FinalK = 8
	}
	if cfg.CandidateLimit < 10 || cfg.CandidateLimit > 20000 {
		cfg.CandidateLimit = 5000
	}
	if cfg.RerankLimit < 1 || cfg.RerankLimit > 100 {
		cfg.RerankLimit = 30
	}
	candidateSQL := `SELECT content,file_path,line_start,line_end,commit_id,heading,embedding FROM document_chunks WHERE repository_id=? AND ref_name=?`
	args = []any{repoID, ref}
	if len(terms) > 0 {
		candidateSQL += " AND ("
		limit := min(len(terms), 5)
		for n, term := range terms[:limit] {
			if n > 0 {
				candidateSQL += " OR "
			}
			candidateSQL += "LOWER(heading || ' ' || content) LIKE ?"
			args = append(args, "%"+term+"%")
		}
		candidateSQL += ")"
	}
	candidateSQL += fmt.Sprintf(" LIMIT %d", cfg.CandidateLimit)
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(candidateSQL), args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type hit struct {
		content, path, commit, heading string
		start, end                     int
		score                          float64
		tokens                         []string
		vector                         []byte
	}
	var hits []hit
	df := map[string]int{}
	totalLength := 0
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.content, &h.path, &h.start, &h.end, &h.commit, &h.heading, &h.vector); err != nil {
			return "", err
		}
		h.tokens = embedding.Tokens(h.heading + " " + h.content)
		totalLength += len(h.tokens)
		present := map[string]bool{}
		for _, token := range h.tokens {
			present[token] = true
		}
		for _, term := range terms {
			if present[term] {
				df[term]++
			}
		}
		hits = append(hits, h)
	}
	avgLength := float64(totalLength) / math.Max(1, float64(len(hits)))
	queryVector, embedErr := s.embedder(ctx).Embed(ctx, query)
	if embedErr != nil {
		queryVector = embedding.Embed(query)
	}
	filtered := hits[:0]
	for n := range hits {
		tf := map[string]int{}
		for _, token := range hits[n].tokens {
			tf[token]++
		}
		var bm25 float64
		for _, term := range terms {
			frequency := float64(tf[term])
			if frequency == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(hits)-df[term])+0.5)/(float64(df[term])+0.5))
			lengthNorm := 1 - 0.75 + 0.75*float64(len(hits[n].tokens))/avgLength
			bm25 += idf * (frequency * 2.2) / (frequency + 1.2*lengthNorm)
		}
		vectorScore := embedding.Cosine(queryVector, embedding.Decode(hits[n].vector))
		hits[n].score = cfg.KeywordWeight*bm25 + cfg.VectorWeight*math.Max(0, vectorScore)
		if bm25 > 0 || vectorScore > 0.18 {
			filtered = append(filtered, hits[n])
		}
	}
	hits = filtered
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if s.reranker != nil && len(hits) > 0 {
		limit := min(len(hits), cfg.RerankLimit)
		documents := make([]string, limit)
		for i := 0; i < limit; i++ {
			documents[i] = hits[i].heading + "\n" + hits[i].content
		}
		if provider := s.reranker(ctx); provider != nil {
			if scores, rerankErr := provider.Rerank(ctx, query, documents); rerankErr == nil && len(scores) == limit {
				for i := 0; i < limit; i++ {
					hits[i].score = scores[i]
				}
				sort.SliceStable(hits[:limit], func(i, j int) bool { return hits[i].score > hits[j].score })
			}
		}
	}
	if len(hits) > cfg.FinalK {
		hits = hits[:cfg.FinalK]
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No indexed documentation matched the query in %s at %s. Try another term or version.", name, ref), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	for _, h := range hits {
		fmt.Fprintf(&b, "## %s\n\n%s\n\nSource: `%s://%s@%s/%s#L%d-L%d`\n\n", h.heading, h.content, sourceType, strings.TrimPrefix(baseID, "/"), h.commit, h.path, h.start, h.end)
	}
	return b.String(), nil
}
func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

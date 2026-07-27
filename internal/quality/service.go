package quality

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"git-ctx/internal/store"
)

type Searcher interface {
	Query(context.Context, []string, string, string) (string, error)
}

type Service struct {
	store  *store.Store
	search Searcher
}

type Case struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	LibraryID       string    `json:"libraryId"`
	Query           string    `json:"query"`
	Principals      []string  `json:"principals"`
	RelevantSources []string  `json:"relevantSources"`
	Enabled         bool      `json:"enabled"`
	CreatedBy       string    `json:"createdBy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Thresholds struct {
	Recall float64 `json:"minimumRecall"`
	MRR    float64 `json:"minimumMrr"`
	NDCG   float64 `json:"minimumNdcg"`
}

type Run struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	TopK         int        `json:"topK"`
	CaseCount    int        `json:"caseCount"`
	PassedCount  int        `json:"passedCount"`
	RecallAtK    float64    `json:"recallAtK"`
	MRR          float64    `json:"mrr"`
	NDCGAtK      float64    `json:"ndcgAtK"`
	Thresholds   Thresholds `json:"thresholds"`
	CreatedBy    string     `json:"createdBy"`
	ErrorMessage string     `json:"errorMessage"`
	CreatedAt    time.Time  `json:"createdAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

type Result struct {
	CaseID           string   `json:"caseId"`
	CaseName         string   `json:"caseName"`
	RetrievedSources []string `json:"retrievedSources"`
	RecallAtK        float64  `json:"recallAtK"`
	ReciprocalRank   float64  `json:"reciprocalRank"`
	NDCGAtK          float64  `json:"ndcgAtK"`
	DurationMS       int64    `json:"durationMs"`
	ErrorMessage     string   `json:"errorMessage"`
}

func New(s *store.Store, search Searcher) *Service { return &Service{store: s, search: search} }

func (s *Service) CreateCase(ctx context.Context, c Case, actor string) (Case, error) {
	c.Name, c.LibraryID, c.Query = strings.TrimSpace(c.Name), strings.TrimSpace(c.LibraryID), strings.TrimSpace(c.Query)
	if c.Name == "" || c.Query == "" || !validLibraryID(c.LibraryID) {
		return Case{}, errors.New("name, query, and a valid /organization/project[/version] libraryId are required")
	}
	c.Principals = normalized(c.Principals)
	c.RelevantSources = normalized(c.RelevantSources)
	if len(c.Principals) == 0 || len(c.RelevantSources) == 0 {
		return Case{}, errors.New("at least one ACL principal and relevant source are required")
	}
	for _, source := range c.RelevantSources {
		if strings.Contains(source, "#") || strings.HasPrefix(source, "/") || strings.Contains(source, "\\") {
			return Case{}, errors.New("relevantSources must be repository-relative paths without line fragments")
		}
	}
	var err error
	c.ID, err = newID()
	if err != nil {
		return Case{}, err
	}
	c.Enabled, c.CreatedBy, c.CreatedAt, c.UpdatedAt = true, actor, time.Now().UTC(), time.Now().UTC()
	principals, _ := json.Marshal(c.Principals)
	relevant, _ := json.Marshal(c.RelevantSources)
	_, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO quality_benchmark_cases(id,name,library_id,query,principals_json,relevant_sources_json,enabled,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`), c.ID, c.Name, c.LibraryID, c.Query, string(principals), string(relevant), 1, actor, c.CreatedAt, c.UpdatedAt)
	return c, err
}

func (s *Service) ListCases(ctx context.Context) ([]Case, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id,name,library_id,query,principals_json,relevant_sources_json,enabled,created_by,created_at,updated_at FROM quality_benchmark_cases ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Case, 0)
	for rows.Next() {
		var c Case
		var principals, relevant string
		if err := rows.Scan(&c.ID, &c.Name, &c.LibraryID, &c.Query, &principals, &relevant, &c.Enabled, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(principals), &c.Principals) != nil || json.Unmarshal([]byte(relevant), &c.RelevantSources) != nil {
			return nil, errors.New("invalid benchmark case JSON")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) DeleteCase(ctx context.Context, id string) error {
	result, err := s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM quality_benchmark_cases WHERE id=?`), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) Run(ctx context.Context, actor string, topK int, thresholds Thresholds) (run Run, err error) {
	if topK < 1 || topK > 50 || !validScore(thresholds.Recall) || !validScore(thresholds.MRR) || !validScore(thresholds.NDCG) {
		return Run{}, errors.New("topK must be 1..50 and thresholds must be between 0 and 1")
	}
	cases, err := s.ListCases(ctx)
	if err != nil {
		return Run{}, err
	}
	enabled := cases[:0]
	for _, c := range cases {
		if c.Enabled {
			enabled = append(enabled, c)
		}
	}
	if len(enabled) == 0 {
		return Run{}, errors.New("no enabled benchmark cases")
	}
	runID, err := newID()
	if err != nil {
		return Run{}, err
	}
	run = Run{ID: runID, Status: "running", TopK: topK, CaseCount: len(enabled), Thresholds: thresholds, CreatedBy: actor, CreatedAt: time.Now().UTC()}
	_, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO quality_benchmark_runs(id,status,top_k,case_count,minimum_recall,minimum_mrr,minimum_ndcg,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?)`), run.ID, run.Status, topK, run.CaseCount, thresholds.Recall, thresholds.MRR, thresholds.NDCG, actor, run.CreatedAt)
	if err != nil {
		return Run{}, err
	}
	defer func() {
		if err != nil {
			run.Status, run.ErrorMessage = "failed", truncate(err.Error(), 500)
			_, _ = s.store.DB.ExecContext(context.Background(), s.store.Rebind(`UPDATE quality_benchmark_runs SET status='failed',error_message=?,completed_at=? WHERE id=?`), run.ErrorMessage, time.Now().UTC(), run.ID)
		}
	}()
	var recall, mrr, ndcg float64
	queryFailures := 0
	for _, c := range enabled {
		started := time.Now()
		text, queryErr := s.search.Query(ctx, c.Principals, c.LibraryID, c.Query)
		result := Result{CaseID: c.ID, CaseName: c.Name, DurationMS: time.Since(started).Milliseconds()}
		if queryErr != nil {
			result.ErrorMessage = truncate(queryErr.Error(), 500)
			queryFailures++
		} else {
			result.RetrievedSources = extractSources(text, topK)
			result.RecallAtK, result.ReciprocalRank, result.NDCGAtK = metrics(result.RetrievedSources, c.RelevantSources, topK)
		}
		if result.ErrorMessage == "" && result.RecallAtK >= thresholds.Recall && result.ReciprocalRank >= thresholds.MRR && result.NDCGAtK >= thresholds.NDCG {
			run.PassedCount++
		}
		recall, mrr, ndcg = recall+result.RecallAtK, mrr+result.ReciprocalRank, ndcg+result.NDCGAtK
		encoded, _ := json.Marshal(result.RetrievedSources)
		_, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO quality_benchmark_results(run_id,case_id,retrieved_sources_json,recall_at_k,reciprocal_rank,ndcg_at_k,duration_ms,error_message) VALUES(?,?,?,?,?,?,?,?)`), run.ID, c.ID, string(encoded), result.RecallAtK, result.ReciprocalRank, result.NDCGAtK, result.DurationMS, result.ErrorMessage)
		if err != nil {
			return run, err
		}
	}
	n := float64(len(enabled))
	run.RecallAtK, run.MRR, run.NDCGAtK = recall/n, mrr/n, ndcg/n
	run.Status = "passed"
	if queryFailures > 0 || run.RecallAtK < thresholds.Recall || run.MRR < thresholds.MRR || run.NDCGAtK < thresholds.NDCG {
		run.Status = "regressed"
	}
	completed := time.Now().UTC()
	run.CompletedAt = &completed
	_, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE quality_benchmark_runs SET status=?,passed_count=?,recall_at_k=?,mrr=?,ndcg_at_k=?,completed_at=? WHERE id=?`), run.Status, run.PassedCount, run.RecallAtK, run.MRR, run.NDCGAtK, completed, run.ID)
	return run, err
}

func (s *Service) ListRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id,status,top_k,case_count,passed_count,recall_at_k,mrr,ndcg_at_k,minimum_recall,minimum_mrr,minimum_ndcg,created_by,error_message,created_at,completed_at FROM quality_benchmark_runs ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0)
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Status, &r.TopK, &r.CaseCount, &r.PassedCount, &r.RecallAtK, &r.MRR, &r.NDCGAtK, &r.Thresholds.Recall, &r.Thresholds.MRR, &r.Thresholds.NDCG, &r.CreatedBy, &r.ErrorMessage, &r.CreatedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Results(ctx context.Context, runID string) ([]Result, error) {
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT x.case_id,c.name,x.retrieved_sources_json,x.recall_at_k,x.reciprocal_rank,x.ndcg_at_k,x.duration_ms,x.error_message FROM quality_benchmark_results x JOIN quality_benchmark_cases c ON c.id=x.case_id WHERE x.run_id=? ORDER BY c.name`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Result, 0)
	for rows.Next() {
		var x Result
		var sources string
		if err := rows.Scan(&x.CaseID, &x.CaseName, &sources, &x.RecallAtK, &x.ReciprocalRank, &x.NDCGAtK, &x.DurationMS, &x.ErrorMessage); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(sources), &x.RetrievedSources) != nil {
			return nil, errors.New("invalid benchmark result JSON")
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

var sourcePattern = regexp.MustCompile("(?m)Source: `(?:bitbucket|gitlab)://[^@`]+@[^/`]+/([^#`]+)#L[0-9]+-L[0-9]+`")

func extractSources(text string, topK int) []string {
	matches := sourcePattern.FindAllStringSubmatch(text, topK)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

func metrics(retrieved, relevant []string, topK int) (float64, float64, float64) {
	wanted := map[string]bool{}
	for _, x := range relevant {
		wanted[x] = true
	}
	seen, hits, rr, dcg := map[string]bool{}, 0, 0.0, 0.0
	for i, source := range retrieved {
		if i >= topK {
			break
		}
		if wanted[source] && !seen[source] {
			seen[source] = true
			hits++
			if rr == 0 {
				rr = 1 / float64(i+1)
			}
			dcg += 1 / math.Log2(float64(i+2))
		}
	}
	ideal, limit := 0.0, min(len(relevant), topK)
	for i := 0; i < limit; i++ {
		ideal += 1 / math.Log2(float64(i+2))
	}
	ndcg := 0.0
	if ideal > 0 {
		ndcg = dcg / ideal
	}
	return float64(hits) / float64(len(relevant)), rr, ndcg
}

func validLibraryID(id string) bool {
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	return strings.HasPrefix(id, "/") && (len(parts) == 2 || len(parts) == 3) && parts[0] != "" && parts[1] != ""
}
func validScore(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 1 }
func normalized(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

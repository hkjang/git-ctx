package app

import (
	"database/sql"
	"errors"
	"net/http"

	"git-ctx/internal/auth"
	"git-ctx/internal/quality"
)

// Search quality benchmark cases and runs.

func (a *App) listQualityCases(w http.ResponseWriter, r *http.Request) {
	cases, err := a.quality.ListCases(r.Context())
	if err != nil {
		problem(w, 500, "quality_cases_failed", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, cases)
}
func (a *App) createQualityCase(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var input quality.Case
	if err := decode(r, &input); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	created, err := a.quality.CreateCase(r.Context(), input, p.UserID)
	if err != nil {
		a.audit(r, p, "quality.case.create", "quality_case", "", "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 400, "quality_case_invalid", err.Error())
		return
	}
	a.audit(r, p, "quality.case.create", "quality_case", created.ID, "success", map[string]any{"libraryId": created.LibraryID, "relevantSourceCount": len(created.RelevantSources)})
	jsonOut(w, http.StatusCreated, created)
}
func (a *App) deleteQualityCase(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id := r.PathValue("id")
	if err := a.quality.DeleteCase(r.Context(), id); err != nil {
		status := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		problem(w, status, "quality_case_delete_failed", "The case does not exist or is referenced by a benchmark run")
		return
	}
	a.audit(r, p, "quality.case.delete", "quality_case", id, "success", nil)
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) listQualityRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := a.quality.ListRuns(r.Context())
	if err != nil {
		problem(w, 500, "quality_runs_failed", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, runs)
}
func (a *App) runQualityBenchmark(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var input struct {
		TopK int `json:"topK"`
		quality.Thresholds
	}
	if err := decode(r, &input); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	run, err := a.quality.Run(r.Context(), p.UserID, input.TopK, input.Thresholds)
	if err != nil {
		a.audit(r, p, "quality.run", "quality_run", run.ID, "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 400, "quality_run_failed", err.Error())
		return
	}
	a.audit(r, p, "quality.run", "quality_run", run.ID, run.Status, map[string]any{"recallAtK": run.RecallAtK, "mrr": run.MRR, "ndcgAtK": run.NDCGAtK, "caseCount": run.CaseCount})
	jsonOut(w, http.StatusCreated, run)
}
func (a *App) qualityResults(w http.ResponseWriter, r *http.Request) {
	results, err := a.quality.Results(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 500, "quality_results_failed", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, results)
}

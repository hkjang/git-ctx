package app

import (
	"encoding/json"
	"net/http"
	"strings"

	"git-ctx/internal/auth"
)

// Admin console helpers that exercise the search tools directly.

func (a *App) testResolve(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "resolve-library-id") {
		problem(w, 403, "forbidden", "API key is not allowed to call resolve-library-id")
		return
	}
	var in struct {
		LibraryName string `json:"libraryName"`
		Query       string `json:"query"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "libraryName and query are required")
		return
	}
	items, err := a.search.Resolve(r.Context(), searchPrincipals(p), in.LibraryName, in.Query)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		filtered := items[:0]
		for _, item := range items {
			if repositoryAllowed(item.ID, p.AllowedRepositories) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	jsonOut(w, 200, map[string]any{"libraries": items})
}
func (a *App) testQuery(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "query-docs") {
		problem(w, 403, "forbidden", "API key is not allowed to call query-docs")
		return
	}
	var in struct {
		LibraryID string `json:"libraryId"`
		Query     string `json:"query"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "libraryId and query are required")
		return
	}
	if p.KeyID != "" && !repositoryAllowed(baseLibraryID(in.LibraryID), p.AllowedRepositories) {
		problem(w, 403, "forbidden", "Library is unavailable or access is denied")
		return
	}
	text, err := a.search.Query(r.Context(), searchPrincipals(p), in.LibraryID, in.Query)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
}

func (a *App) testSearchCode(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "search-code") {
		problem(w, 403, "forbidden", "API key is not allowed to call search-code")
		return
	}
	var in struct {
		Query      string `json:"query"`
		SourceType string `json:"sourceType"`
		Project    string `json:"project"`
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		Limit      int    `json:"limit"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Query) == "" {
		problem(w, 400, "invalid_request", "query is required")
		return
	}
	result, err := a.search.SearchCode(r.Context(), searchPrincipals(p), in.Query, in.SourceType, in.Project, in.Repository, in.Ref, in.Limit)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		repositories := result.Repositories[:0]
		for _, item := range result.Repositories {
			if repositoryAllowed(item.LibraryID, p.AllowedRepositories) {
				repositories = append(repositories, item)
			}
		}
		result.Repositories = repositories
		hits := result.Hits[:0]
		for _, hit := range result.Hits {
			if repositoryAllowed(hit.LibraryID, p.AllowedRepositories) {
				hits = append(hits, hit)
			}
		}
		result.Hits = hits
	}
	jsonOut(w, 200, result)
}
func (a *App) testFindFile(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "find-file") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call find-file")
		return
	}
	var in struct {
		Pattern    string `json:"pattern"`
		LibraryID  string `json:"libraryId"`
		SourceType string `json:"sourceType"`
		Project    string `json:"project"`
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		Limit      int    `json:"limit"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Pattern) == "" {
		problem(w, http.StatusBadRequest, "invalid_request", "pattern is required")
		return
	}
	result, err := a.search.FindFiles(r.Context(), searchPrincipals(p), in.Pattern, in.LibraryID, in.SourceType, in.Project, in.Repository, in.Ref, in.Limit)
	if err != nil {
		problem(w, http.StatusBadRequest, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		files := result.Files[:0]
		for _, item := range result.Files {
			if repositoryAllowed(item.LibraryID, p.AllowedRepositories) {
				files = append(files, item)
			}
		}
		result.Files = files
	}
	jsonOut(w, http.StatusOK, result)
}

func (a *App) testReadFile(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "read-file") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call read-file")
		return
	}
	var in struct {
		Path       string `json:"path"`
		LibraryID  string `json:"libraryId"`
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		StartLine  int    `json:"startLine"`
		EndLine    int    `json:"endLine"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Path) == "" {
		problem(w, http.StatusBadRequest, "invalid_request", "path is required")
		return
	}
	file, err := a.search.ReadFile(r.Context(), searchPrincipals(p), in.LibraryID, in.Repository, in.Path, in.Ref, in.StartLine, in.EndLine)
	if err != nil {
		problem(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}
	if p.KeyID != "" && !repositoryAllowed(file.LibraryID, p.AllowedRepositories) {
		problem(w, http.StatusForbidden, "forbidden", "File is unavailable or access is denied")
		return
	}
	jsonOut(w, http.StatusOK, file)
}

func (a *App) testSemanticSearch(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "search-semantic") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call search-semantic")
		return
	}
	var in struct {
		Query      string `json:"query"`
		LibraryID  string `json:"libraryId"`
		SourceType string `json:"sourceType"`
		Limit      int    `json:"limit"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Query) == "" {
		problem(w, http.StatusBadRequest, "invalid_request", "query is required")
		return
	}
	result, err := a.search.SemanticSearch(r.Context(), searchPrincipals(p), in.Query, in.LibraryID, in.SourceType, in.Limit)
	if err != nil {
		problem(w, http.StatusBadRequest, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		hits := result.Hits[:0]
		for _, item := range result.Hits {
			if repositoryAllowed(item.LibraryID, p.AllowedRepositories) {
				hits = append(hits, item)
			}
		}
		result.Hits = hits
	}
	jsonOut(w, http.StatusOK, result)
}

// testDependencyUsage answers the inventory question from the console, so an
// operator handling an advisory does not have to reach for an MCP client.
func (a *App) testDependencyUsage(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "find-dependency-usage") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call find-dependency-usage")
		return
	}
	var in struct {
		Name       string `json:"name"`
		Ecosystem  string `json:"ecosystem"`
		SourceType string `json:"sourceType"`
		Limit      int    `json:"limit"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Name) == "" {
		problem(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	result, err := a.search.FindDependencyUsage(r.Context(), searchPrincipals(p), in.Name, in.Ecosystem, in.SourceType, in.Limit)
	if err != nil {
		problem(w, http.StatusBadRequest, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		users := result.Users[:0]
		for _, item := range result.Users {
			if repositoryAllowed(item.LibraryID, p.AllowedRepositories) {
				users = append(users, item)
			}
		}
		result.Users = users
	}
	jsonOut(w, http.StatusOK, result)
}

func (a *App) testDependents(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "find-dependents") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call find-dependents")
		return
	}
	var in struct {
		Target     string `json:"target"`
		SourceType string `json:"sourceType"`
		Limit      int    `json:"limit"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Target) == "" {
		problem(w, http.StatusBadRequest, "invalid_request", "target is required")
		return
	}
	result, err := a.search.FindDependents(r.Context(), searchPrincipals(p), in.Target, in.SourceType, in.Limit)
	if err != nil {
		problem(w, http.StatusBadRequest, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		dependents := result.Dependents[:0]
		for _, item := range result.Dependents {
			if repositoryAllowed(item.LibraryID, p.AllowedRepositories) {
				dependents = append(dependents, item)
			}
		}
		result.Dependents = dependents
	}
	jsonOut(w, http.StatusOK, result)
}

func (a *App) testMergeRequests(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "search-merge-requests") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call search-merge-requests")
		return
	}
	var in struct {
		Query      string `json:"query"`
		LibraryID  string `json:"libraryId"`
		Repository string `json:"repository"`
		State      string `json:"state"`
		Limit      int    `json:"limit"`
	}
	if decode(r, &in) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "Invalid JSON")
		return
	}
	result, err := a.search.SearchChangeRequests(r.Context(), searchPrincipals(p), in.Query, in.LibraryID, in.Repository, in.State, in.Limit)
	if err != nil {
		problem(w, http.StatusBadRequest, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		requests := result.Requests[:0]
		for _, item := range result.Requests {
			if repositoryAllowed(item.LibraryID, p.AllowedRepositories) {
				requests = append(requests, item)
			}
		}
		result.Requests = requests
	}
	jsonOut(w, http.StatusOK, result)
}

func (a *App) testFileHistory(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "get-file-history") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call get-file-history")
		return
	}
	var in struct {
		Path       string `json:"path"`
		LibraryID  string `json:"libraryId"`
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		Limit      int    `json:"limit"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Path) == "" {
		problem(w, http.StatusBadRequest, "invalid_request", "path is required")
		return
	}
	history, err := a.search.FileHistory(r.Context(), searchPrincipals(p), in.LibraryID, in.Repository, in.Path, in.Ref, in.Limit)
	if err != nil {
		problem(w, http.StatusBadRequest, "history_failed", err.Error())
		return
	}
	if p.KeyID != "" && !repositoryAllowed(history.LibraryID, p.AllowedRepositories) {
		problem(w, http.StatusForbidden, "forbidden", "File is unavailable or access is denied")
		return
	}
	jsonOut(w, http.StatusOK, history)
}

func (a *App) testDirectory(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "list-directory") {
		problem(w, http.StatusForbidden, "forbidden", "API key is not allowed to call list-directory")
		return
	}
	var in struct {
		Path       string `json:"path"`
		LibraryID  string `json:"libraryId"`
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
	}
	if decode(r, &in) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "Invalid JSON")
		return
	}
	listing, err := a.search.ListDirectory(r.Context(), searchPrincipals(p), in.LibraryID, in.Repository, in.Path, in.Ref)
	if err != nil {
		problem(w, http.StatusBadRequest, "listing_failed", err.Error())
		return
	}
	if p.KeyID != "" && !repositoryAllowed(listing.LibraryID, p.AllowedRepositories) {
		problem(w, http.StatusForbidden, "forbidden", "Directory is unavailable or access is denied")
		return
	}
	jsonOut(w, http.StatusOK, listing)
}

func (a *App) testRepositoryMap(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "get-repository-map") {
		problem(w, 403, "forbidden", "API key is not allowed to call get-repository-map")
		return
	}
	var in struct {
		LibraryID string `json:"libraryId"`
		Ref       string `json:"ref"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.LibraryID) == "" {
		problem(w, 400, "invalid_request", "libraryId is required")
		return
	}
	if p.KeyID != "" && !repositoryAllowed(baseLibraryID(in.LibraryID), p.AllowedRepositories) {
		problem(w, 403, "forbidden", "Library is unavailable or access is denied")
		return
	}
	item, err := a.search.RepositoryMap(r.Context(), searchPrincipals(p), in.LibraryID, in.Ref)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	var summary any
	_ = json.Unmarshal([]byte(item.SummaryJSON), &summary)
	jsonOut(w, 200, map[string]any{"libraryId": item.LibraryID, "ref": item.Ref, "commitId": item.CommitID, "summary": summary, "conventions": item.Conventions})
}
func (a *App) testSymbols(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "find-symbol") {
		problem(w, 403, "forbidden", "API key is not allowed to call find-symbol")
		return
	}
	var in struct {
		LibraryID string `json:"libraryId"`
		Ref       string `json:"ref"`
		Query     string `json:"query"`
		Kind      string `json:"kind"`
		Limit     int    `json:"limit"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Query) == "" {
		problem(w, 400, "invalid_request", "query is required")
		return
	}
	if p.KeyID != "" && in.LibraryID != "" && !repositoryAllowed(baseLibraryID(in.LibraryID), p.AllowedRepositories) {
		problem(w, 403, "forbidden", "Library is unavailable or access is denied")
		return
	}
	items, err := a.search.FindSymbols(r.Context(), searchPrincipals(p), in.LibraryID, in.Ref, in.Query, in.Kind, in.Limit)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	if p.KeyID != "" && len(p.AllowedRepositories) > 0 {
		filtered := items[:0]
		for _, item := range items {
			if repositoryAllowed(item.LibraryID, p.AllowedRepositories) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	jsonOut(w, 200, map[string]any{"symbols": items})
}
func (a *App) testSymbolContext(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "get-symbol-context") {
		problem(w, 403, "forbidden", "API key is not allowed to call get-symbol-context")
		return
	}
	var in struct {
		LibraryID string `json:"libraryId"`
		Ref       string `json:"ref"`
		Symbol    string `json:"symbol"`
	}
	if decode(r, &in) != nil || in.LibraryID == "" || in.Symbol == "" {
		problem(w, 400, "invalid_request", "libraryId and symbol are required")
		return
	}
	if p.KeyID != "" && !repositoryAllowed(baseLibraryID(in.LibraryID), p.AllowedRepositories) {
		problem(w, 403, "forbidden", "Library is unavailable or access is denied")
		return
	}
	item, err := a.search.SymbolContext(r.Context(), searchPrincipals(p), in.LibraryID, in.Ref, in.Symbol)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, item)
}
func (a *App) testDependencies(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "trace-dependencies") {
		problem(w, 403, "forbidden", "API key is not allowed to call trace-dependencies")
		return
	}
	var in struct {
		LibraryID string `json:"libraryId"`
		Ref       string `json:"ref"`
		Symbol    string `json:"symbol"`
		Limit     int    `json:"limit"`
	}
	if decode(r, &in) != nil || in.LibraryID == "" || in.Symbol == "" {
		problem(w, 400, "invalid_request", "libraryId and symbol are required")
		return
	}
	if p.KeyID != "" && !repositoryAllowed(baseLibraryID(in.LibraryID), p.AllowedRepositories) {
		problem(w, 403, "forbidden", "Library is unavailable or access is denied")
		return
	}
	items, err := a.search.TraceDependencies(r.Context(), searchPrincipals(p), in.LibraryID, in.Ref, in.Symbol, in.Limit)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"dependencies": items})
}
func (a *App) testCompareRefs(w http.ResponseWriter, r *http.Request) {
	a.refAnalysis(w, r, false)
}
func (a *App) testChangeImpact(w http.ResponseWriter, r *http.Request) {
	a.refAnalysis(w, r, true)
}
func (a *App) refAnalysis(w http.ResponseWriter, r *http.Request, impact bool) {
	p, _ := auth.FromContext(r.Context())
	scope := "compare-refs"
	if impact {
		scope = "get-change-impact"
	}
	if p.KeyID != "" && !stringContains(p.Scopes, scope) {
		problem(w, 403, "forbidden", "API key is not allowed to call "+scope)
		return
	}
	var in struct {
		LibraryID string `json:"libraryId"`
		BaseRef   string `json:"baseRef"`
		HeadRef   string `json:"headRef"`
		Limit     int    `json:"limit"`
	}
	if decode(r, &in) != nil || in.LibraryID == "" || in.BaseRef == "" || in.HeadRef == "" {
		problem(w, 400, "invalid_request", "libraryId, baseRef and headRef are required")
		return
	}
	if p.KeyID != "" && !repositoryAllowed(baseLibraryID(in.LibraryID), p.AllowedRepositories) {
		problem(w, 403, "forbidden", "Library is unavailable or access is denied")
		return
	}
	if impact {
		item, err := a.search.ChangeImpact(r.Context(), searchPrincipals(p), in.LibraryID, in.BaseRef, in.HeadRef, in.Limit)
		if err != nil {
			problem(w, 400, "search_failed", err.Error())
			return
		}
		jsonOut(w, 200, item)
		return
	}
	item, err := a.search.CompareRefs(r.Context(), searchPrincipals(p), in.LibraryID, in.BaseRef, in.HeadRef)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, item)
}
func (a *App) testContextPack(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "get-context-pack") {
		problem(w, 403, "forbidden", "API key is not allowed to call get-context-pack")
		return
	}
	var in struct {
		Pack  string `json:"pack"`
		Query string `json:"query"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "pack and query are required")
		return
	}
	item, err := a.search.ContextPack(r.Context(), searchPrincipals(p), in.Pack, in.Query)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, item)
}
func (a *App) testRunbooks(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "find-runbook") {
		problem(w, 403, "forbidden", "API key is not allowed to call find-runbook")
		return
	}
	var in struct {
		LibraryID string `json:"libraryId"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "query is required")
		return
	}
	if p.KeyID != "" && in.LibraryID != "" && !repositoryAllowed(baseLibraryID(in.LibraryID), p.AllowedRepositories) {
		problem(w, 403, "forbidden", "Library is unavailable or access is denied")
		return
	}
	items, err := a.search.FindRunbooks(r.Context(), searchPrincipals(p), in.LibraryID, in.Query, in.Limit)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"runbooks": items})
}
func (a *App) testContextExport(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.KeyID != "" && !stringContains(p.Scopes, "export-context") {
		problem(w, 403, "forbidden", "API key is not allowed to call export-context")
		return
	}
	var in struct {
		LibraryIDs []string `json:"libraryIds"`
		Query      string   `json:"query"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "libraryIds and query are required")
		return
	}
	if p.KeyID != "" {
		for _, id := range in.LibraryIDs {
			if !repositoryAllowed(baseLibraryID(id), p.AllowedRepositories) {
				problem(w, 403, "forbidden", "Context is unavailable or access is denied")
				return
			}
		}
	}
	content, err := a.search.ExportContext(r.Context(), searchPrincipals(p), in.LibraryIDs, in.Query)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, map[string]string{"content": content})
}
func baseLibraryID(id string) string {
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(id), "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return "/" + parts[0] + "/" + parts[1]
}

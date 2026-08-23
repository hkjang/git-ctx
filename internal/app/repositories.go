package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git-ctx/internal/auth"
	bitbucketv6 "git-ctx/internal/bitbucket/v6"
	confluencesource "git-ctx/internal/confluence"
	gitlabsource "git-ctx/internal/gitlab"
	"git-ctx/internal/indexer"
	jirasource "git-ctx/internal/jira"
	"git-ctx/internal/source"
)

// Repository registration, indexing jobs and the source adapters behind them.

// sourceAdapter returns the adapter for one source type, reusing the one that
// is already built.
//
// It used to construct a new client — and with it a new TLS transport and
// connection pool — on every call. A single code search fans out over up to
// twenty-five repositories in parallel and the instance-wide fallback over
// hundreds, so one MCP call meant hundreds of handshakes to the same server and
// hundreds of setting decryptions. The adapter is cached per source and
// rebuilt only when the setting version changes.
func (a *App) sourceAdapter(ctx context.Context, sourceType string) (source.RepositorySource, error) {
	var version int
	err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), sourceType).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		// Not configured is a normal state, and saying so keeps an unused source
		// out of the connector health screen and out of the circuit breaker.
		return nil, fmt.Errorf("%w: %s", source.ErrNotConfigured, sourceType)
	}
	if err != nil {
		return nil, fmt.Errorf("%s setting is unavailable: %w", sourceType, err)
	}
	a.adapterMu.RLock()
	cached, ok := a.adapters[sourceType]
	a.adapterMu.RUnlock()
	if ok && cached.version == version {
		return cached.adapter, nil
	}
	settings, err := a.loadSettingMap(ctx, sourceType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", source.ErrNotConfigured, sourceType)
	}
	if err != nil {
		return nil, fmt.Errorf("%s setting is unavailable: %w", sourceType, err)
	}
	adapter, err := sourceAdapterFromMap(sourceType, settings)
	if err != nil {
		return nil, err
	}
	a.adapterMu.Lock()
	if a.adapters == nil {
		a.adapters = map[string]cachedAdapter{}
	}
	previous := a.adapters[sourceType]
	a.adapters[sourceType] = cachedAdapter{adapter: adapter, version: version}
	a.adapterMu.Unlock()
	// The replaced adapter keeps a connection pool to the old endpoint. Releasing
	// it here keeps a series of setting changes from leaking sockets.
	closeIdle(previous.adapter)
	return adapter, nil
}

// cachedAdapter keeps the adapter with the setting version it was built from,
// which is what makes an administrator's save take effect on the next call
// without any explicit invalidation.
type cachedAdapter struct {
	adapter source.RepositorySource
	version int
}

// closeIdle releases the connection pool of an adapter that is being replaced.
// Adapters are not required to implement it; the ones that hold an HTTP client
// do.
func closeIdle(adapter source.RepositorySource) {
	if closer, ok := adapter.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func sourceAdapterFromMap(sourceType string, settings map[string]any) (source.RepositorySource, error) {
	baseURL, _ := settings["baseUrl"].(string)
	token, _ := settings["token"].(string)
	if token == "" {
		token, _ = settings["pat"].(string)
	}
	timeout := 30 * time.Second
	if seconds, ok := settings["timeoutSeconds"].(float64); ok && seconds > 0 {
		timeout = time.Duration(seconds * float64(time.Second))
	}
	var tlsVerify *bool
	if value, ok := settings["tlsVerify"].(bool); ok {
		tlsVerify = &value
	}
	caCertificate, _ := settings["caCertificate"].(string)
	proxyURL, _ := settings["proxyUrl"].(string)
	switch sourceType {
	case "bitbucket":
		apiPrefix, _ := settings["apiPrefix"].(string)
		searchAPIPath, _ := settings["searchApiPath"].(string)
		username, _ := settings["username"].(string)
		password, _ := settings["password"].(string)
		return bitbucketv6.New(bitbucketv6.Config{BaseURL: baseURL, APIPrefix: apiPrefix, SearchAPIPath: searchAPIPath, Token: token, Username: username, Password: password, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: caCertificate, ProxyURL: proxyURL})
	case "gitlab":
		return gitlabsource.New(gitlabsource.Config{BaseURL: baseURL, Token: token, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: caCertificate, ProxyURL: proxyURL})
	case "confluence":
		authType, _ := settings["authType"].(string)
		username, _ := settings["username"].(string)
		password, _ := settings["password"].(string)
		return confluencesource.New(confluencesource.Config{BaseURL: baseURL, AuthType: authType, Token: token, Username: username, Password: password, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: caCertificate, ProxyURL: proxyURL, AllowedPrincipals: stringArrayValue(settings["allowedPrincipals"])})
	case "jira":
		authType, _ := settings["authType"].(string)
		username, _ := settings["username"].(string)
		password, _ := settings["password"].(string)
		return jirasource.New(jirasource.Config{BaseURL: baseURL, AuthType: authType, Token: token, Username: username, Password: password, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: caCertificate, ProxyURL: proxyURL, AllowedPrincipals: stringArrayValue(settings["allowedPrincipals"])})
	default:
		return nil, errors.New("unsupported source type")
	}
}
func (a *App) discoverSource(w http.ResponseWriter, r *http.Request) {
	sourceType := r.PathValue("source")
	adapter, err := a.sourceAdapter(r.Context(), sourceType)
	if err != nil {
		problem(w, 400, "source_connection_failed", err.Error())
		return
	}
	var in struct {
		ProjectKey string `json:"projectKey"`
	}
	if r.ContentLength > 0 && decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "Invalid discovery request")
		return
	}
	if in.ProjectKey == "" {
		projects, err := adapter.ListProjects(r.Context())
		if err != nil {
			problem(w, 502, "source_discovery_failed", err.Error())
			return
		}
		jsonOut(w, 200, map[string]any{"sourceType": sourceType, "projects": projects})
		return
	}
	repositories, err := adapter.ListRepositories(r.Context(), in.ProjectKey)
	if err != nil {
		problem(w, 502, "source_discovery_failed", err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"sourceType": sourceType, "projectKey": in.ProjectKey, "repositories": repositories})
}
func (a *App) adminRepositories(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT id,source_type,source_external_id,project_key,slug,name,description,library_id,default_branch,reputation,enabled,indexed_at FROM repositories ORDER BY source_type,project_key,slug`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, sourceType, externalID, projectKey, slug, name, description, libraryID, branch, reputation string
		var enabled int
		var indexed sql.NullTime
		if err := rows.Scan(&id, &sourceType, &externalID, &projectKey, &slug, &name, &description, &libraryID, &branch, &reputation, &enabled, &indexed); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		item := map[string]any{"id": id, "sourceType": sourceType, "externalId": externalID, "projectKey": projectKey, "slug": slug, "name": name, "description": description, "libraryId": libraryID, "defaultBranch": branch, "reputation": reputation, "enabled": enabled == 1}
		if indexed.Valid {
			item["indexedAt"] = indexed.Time
		}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func (a *App) registerRepository(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SourceType string            `json:"sourceType"`
		Repository source.Repository `json:"repository"`
		RefName    string            `json:"refName"`
	}
	if decode(r, &in) != nil || !supportedSourceType(in.SourceType) || in.Repository.ID == 0 || in.Repository.ProjectKey == "" || in.Repository.Slug == "" {
		problem(w, 400, "invalid_request", "sourceType and a discovered repository are required")
		return
	}
	id := in.SourceType + ":" + fmt.Sprint(in.Repository.ID)
	libraryID := indexer.LibraryIDForSource(in.SourceType, in.Repository.ProjectKey, in.Repository.Slug)
	settings, err := a.loadSettingMap(r.Context(), in.SourceType)
	if err != nil {
		problem(w, 400, "source_not_configured", "Source setting is required before repository registration")
		return
	}
	autoWebhook := in.SourceType == "bitbucket" || in.SourceType == "gitlab"
	if configured, ok := settings["autoRegisterWebhook"].(bool); ok {
		autoWebhook = configured
	}
	var alreadyRegistered int
	_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM repositories WHERE id=?`), id).Scan(&alreadyRegistered)
	if autoWebhook && alreadyRegistered == 0 {
		secret, _ := settings["webhookSecret"].(string)
		if secret == "" {
			problem(w, http.StatusBadRequest, "webhook_secret_required", fmt.Sprintf(
				"Registering a %s repository automatically installs a push webhook, which needs a shared secret. Set 'Webhook Secret' in the %s setting, or turn off 'Webhook 자동 등록' to register without one.",
				in.SourceType, in.SourceType))
			return
		}
		adapter, adapterErr := sourceAdapterFromMap(in.SourceType, settings)
		if adapterErr != nil {
			problem(w, 400, "source_connection_failed", adapterErr.Error())
			return
		}
		target := strings.TrimSuffix(a.publicURL(r.Context()), "/") + "/webhooks/" + in.SourceType
		if adapterErr = adapter.RegisterWebhook(r.Context(), source.RepositoryRef{ProjectKey: in.Repository.ProjectKey, Slug: in.Repository.Slug}, target, secret); adapterErr != nil {
			problem(w, 502, "webhook_registration_failed", adapterErr.Error())
			return
		}
	}
	_, err = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES(?,?,?,?,?,?,?,?,?,1) ON CONFLICT(id) DO UPDATE SET project_key=excluded.project_key,slug=excluded.slug,name=excluded.name,description=excluded.description,library_id=excluded.library_id,default_branch=excluded.default_branch,enabled=1`), id, in.Repository.ProjectKey, in.Repository.Slug, in.Repository.Name, in.Repository.Description, in.SourceType, fmt.Sprint(in.Repository.ID), libraryID, in.Repository.DefaultBranch)
	if err != nil {
		problem(w, 409, "repository_registration_failed", err.Error())
		return
	}
	refName := in.RefName
	if refName == "" {
		refName = in.Repository.DefaultBranch
	}
	jobID, _ := randomToken(18)
	_, err = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES(?,?,?,'initial','pending')`), jobID, id, refName)
	if err != nil {
		problem(w, 500, "job_enqueue_failed", err.Error())
		return
	}
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "repository.register", "repository", id, "success", map[string]any{"jobId": jobID})
	jsonOut(w, 201, map[string]any{"id": id, "libraryId": libraryID, "jobId": jobID})
}
func (a *App) enqueueIndex(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var defaultRef string
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT default_branch FROM repositories WHERE id=? AND enabled=1`), id).Scan(&defaultRef); err != nil {
		problem(w, 404, "not_found", "Repository not found")
		return
	}
	var in struct {
		RefName string `json:"refName"`
	}
	if r.ContentLength > 0 && decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "Invalid index request")
		return
	}
	if in.RefName == "" {
		in.RefName = defaultRef
	}
	jobID, _ := randomToken(18)
	if _, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES(?,?,?,'manual','pending')`), jobID, id, in.RefName); err != nil {
		problem(w, 500, "job_enqueue_failed", err.Error())
		return
	}
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "index.enqueue", "repository", id, "success", map[string]any{"jobId": jobID, "ref": in.RefName})
	jsonOut(w, 202, map[string]any{"jobId": jobID, "status": "pending"})
}
func (a *App) repositoryRefs(w http.ResponseWriter, r *http.Request) {
	adapter, repo, err := a.registeredSource(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "not_found", err.Error())
		return
	}
	ref := source.RepositoryRef{ProjectKey: repo.ProjectKey, Slug: repo.Slug}
	branches, err := adapter.ListBranches(r.Context(), ref)
	if err != nil {
		problem(w, 502, "source_error", err.Error())
		return
	}
	tags, err := adapter.ListTags(r.Context(), ref)
	if err != nil {
		problem(w, 502, "source_error", err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"branches": branches, "tags": tags})
}
func (a *App) registeredSource(ctx context.Context, id string) (source.RepositorySource, source.Repository, error) {
	var repo source.Repository
	var sourceType, external string
	err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT source_type,source_external_id,project_key,slug,name,description,default_branch FROM repositories WHERE id=? AND enabled=1`), id).Scan(&sourceType, &external, &repo.ProjectKey, &repo.Slug, &repo.Name, &repo.Description, &repo.DefaultBranch)
	if err != nil {
		return nil, repo, errors.New("registered repository not found")
	}
	repo.ID, _ = strconv.ParseInt(external, 10, 64)
	adapter, err := a.sourceAdapter(ctx, sourceType)
	return adapter, repo, err
}
func (a *App) getRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	policy := indexer.DefaultPolicy()
	var extensions, excludes string
	err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT include_extensions,exclude_prefixes,max_file_bytes FROM repository_index_policies WHERE repository_id=?`), r.PathValue("id")).Scan(&extensions, &excludes, &policy.MaxFileBytes)
	if err == nil {
		policy.IncludeExtensions = splitCSV(extensions)
		policy.ExcludePrefixes = splitCSV(excludes)
	} else if !errors.Is(err, sql.ErrNoRows) {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	jsonOut(w, 200, policy)
}
func (a *App) putRepositoryPolicy(w http.ResponseWriter, r *http.Request) {
	var policy indexer.Policy
	if decode(r, &policy) != nil || len(policy.IncludeExtensions) == 0 || policy.MaxFileBytes < 1024 || policy.MaxFileBytes > 10<<20 {
		problem(w, 400, "invalid_policy", "Extensions and maxFileBytes between 1024 and 10485760 are required")
		return
	}
	for _, extension := range policy.IncludeExtensions {
		if !strings.HasPrefix(extension, ".") || strings.ContainsAny(extension, "/\\") {
			problem(w, 400, "invalid_policy", "Extensions must start with a dot and contain no path separator")
			return
		}
	}
	p, _ := auth.FromContext(r.Context())
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO repository_index_policies(repository_id,include_extensions,exclude_prefixes,max_file_bytes,updated_by,updated_at) SELECT id,?,?,?,?,? FROM repositories WHERE id=? ON CONFLICT(repository_id) DO UPDATE SET include_extensions=excluded.include_extensions,exclude_prefixes=excluded.exclude_prefixes,max_file_bytes=excluded.max_file_bytes,updated_by=excluded.updated_by,updated_at=excluded.updated_at`), strings.Join(policy.IncludeExtensions, ","), strings.Join(policy.ExcludePrefixes, ","), policy.MaxFileBytes, p.UserID, time.Now().UTC(), r.PathValue("id"))
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		problem(w, 404, "not_found", "Repository not found")
		return
	}
	a.audit(r, p, "repository.policy_update", "repository", r.PathValue("id"), "success", policy)
	jsonOut(w, 200, policy)
}
func (a *App) indexJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT id,repository_id,ref_name,kind,status,attempts,error_message,files_processed,created_at,started_at,completed_at FROM index_jobs ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, repositoryID, refName, kind, status, message string
		var attempts, files int
		var created time.Time
		var started, completed sql.NullTime
		if err := rows.Scan(&id, &repositoryID, &refName, &kind, &status, &attempts, &message, &files, &created, &started, &completed); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		item := map[string]any{"id": id, "repositoryId": repositoryID, "refName": refName, "kind": kind, "status": status, "attempts": attempts, "error": message, "filesProcessed": files, "createdAt": created}
		if started.Valid {
			item["startedAt"] = started.Time
		}
		if completed.Valid {
			item["completedAt"] = completed.Time
		}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func (a *App) retryIndexJob(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE index_jobs SET status='pending',attempts=0,error_message='',next_run_at=?,completed_at=NULL WHERE id=? AND status='failed'`), time.Now().UTC(), r.PathValue("id"))
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		problem(w, 409, "job_not_retryable", "Only failed jobs can be retried")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
func supportedSourceType(value string) bool {
	switch value {
	case "bitbucket", "gitlab", "confluence", "jira":
		return true
	default:
		return false
	}
}

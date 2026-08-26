package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"strconv"

	"git-ctx/internal/auth"
	"git-ctx/internal/backup"
	"git-ctx/internal/embedding"
	"git-ctx/internal/observability"
	"git-ctx/internal/search"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/vectorstore"
	"git-ctx/internal/version"
)

// Readiness, diagnostics and the health views the console renders.

func observabilityConfigFromMap(settings map[string]any) observability.Config {
	cfg := observability.Config{ServiceName: "git-ctx", SampleRatio: 1, Timeout: 10 * time.Second, Headers: map[string]string{}}
	cfg.Enabled, _ = settings["enabled"].(bool)
	cfg.Endpoint, _ = settings["otlpEndpoint"].(string)
	if value, ok := settings["serviceName"].(string); ok && value != "" {
		cfg.ServiceName = value
	}
	if value, ok := settings["sampleRatio"].(float64); ok {
		cfg.SampleRatio = value
	}
	if value, ok := settings["timeoutSeconds"].(float64); ok && value > 0 {
		cfg.Timeout = time.Duration(value * float64(time.Second))
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	cfg.CACertificate, _ = settings["caCertificate"].(string)
	cfg.ProxyURL, _ = settings["proxyUrl"].(string)
	cfg.AllowInsecureLocalhost, _ = settings["allowInsecureLocalhost"].(bool)
	if headers, ok := settings["headers"].(map[string]any); ok {
		for key, value := range headers {
			if text, ok := value.(string); ok {
				cfg.Headers[key] = text
			}
		}
	}
	return cfg
}

// sourceHealth reports connector health for the operations screen: whether the
// platform is currently calling each source, why it stopped, and when it will
// try again.
func (a *App) sourceHealth(w http.ResponseWriter, r *http.Request) {
	states := a.breakers.States()
	known := map[string]bool{}
	for _, state := range states {
		known[state.Source] = true
	}
	// A configured source with no recorded calls is healthy but unproven; saying
	// so is more useful than omitting it.
	out := make([]map[string]any, 0, len(states)+2)
	for _, state := range states {
		payload := breakerPayload(state)
		// A source with no saved configuration is reported as such rather than as
		// a healthy connector that simply has not been called.
		var configured int
		_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM system_settings WHERE category=?`), state.Source).Scan(&configured)
		if configured == 0 {
			payload["state"], payload["configured"] = "not-configured", false
			payload["detail"] = "연동이 설정되어 있지 않습니다."
		} else {
			payload["configured"] = true
		}
		out = append(out, payload)
	}
	for _, sourceType := range []string{"bitbucket", "gitlab", "confluence", "jira"} {
		if known[sourceType] {
			continue
		}
		var configured int
		_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM system_settings WHERE category=?`), sourceType).Scan(&configured)
		if configured == 0 {
			continue
		}
		out = append(out, map[string]any{"source": sourceType, "state": "closed", "healthy": true, "configured": true,
			"detail": "설정되어 있으나 아직 호출 기록이 없습니다."})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["source"].(string) < out[j]["source"].(string) })
	jsonOut(w, http.StatusOK, map[string]any{"sources": out})
}

func breakerPayload(state source.BreakerState) map[string]any {
	detail := "정상적으로 호출하고 있습니다."
	switch state.State {
	case "degraded":
		detail = fmt.Sprintf("최근 %d회 연속 실패: %s", state.Failures, state.LastError)
	case "open":
		detail = fmt.Sprintf("연속 실패 %d회로 호출을 멈췄습니다(%s). %s 이후 자동 재시도합니다.",
			state.Failures, state.LastError, state.RetryAt.UTC().Format(time.RFC3339))
	case "half-open":
		detail = fmt.Sprintf("복구를 확인할 차례입니다. 마지막 오류: %s", state.LastError)
	}
	return map[string]any{
		"source": state.Source, "state": state.State, "healthy": state.State == "closed",
		"failures": state.Failures, "lastError": state.LastError, "openedAt": state.OpenedAt,
		"retryAt": state.RetryAt, "detail": detail,
	}
}

// resetSourceHealth is the administrator's "try again now": after fixing a
// token or restarting a server, waiting out the back-off is pointless.
func (a *App) resetSourceHealth(w http.ResponseWriter, r *http.Request) {
	sourceType := r.PathValue("source")
	if !settingCategories()[sourceType] {
		problem(w, http.StatusNotFound, "not_found", "Unknown source")
		return
	}
	a.breakers.Get(sourceType).Reset()
	// The adapter is dropped too, so a corrected credential is picked up even if
	// the setting version did not change.
	a.adapterMu.Lock()
	evicted := a.adapters[sourceType]
	delete(a.adapters, sourceType)
	a.adapterMu.Unlock()
	closeIdle(evicted.adapter)
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "source.health.reset", "source", sourceType, "success", nil)
	jsonOut(w, http.StatusOK, breakerPayload(a.breakers.Get(sourceType).State(sourceType)))
}

func (a *App) readiness(w http.ResponseWriter, r *http.Request) {
	if a.databaseRestart.Load() {
		jsonOut(w, http.StatusServiceUnavailable, map[string]any{"status": "restart_required", "database": "migration_completed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.DB.PingContext(ctx); err != nil {
		jsonOut(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "database": "unavailable"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"status": "ready", "database": "ok"})
}
func (a *App) publicDatabaseStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	status, ok := a.databaseStatus(ctx, false)
	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}
	jsonOut(w, code, status)
}
func (a *App) adminDatabaseStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, ok := a.databaseStatus(ctx, true)
	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}
	jsonOut(w, code, status)
}
func (a *App) databaseStatus(ctx context.Context, detailed bool) (map[string]any, bool) {
	started := time.Now()
	err := a.store.DB.PingContext(ctx)
	result := map[string]any{
		"status": "connected", "driver": a.store.Driver(),
		"latencyMs": float64(time.Since(started).Microseconds()) / 1000, "checkedAt": time.Now().UTC(),
		"recoveryMode":    a.recoveryMode,
		"restartRequired": a.databaseRestart.Load(),
	}
	if err != nil {
		result["status"] = "unavailable"
		if detailed {
			result["error"] = truncateText(err.Error(), 500)
		}
		return result, false
	}
	if !detailed {
		return result, true
	}
	if a.recoveryMode {
		result["startupError"] = a.databaseStartupErr
		result["recoveryDatabase"] = a.recoveryDatabase
		result["warning"] = "SQLite recovery mode is active; configure and migrate PostgreSQL before production use"
	}
	stats := a.store.DB.Stats()
	result["pool"] = map[string]any{"open": stats.OpenConnections, "inUse": stats.InUse, "idle": stats.Idle, "waitCount": stats.WaitCount, "waitDurationMs": float64(stats.WaitDuration.Microseconds()) / 1000, "maxOpen": stats.MaxOpenConnections}
	var migrationCount int
	var latestMigration string
	_ = a.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount)
	_ = a.store.DB.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&latestMigration)
	result["migrations"] = map[string]any{"count": migrationCount, "latest": latestMigration}
	if a.store.Driver() == "postgres" {
		var database, user, serverVersion string
		var databaseTime time.Time
		if queryErr := a.store.DB.QueryRowContext(ctx, `SELECT current_database(),current_user,current_setting('server_version'),CURRENT_TIMESTAMP`).Scan(&database, &user, &serverVersion, &databaseTime); queryErr == nil {
			result["database"] = database
			result["user"] = user
			result["serverVersion"] = serverVersion
			result["databaseTime"] = databaseTime
		}
	} else {
		var sqliteVersion string
		if queryErr := a.store.DB.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&sqliteVersion); queryErr == nil {
			result["serverVersion"] = sqliteVersion
		}
	}
	return result, true
}

func (a *App) testDatabaseTarget(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		DSN string `json:"dsn"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.DSN) == "" {
		problem(w, 400, "database_dsn_required", "PostgreSQL DSN is required")
		return
	}
	if store.DriverForDSN(in.DSN) != "postgres" {
		problem(w, 400, "postgres_required", "Migration target must be PostgreSQL")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	started := time.Now()
	info, err := store.TestConnection(ctx, in.DSN)
	if err != nil {
		safeErr := safeDatabaseError(err, in.DSN)
		a.audit(r, p, "database.test", "database", "postgres", "failure", map[string]any{"error": safeErr})
		problem(w, 400, "database_connection_failed", safeErr)
		return
	}
	a.audit(r, p, "database.test", "database", "postgres", "success", nil)
	jsonOut(w, 200, map[string]any{"status": "verified", "driver": info["driver"], "database": info["database"], "user": info["user"], "serverVersion": info["serverVersion"], "latencyMs": float64(time.Since(started).Microseconds()) / 1000})
}

func (a *App) migrateDatabaseTarget(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		DSN     string `json:"dsn"`
		Confirm string `json:"confirm"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.DSN) == "" || in.Confirm != "MIGRATE TO POSTGRES" {
		problem(w, 400, "migration_confirmation_required", "DSN and exact confirmation MIGRATE TO POSTGRES are required")
		return
	}
	if store.DriverForDSN(in.DSN) != "postgres" {
		problem(w, 400, "postgres_required", "Migration target must be PostgreSQL")
		return
	}
	a.requestGate.Lock()
	a.stopBackground()
	succeeded := false
	defer func() {
		if !succeeded {
			a.startBackground()
		}
		a.requestGate.Unlock()
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	target, err := store.Open(ctx, "postgres", in.DSN)
	if err != nil {
		problem(w, 400, "database_migration_failed", safeDatabaseError(err, in.DSN))
		return
	}
	defer target.DB.Close()
	if err = backup.MigrateLogical(ctx, a.store, target); err != nil {
		a.audit(r, p, "database.migrate", "database", "postgres", "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 400, "database_migration_failed", err.Error())
		return
	}
	if err = a.saveDatabaseTarget(ctx, target, in.DSN, p.UserID, "database migration"); err != nil {
		problem(w, 500, "database_target_save_failed", err.Error())
		return
	}
	a.audit(r, p, "database.migrate", "database", "postgres", "success", nil)
	succeeded = true
	a.databaseRestart.Store(true)
	jsonOut(w, 200, map[string]any{"status": "migrated", "restartRequired": true, "message": "Data migration completed. Restart the service to activate PostgreSQL."})
}

func safeDatabaseError(err error, dsn string) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), dsn, "[redacted DSN]")
	return truncateText(message, 500)
}

type embeddingHealthView struct {
	RequestedMode    string                    `json:"requestedMode"`
	EffectiveMode    string                    `json:"effectiveMode"`
	OperationalMode  string                    `json:"operationalMode"`
	DegradedReason   string                    `json:"degradedReason,omitempty"`
	ModelConfigured  bool                      `json:"modelConfigured"`
	TotalChunks      int64                     `json:"totalChunks"`
	StoredEmbeddings int64                     `json:"storedEmbeddings"`
	EmbeddedChunks   int64                     `json:"embeddedChunks"`
	CoveragePercent  float64                   `json:"coveragePercent"`
	MinimumCoverage  float64                   `json:"minimumCoveragePercent"`
	IncompatibleRefs int64                     `json:"incompatibleRefs"`
	ReadyRefs        int64                     `json:"readyRefs"`
	PartialRefs      int64                     `json:"partialRefs"`
	DegradedRefs     int64                     `json:"degradedRefs"`
	UnavailableRefs  int64                     `json:"unavailableRefs"`
	Circuit          embedding.RuntimeSnapshot `json:"circuit"`
}

func (a *App) embeddingHealth(ctx context.Context) embeddingHealthView {
	cfg := a.searchConfig(ctx)
	view := embeddingHealthView{
		RequestedMode: a.requestedRetrievalMode(ctx), EffectiveMode: cfg.RetrievalMode,
		OperationalMode: cfg.RetrievalMode, MinimumCoverage: cfg.MinimumEmbeddingCoverage,
	}
	modelSettings, modelErr := a.loadSettingMap(ctx, "model")
	provider, _ := modelSettings["provider"].(string)
	view.ModelConfigured = modelErr == nil && provider == "openai-compatible"
	identity := embeddingRuntimeIdentity(modelSettings)
	if a.embeddingRuntime != nil {
		view.Circuit = a.embeddingRuntime.Snapshot(identity)
	} else {
		view.Circuit = embedding.RuntimeSnapshot{Identity: identity, State: "idle"}
	}
	var trackedRefs int64
	_ = a.store.DB.QueryRowContext(ctx, `SELECT
COUNT(*),
COALESCE(SUM(total_chunks),0),
COALESCE(SUM(embedded_chunks),0),
COALESCE(SUM(CASE WHEN embedding_status='ready' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN embedding_status='partial' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN embedding_status='degraded' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN embedding_status='unavailable' THEN 1 ELSE 0 END),0)
FROM repository_ref_states`).Scan(
		&trackedRefs, &view.TotalChunks, &view.StoredEmbeddings,
		&view.ReadyRefs, &view.PartialRefs, &view.DegradedRefs, &view.UnavailableRefs,
	)
	view.EmbeddedChunks = view.StoredEmbeddings
	if cfg.EmbeddingRevision != "" {
		_ = a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT
COALESCE(SUM(CASE WHEN embedding_revision=? THEN embedded_chunks ELSE 0 END),0),
COALESCE(SUM(CASE WHEN embedded_chunks>0 AND embedding_revision<>? THEN 1 ELSE 0 END),0)
FROM repository_ref_states`), cfg.EmbeddingRevision, cfg.EmbeddingRevision).Scan(&view.EmbeddedChunks, &view.IncompatibleRefs)
	}
	// Installations upgraded from a version before ref coverage tracking can
	// temporarily have chunks but no ref state. Keep diagnostics accurate until
	// the next index run backfills the aggregate.
	if trackedRefs == 0 {
		_ = a.store.DB.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(embedding) FROM document_chunks`).Scan(&view.TotalChunks, &view.StoredEmbeddings)
		view.EmbeddedChunks = view.StoredEmbeddings
		if cfg.EmbeddingRevision != "" {
			_ = a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT COALESCE(SUM(CASE WHEN embedding IS NOT NULL AND embedding_revision=? THEN 1 ELSE 0 END),0) FROM document_chunks`), cfg.EmbeddingRevision).Scan(&view.EmbeddedChunks)
		}
	}
	if view.TotalChunks > 0 {
		view.CoveragePercent = float64(view.EmbeddedChunks) * 100 / float64(view.TotalChunks)
	}
	switch {
	case view.RequestedMode == search.RetrievalKeywordOnly:
		view.OperationalMode = search.RetrievalKeywordOnly
	case !view.ModelConfigured && view.RequestedMode == search.RetrievalHybridFallback:
		view.OperationalMode = search.RetrievalKeywordOnly
		view.DegradedReason = "embedding model is not configured"
	case view.Circuit.State == "open" && view.RequestedMode == search.RetrievalHybridFallback:
		view.OperationalMode = search.RetrievalKeywordOnly
		view.DegradedReason = "embedding circuit is open"
	case view.TotalChunks > 0 && view.CoveragePercent < view.MinimumCoverage && view.RequestedMode == search.RetrievalHybridFallback:
		view.OperationalMode = search.RetrievalKeywordOnly
		if view.IncompatibleRefs > 0 {
			view.DegradedReason = fmt.Sprintf("%d refs use a different embedding model revision; compatible coverage is %.1f%%", view.IncompatibleRefs, view.CoveragePercent)
		} else {
			view.DegradedReason = fmt.Sprintf("embedding coverage %.1f%% is below %.1f%%", view.CoveragePercent, view.MinimumCoverage)
		}
	}
	return view
}

func (a *App) embeddingHealthMarkdown(ctx context.Context) string {
	view := a.embeddingHealth(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "- Requested Mode: %s\n- Effective Configuration: %s\n- Operational Mode: %s\n", view.RequestedMode, view.EffectiveMode, view.OperationalMode)
	fmt.Fprintf(&b, "- Compatible Coverage: %.1f%% (%d/%d chunks, %d stored embeddings, minimum %.1f%%)\n", view.CoveragePercent, view.EmbeddedChunks, view.TotalChunks, view.StoredEmbeddings, view.MinimumCoverage)
	fmt.Fprintf(&b, "- Incompatible Refs: %d\n", view.IncompatibleRefs)
	fmt.Fprintf(&b, "- Model Circuit: %s · requests %d · failures %d · cache hits %d · coalesced %d\n", view.Circuit.State, view.Circuit.Requests, view.Circuit.Failures, view.Circuit.CacheHits, view.Circuit.Coalesced)
	if view.DegradedReason != "" {
		fmt.Fprintf(&b, "- Degraded Reason: %s\n", view.DegradedReason)
	}
	if view.Circuit.LastError != "" {
		fmt.Fprintf(&b, "- Last Model Error: %s\n", view.Circuit.LastError)
	}
	if !view.Circuit.RetryAt.IsZero() && view.Circuit.RetryAt.After(time.Now()) {
		fmt.Fprintf(&b, "- Next Model Probe: %s\n", view.Circuit.RetryAt.UTC().Format(time.RFC3339))
	}
	// Whether results are reranked is an operational fact of the same kind: a
	// reranker that is configured but failing changes the order of every answer
	// and reports itself nowhere else.
	settings, err := a.loadSettingMap(ctx, "model")
	if err != nil {
		fmt.Fprintf(&b, "- Reranker: unknown (model settings unreadable)\n")
		return b.String()
	}
	if enabled, _ := settings["rerankerEnabled"].(bool); !enabled {
		fmt.Fprintf(&b, "- Reranker: disabled\n")
		return b.String()
	}
	model, _ := settings["rerankerModel"].(string)
	if _, rerankErr := rerankerProviderFromMap(settings); rerankErr != nil {
		fmt.Fprintf(&b, "- Reranker: enabled but unusable (%s): %s\n", model, rerankErr.Error())
		return b.String()
	}
	fmt.Fprintf(&b, "- Reranker: enabled (%s) · a call that fails is reported in the answer itself\n", model)
	a.appendVectorStoreStatus(ctx, &b)
	return b.String()
}

// appendVectorStoreStatus reports the external vector database. It is the third
// component whose failure an answer would otherwise absorb: the candidates it
// contributes are the ones no keyword would have found, so losing it narrows
// answers without changing how they look.
func (a *App) appendVectorStoreStatus(ctx context.Context, b *strings.Builder) {
	settings, err := a.loadSettingMap(ctx, "vector")
	if err != nil {
		fmt.Fprintf(b, "- Vector Database: unknown (settings unreadable)\n")
		return
	}
	cfg := vectorstore.FromMap(settings)
	if !cfg.Enabled() {
		fmt.Fprintf(b, "- Vector Database: not configured (embeddings are scored in this database)\n")
		return
	}
	status, statusErr := vectorstore.TestConnection(ctx, cfg, a.postgresDSN(ctx))
	if statusErr != nil {
		fmt.Fprintf(b, "- Vector Database: %s configured but unreachable: %s\n", cfg.Provider, statusErr.Error())
		return
	}
	fmt.Fprintf(b, "- Vector Database: %s · collection %s · %d vectors · %d dimensions\n",
		cfg.Provider, status.Collection, status.Vectors, status.Dimensions)
}

// searchBackendHealth describes the paths a search can take on this instance.
func (a *App) searchBackendHealth(ctx context.Context) map[string]any {
	health := map[string]any{"fullTextIndex": a.store.FullTextAvailable(), "reranker": "disabled",
		"vectorDatabase": "not configured"}
	if settings, err := a.loadSettingMap(ctx, "model"); err == nil {
		if enabled, _ := settings["rerankerEnabled"].(bool); enabled {
			model, _ := settings["rerankerModel"].(string)
			if _, rerankErr := rerankerProviderFromMap(settings); rerankErr != nil {
				health["reranker"] = "unusable: " + rerankErr.Error()
			} else {
				health["reranker"] = "enabled: " + model
			}
		}
	}
	if settings, err := a.loadSettingMap(ctx, "vector"); err == nil {
		cfg := vectorstore.FromMap(settings)
		if cfg.Enabled() {
			status, statusErr := vectorstore.TestConnection(ctx, cfg, a.postgresDSN(ctx))
			if statusErr != nil {
				health["vectorDatabase"] = cfg.Provider + " unreachable: " + statusErr.Error()
			} else {
				health["vectorDatabase"] = fmt.Sprintf("%s · %s · %d vectors", cfg.Provider, status.Collection, status.Vectors)
			}
		}
	}
	return health
}

func (a *App) adminHealth(w http.ResponseWriter, r *http.Request) {
	var repositories, chunks, pending, failed, activeKeys, activeSecrets, notificationPending, notificationFailed, notificationDead int64
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM repositories WHERE enabled=1`).Scan(&repositories)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM document_chunks`).Scan(&chunks)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM index_jobs WHERE status='pending'`).Scan(&pending)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM index_jobs WHERE status='failed'`).Scan(&failed)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND disabled_at IS NULL AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP)`).Scan(&activeKeys)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM managed_secrets WHERE status='active'`).Scan(&activeSecrets)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM notification_deliveries WHERE status='pending'`).Scan(&notificationPending)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM notification_deliveries WHERE status='failed'`).Scan(&notificationFailed)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM notification_deliveries WHERE status='dead'`).Scan(&notificationDead)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	// The retrieval path and the optional backends are reported here as well as
	// through the MCP status tool. An operator lives in the console, and until
	// now the console could not tell whether searches used the full-text index
	// or scanned, nor whether the reranker and vector database were reachable —
	// the agent asking for platform status knew more than the person running it.
	jsonOut(w, 200, map[string]any{"status": "ok", "version": version.Version, "build": version.Full(), "database": "ok", "repositories": repositories, "chunks": chunks, "search": a.searchBackendHealth(r.Context()), "indexJobs": map[string]int64{"pending": pending, "failed": failed}, "notificationDeliveries": map[string]int64{"pending": notificationPending, "failed": notificationFailed, "dead": notificationDead}, "activeApiKeys": activeKeys, "activeManagedSecrets": activeSecrets, "embedding": a.embeddingHealth(r.Context()), "observability": map[string]bool{"tracingEnabled": a.traces.Enabled()}, "go": map[string]any{"goroutines": runtime.NumGoroutine(), "allocatedBytes": memory.Alloc}})
}

// setupStatus reports how far the initial configuration has progressed. A fresh
// on-prem install needs Keycloak, an identity claim mapping, a source connector,
// registered repositories and an index before any search returns a result, and
// every one of those failures otherwise looks like "search is broken".
func (a *App) setupStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	configured := map[string]bool{}
	rows, err := a.store.DB.QueryContext(ctx, `SELECT category FROM system_settings`)
	if err == nil {
		for rows.Next() {
			var category string
			if rows.Scan(&category) == nil {
				configured[category] = true
			}
		}
		rows.Close()
	}
	count := func(query string) int64 {
		var value int64
		_ = a.store.DB.QueryRowContext(ctx, query).Scan(&value)
		return value
	}
	repositories := count(`SELECT COUNT(*) FROM repositories WHERE enabled=1`)
	chunks := count(`SELECT COUNT(*) FROM document_chunks`)
	failedJobs := count(`SELECT COUNT(*) FROM index_jobs WHERE status='failed'`)
	runningJobs := count(`SELECT COUNT(*) FROM index_jobs WHERE status='running'`)
	pendingJobs := count(`SELECT COUNT(*) FROM index_jobs WHERE status='pending'`)
	mappedIdentities := count(`SELECT COUNT(*) FROM user_identities WHERE bitbucket_user_slug<>'' OR gitlab_user_id<>''`)
	users := count(`SELECT COUNT(*) FROM users WHERE status='active' AND id NOT IN ('bootstrap-admin','break-glass-admin')`)
	sourceConfigured := configured["bitbucket"] || configured["gitlab"] || configured["confluence"] || configured["jira"]

	keycloakDetail := "Keycloak 설정이 없어 SSO 로그인을 사용할 수 없습니다."
	keycloakStatus := "todo"
	if configured["keycloak"] {
		keycloakStatus, keycloakDetail = "done", "SSO 설정이 저장되었습니다."
		if _, cfgErr := a.loadOIDCConfig(ctx); cfgErr != nil {
			keycloakStatus, keycloakDetail = "warn", "저장된 OIDC 설정을 적용할 수 없습니다: "+truncateText(cfgErr.Error(), 200)
		}
	}
	step := func(key, title, detail, status, target, category string) map[string]any {
		return map[string]any{"key": key, "title": title, "detail": detail, "status": status, "target": target, "category": category}
	}
	steps := []map[string]any{
		step("keycloak", "Keycloak SSO 연결", keycloakDetail, keycloakStatus, "settings-admin", "keycloak"),
		step("identity", "소스 ACL Claim 매핑",
			fmt.Sprintf("활성 사용자 %d명 중 %d명에게 Bitbucket·GitLab 신원이 매핑되었습니다.", users, mappedIdentities),
			map[bool]string{true: "done", false: "todo"}[mappedIdentities > 0 || users == 0], "settings-admin", "keycloak"),
		step("source", "소스 시스템 연결",
			map[bool]string{true: "Bitbucket 또는 GitLab 연결이 저장되었습니다.", false: "연결된 소스가 없어 검색할 대상이 없습니다."}[sourceConfigured],
			map[bool]string{true: "done", false: "todo"}[sourceConfigured], "settings-admin", "gitlab"),
		step("repositories", "저장소 등록",
			fmt.Sprintf("등록된 저장소 %d개", repositories),
			map[bool]string{true: "done", false: "todo"}[repositories > 0], "source-admin-section", ""),
		step("index", "초기 색인",
			fmt.Sprintf("색인 청크 %d개 · 대기 %d · 실행 중 %d · 실패 %d", chunks, pendingJobs, runningJobs, failedJobs),
			map[bool]string{true: "done", false: "todo"}[chunks > 0 && failedJobs == 0], "source-admin-section", ""),
		step("backup", "백업 예약",
			map[bool]string{true: "백업 설정이 저장되었습니다.", false: "예약 백업이 설정되지 않았습니다."}[configured["backup"]],
			map[bool]string{true: "done", false: "warn"}[configured["backup"]], "backup-admin-section", "backup"),
	}
	if failedJobs > 0 && chunks > 0 {
		steps[4]["status"] = "warn"
	}
	done := 0
	for _, item := range steps {
		if item["status"] == "done" {
			done++
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{
		"steps": steps, "completed": done, "total": len(steps),
		"ready": done == len(steps),
	})
}

// searchDiagnostics replays a code search with another user's ACL principals so
// an administrator can tell a missing claim from a missing index without asking
// the user to reproduce it. Snippets and file paths are never returned: the
// caller may legitimately administer the platform without holding source access.
func (a *App) searchDiagnostics(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var in struct {
		Username   string `json:"username"`
		Query      string `json:"query"`
		SourceType string `json:"sourceType"`
	}
	if decode(r, &in) != nil || strings.TrimSpace(in.Query) == "" || strings.TrimSpace(in.Username) == "" {
		problem(w, http.StatusBadRequest, "invalid_request", "username and query are required")
		return
	}
	var userID, subject, username, bitbucketSlug, gitlabID, groups, status string
	err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT u.id,u.subject,u.username,COALESCE(i.bitbucket_user_slug,''),COALESCE(i.gitlab_user_id,''),COALESCE(i.bitbucket_groups,''),u.status
FROM users u LEFT JOIN user_identities i ON i.user_id=u.id WHERE u.username=? OR u.subject=? OR u.id=?`),
		strings.TrimSpace(in.Username), strings.TrimSpace(in.Username), strings.TrimSpace(in.Username)).
		Scan(&userID, &subject, &username, &bitbucketSlug, &gitlabID, &groups, &status)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, http.StatusNotFound, "not_found", "No platform user matches that username, subject, or id")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	roles, _ := a.userRoles(r.Context(), userID)
	// The diagnostic must mirror the real search path, including the
	// administrator ACL bypass, or it explains a different system.
	principals := search.WithUnrestricted(sourceACLPrincipals(bitbucketSlug, gitlabID, splitCSV(groups)), roles)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, searchErr := a.search.SearchCode(ctx, principals, in.Query, in.SourceType, "", "", "", 20)
	a.audit(r, actor, "search.diagnostics", "user", userID, "success", map[string]any{"query": truncateText(in.Query, 200), "sourceType": in.SourceType})
	hitsByRepository := map[string]int{}
	for _, hit := range result.Hits {
		hitsByRepository[hit.LibraryID]++
	}
	repositories := make([]map[string]any, 0, len(result.Repositories))
	for _, item := range result.Repositories {
		repositories = append(repositories, map[string]any{"libraryId": item.LibraryID, "sourceType": item.SourceType, "hits": hitsByRepository[item.LibraryID]})
	}
	response := map[string]any{
		"target": map[string]any{
			"userId": userID, "username": username, "subject": subject, "status": status, "roles": roles,
			"aclPrincipals": principals, "aclReady": len(principals) > 0,
			"unrestrictedSearch": search.GrantsUnrestrictedSearch(roles),
		},
		"query": result.Query, "repositoryCount": len(result.Repositories), "hitCount": len(result.Hits),
		"repositories": repositories, "diagnostics": result.Diagnostics, "warning": result.Warning,
		"note": "Snippets and file paths are omitted; this view only explains why results are or are not visible.",
	}
	if searchErr != nil {
		response["error"] = searchErr.Error()
	}
	jsonOut(w, http.StatusOK, response)
}

// probeEmbedding embeds one short string so the screen can say whether the model
// endpoint answers right now. The stored counts and the circuit breaker only
// describe traffic that already happened, and the model is otherwise reached
// live only when an administrator saves the setting -- which left an operator
// unable to tell a dead model from a dead vector database, the two halves that
// degrade a semantic search in different ways.
func (a *App) probeEmbedding(ctx context.Context) map[string]any {
	settings, err := a.loadSettingMap(ctx, "model")
	if err != nil {
		return map[string]any{"ok": false, "stage": "configuration", "error": "embedding model is not configured"}
	}
	if configured, _ := settings["provider"].(string); configured != "openai-compatible" {
		return map[string]any{"ok": true, "provider": "local",
			"detail": "vectors are computed in-process, so there is no endpoint to reach"}
	}
	// Built straight from the setting rather than through the shared runtime.
	// That runtime caches vectors and short-circuits on an open breaker, so a
	// probe sent through it can be answered without the endpoint being touched
	// at all -- which is the one thing a liveness check must not do.
	provider, err := embeddingProviderFromMap(settings)
	if err != nil {
		return map[string]any{"ok": false, "stage": "configuration", "error": err.Error()}
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	vector, err := provider.Embed(ctx, "git-ctx embedding probe")
	if err != nil {
		return map[string]any{"ok": false, "stage": "request", "error": err.Error(),
			"latencyMs": time.Since(started).Milliseconds()}
	}
	if len(vector) == 0 {
		return map[string]any{"ok": false, "stage": "response", "error": "the model returned an empty vector",
			"latencyMs": time.Since(started).Milliseconds()}
	}
	return map[string]any{"ok": true, "dimensions": len(vector), "latencyMs": time.Since(started).Milliseconds()}
}

// vectorStatus reports whether the configured vector database is reachable and
// how many vectors it holds compared with the metadata database.
func (a *App) vectorStatus(w http.ResponseWriter, r *http.Request) {
	searchCfg := a.searchConfig(r.Context())
	embeddingHealth := a.embeddingHealth(r.Context())
	chunks, compatible := embeddingHealth.TotalChunks, embeddingHealth.EmbeddedChunks
	stored := embeddingHealth.StoredEmbeddings
	coverage := 0.0
	if chunks > 0 {
		coverage = float64(compatible) * 100 / float64(chunks)
	}
	policy := map[string]any{
		"retrievalMode": searchCfg.RetrievalMode, "requestedRetrievalMode": embeddingHealth.RequestedMode,
		"operationalMode": embeddingHealth.OperationalMode, "degradedReason": embeddingHealth.DegradedReason,
		"embeddingEnabled": searchCfg.UsesEmbeddings(), "circuit": embeddingHealth.Circuit,
		"storedVectors": stored, "compatibleVectors": compatible, "totalChunks": chunks, "chunksWithoutEmbeddings": chunks - stored, "embeddingCoveragePercent": coverage,
		"minimumEmbeddingCoveragePercent": embeddingHealth.MinimumCoverage,
		"incompatibleRefs":                embeddingHealth.IncompatibleRefs,
		"readyRefs":                       embeddingHealth.ReadyRefs, "partialRefs": embeddingHealth.PartialRefs,
		"degradedRefs": embeddingHealth.DegradedRefs, "unavailableRefs": embeddingHealth.UnavailableRefs,
	}
	// Probing costs an external call, so routine polling does not pay for it.
	if r.URL.Query().Get("probe") == "true" {
		policy["embeddingProbe"] = a.probeEmbedding(r.Context())
	}
	store, err := a.vectorStore(r.Context())
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		policy["configured"], policy["provider"] = false, "none"
		if searchCfg.UsesEmbeddings() {
			policy["detail"] = "외부 벡터 DB 없이 메타 DB에 저장된 임베딩을 애플리케이션에서 직접 채점합니다."
		} else {
			policy["detail"] = "관리자 검색 정책이 키워드 전용입니다. 임베딩과 벡터 DB는 검색에 사용되지 않습니다."
		}
		jsonOut(w, http.StatusOK, policy)
		return
	}
	if err != nil {
		problem(w, http.StatusBadRequest, "vector_unavailable", err.Error())
		return
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	status, statusErr := store.Status(ctx)
	response := policy
	for key, value := range map[string]any{
		"configured": true, "provider": status.Provider, "target": status.Target, "collection": status.Collection,
		"dimensions": status.Dimensions, "vectors": status.Vectors, "ready": status.Ready,
		"detail": status.Detail, "database": status.Database, "user": status.User,
		"extensionVersion": status.ExtensionVersion, "extensionSchema": status.ExtensionSchema,
	} {
		response[key] = value
	}
	if statusErr != nil {
		response["ready"] = false
		response["error"] = statusErr.Error()
	}
	jsonOut(w, http.StatusOK, response)
}

// vectorRebuild republishes every stored embedding, which is how an operator
// migrates to a vector database or switches between pgvector and Milvus.
func (a *App) vectorRebuild(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if !a.searchConfig(r.Context()).UsesEmbeddings() {
		problem(w, http.StatusConflict, "embedding_disabled", "검색 실행 모드에서 임베딩 사용이 비활성화되어 있습니다. 하이브리드 모드와 임베딩 모델을 먼저 설정하세요.")
		return
	}
	store, err := a.vectorStore(r.Context())
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		problem(w, http.StatusBadRequest, "vector_not_configured", "Select a vector database provider in the vector setting first")
		return
	}
	if err != nil {
		problem(w, http.StatusBadRequest, "vector_unavailable", err.Error())
		return
	}
	defer store.Close()
	// A rebuild walks the whole corpus, so it gets its own generous budget.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	var dimensions int
	var sample []byte
	if a.store.DB.QueryRowContext(ctx, `SELECT embedding FROM document_chunks WHERE embedding IS NOT NULL LIMIT 1`).Scan(&sample) == nil {
		dimensions = len(embedding.Decode(sample))
	}
	if dimensions > 0 {
		if err = store.Ensure(ctx, dimensions); err != nil {
			problem(w, http.StatusBadRequest, "vector_ensure_failed", err.Error())
			return
		}
	}
	projected, err := a.streamVectors(ctx, store, "", "")
	if err != nil {
		a.audit(r, p, "vector.rebuild", "vector", store.Name(), "failure", map[string]any{"projected": projected, "error": truncateText(err.Error(), 300)})
		problem(w, http.StatusBadGateway, "vector_rebuild_failed", err.Error())
		return
	}
	a.audit(r, p, "vector.rebuild", "vector", store.Name(), "success", map[string]any{"projected": projected})
	jsonOut(w, http.StatusOK, map[string]any{"provider": store.Name(), "projected": projected, "dimensions": dimensions})
}

// indexDiagnostics explains, per repository, whether content is searchable and
// what is blocking it. "Not indexed" has half a dozen very different causes -
// no job, a stalled worker, a failed download, a rejected embedding endpoint or
// a file policy that matched nothing - and an operator cannot act on the word
// "pending" alone.
func (a *App) indexDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := a.store.DB.QueryContext(ctx, `SELECT r.id,r.source_type,r.library_id,r.default_branch,r.indexed_at,
COALESCE(s.commit_id,''),COALESCE(s.embedding_revision,''),s.indexed_at,
(SELECT COUNT(*) FROM document_chunks c WHERE c.repository_id=r.id) AS chunks,
(SELECT COUNT(*) FROM code_symbols y WHERE y.repository_id=r.id) AS symbols
FROM repositories r LEFT JOIN repository_ref_states s ON s.repository_id=r.id AND s.ref_name=r.default_branch
WHERE r.enabled=1 ORDER BY r.library_id`)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	type repositoryDiagnostic struct {
		id                                                     string
		sourceType, libraryID, defaultBranch, commit, revision string
		indexedAt, refIndexedAt                                sql.NullTime
		chunks, symbols                                        int
	}
	var repositories []repositoryDiagnostic
	for rows.Next() {
		var item repositoryDiagnostic
		if err = rows.Scan(&item.id, &item.sourceType, &item.libraryID, &item.defaultBranch, &item.indexedAt,
			&item.commit, &item.revision, &item.refIndexedAt, &item.chunks, &item.symbols); err != nil {
			problem(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		repositories = append(repositories, item)
	}
	now := time.Now().UTC()
	out := make([]map[string]any, 0, len(repositories))
	counts := map[string]int{}
	for _, item := range repositories {
		var status, message string
		var attempts, files int
		var startedAt, completedAt sql.NullTime
		_ = a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT status,attempts,error_message,files_processed,started_at,completed_at
FROM index_jobs WHERE repository_id=? ORDER BY created_at DESC LIMIT 1`), item.id).Scan(&status, &attempts, &message, &files, &startedAt, &completedAt)
		state, detail, action := indexState(item.chunks, status, message, files, startedAt, now)
		counts[state]++
		entry := map[string]any{
			"repositoryId": item.id, "libraryId": item.libraryID, "sourceType": item.sourceType,
			"defaultBranch": item.defaultBranch, "chunks": item.chunks, "symbols": item.symbols,
			"commitId": item.commit, "embeddingRevision": item.revision,
			"state": state, "detail": detail, "action": action,
			"lastJob": map[string]any{"status": status, "attempts": attempts, "filesProcessed": files, "error": message},
		}
		if item.refIndexedAt.Valid {
			entry["refIndexedAt"] = item.refIndexedAt.Time
		}
		if startedAt.Valid {
			entry["startedAt"] = startedAt.Time
		}
		if completedAt.Valid {
			entry["completedAt"] = completedAt.Time
		}
		out = append(out, entry)
	}
	var pending, running, failed int64
	_ = a.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM index_jobs WHERE status='pending'`).Scan(&pending)
	_ = a.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM index_jobs WHERE status='running'`).Scan(&running)
	_ = a.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM index_jobs WHERE status='failed'`).Scan(&failed)
	jsonOut(w, http.StatusOK, map[string]any{
		"repositories": out, "states": counts,
		"queue":     map[string]int64{"pending": pending, "running": running, "failed": failed},
		"checkedAt": now,
	})
}

// indexState turns the raw job row into a state an operator can act on.
func indexState(chunks int, status, message string, files int, startedAt sql.NullTime, now time.Time) (state, detail, action string) {
	stalled := status == "running" && startedAt.Valid && now.Sub(startedAt.Time) > jobStallWarning
	switch {
	case status == "":
		return "never-run", "색인 작업이 한 번도 생성되지 않았습니다.", "등록 저장소 목록에서 [재색인]을 실행하세요."
	case status == "failed":
		return "failed", "마지막 색인 작업이 실패했습니다: " + truncateText(message, 300), "오류 원인을 해결한 뒤 [재시도]하세요."
	case stalled:
		return "stalled", "작업이 " + fmt.Sprint(int(now.Sub(startedAt.Time).Minutes())) + "분째 실행 중입니다.", "소스 서버 응답과 임베딩 엔드포인트를 확인하세요. 리스 시간이 지나면 자동으로 다시 큐에 넣습니다."
	case status == "running":
		return "indexing", "색인이 진행 중입니다. 처리 파일 " + fmt.Sprint(files) + "개.", "완료될 때까지 기다리세요. 검색은 소스 API failover로 동작합니다."
	case status == "pending" && strings.Contains(message, "일시 중단"):
		// The queue is not stuck: the connector is paused and the job is waiting
		// for it on purpose. Saying so keeps an operator from "fixing" the worker.
		return "source-paused", "소스 연동이 일시 중단되어 색인이 대기 중입니다: " + truncateText(message, 300),
			"소스·색인 화면의 연동 상태에서 원인을 확인하고, 복구되면 [지금 재시도]를 누르세요. 복구되면 자동으로 재개됩니다."
	case status == "pending":
		return "queued", "색인 작업이 대기 중입니다.", "Worker 동작과 메타 DB 연결을 확인하세요."
	case chunks == 0 && message != "":
		return "empty", "작업은 완료됐지만 색인된 내용이 없습니다: " + truncateText(message, 300), "색인 정책의 확장자와 제외 경로를 확인하세요."
	case chunks == 0:
		return "empty", "작업은 완료됐지만 색인된 청크가 0개입니다.", "색인 정책의 확장자와 제외 경로를 확인한 뒤 재색인하세요."
	case message != "":
		return "partial", "색인됐지만 일부 파일을 건너뛰었습니다: " + truncateText(message, 300), "건너뛴 파일이 필요하면 원인을 해결하고 재색인하세요."
	default:
		return "indexed", fmt.Sprintf("청크 %d개가 검색 가능합니다.", chunks), ""
	}
}

// webhookEvents shows what the source servers have actually sent. Without it an
// operator debugging "this repository does not index by itself" cannot separate
// a hook that was never configured from one that fires against a repository
// this platform does not have.
func (a *App) webhookEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 && value <= 500 {
		limit = value
	}
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	var received, queued, rejected int
	_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*),
COALESCE(SUM(CASE WHEN status='queued' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='rejected' THEN 1 ELSE 0 END),0)
FROM webhook_events WHERE received_at>=?`), since).Scan(&received, &queued, &rejected)

	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT e.id,e.source_type,e.external_event_id,e.event_type,e.status,e.detail,e.received_at,
COALESCE(r.library_id,''),COALESCE(e.repository_id,'')
FROM webhook_events e LEFT JOIN repositories r ON r.id=e.repository_id
ORDER BY e.received_at DESC LIMIT ?`), limit)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	events := []map[string]any{}
	for rows.Next() {
		var id, sourceType, externalID, eventType, status, detail, libraryID, repositoryID string
		var receivedAt time.Time
		if err = rows.Scan(&id, &sourceType, &externalID, &eventType, &status, &detail, &receivedAt, &libraryID, &repositoryID); err != nil {
			problem(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		// A rejected event has no repository row, so the identifier the sender
		// used is shown instead — that is what has to be matched up.
		target := libraryID
		if target == "" {
			target = repositoryID
		}
		events = append(events, map[string]any{
			"id": id, "sourceType": sourceType, "externalEventId": externalID, "eventType": eventType,
			"status": status, "detail": detail, "receivedAt": receivedAt, "target": target,
		})
	}
	jsonOut(w, http.StatusOK, map[string]any{
		"events": events,
		"window": map[string]any{"days": 7, "received": received, "queued": queued, "rejected": rejected},
	})
}

// dependencyInventory is the catalogue-wide view of what the estate depends on.
// It is scoped to the caller's own ACL like every other search, so an operator
// without repository access does not learn the inventory of repositories they
// cannot read.
func (a *App) dependencyInventory(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	limit := 50
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 {
		limit = value
	}
	result, err := a.search.DependencyInventorySummary(r.Context(), searchPrincipals(p), r.URL.Query().Get("ecosystem"), limit)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, result)
}

func (a *App) adminFreshness(w http.ResponseWriter, r *http.Request) {
	sloMinutes := 60
	if settings, err := a.loadSettingMap(r.Context(), "index"); err == nil {
		if value, ok := settings["freshnessSloMinutes"].(float64); ok && value >= 5 && value <= 10080 {
			sloMinutes = int(value)
		}
	}
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT r.id,r.source_type,r.library_id,r.default_branch,r.indexed_at,
COALESCE(s.commit_id,''),s.indexed_at
FROM repositories r LEFT JOIN repository_ref_states s ON s.repository_id=r.id AND s.ref_name=r.default_branch
WHERE r.enabled=1 ORDER BY r.source_type,r.library_id`)
	if err != nil {
		problem(w, 500, "freshness_failed", err.Error())
		return
	}
	defer rows.Close()
	now := time.Now().UTC()
	var items []map[string]any
	var stale int
	sourceCounts := map[string]int{}
	for rows.Next() {
		var id, sourceType, libraryID, defaultRef, commit string
		var repositoryIndexed, refIndexed sql.NullTime
		if err = rows.Scan(&id, &sourceType, &libraryID, &defaultRef, &repositoryIndexed, &commit, &refIndexed); err != nil {
			problem(w, 500, "freshness_failed", err.Error())
			return
		}
		sourceCounts[sourceType]++
		indexedAt := repositoryIndexed
		if refIndexed.Valid {
			indexedAt = refIndexed
		}
		ageMinutes := -1
		status := "never-indexed"
		if indexedAt.Valid {
			ageMinutes = int(now.Sub(indexedAt.Time).Minutes())
			status = "fresh"
			if ageMinutes > sloMinutes {
				status = "stale"
			}
		}
		if status != "fresh" {
			stale++
		}
		items = append(items, map[string]any{"repositoryId": id, "sourceType": sourceType, "libraryId": libraryID, "ref": defaultRef, "commitId": commit, "indexedAt": indexedAt, "ageMinutes": ageMinutes, "status": status})
	}
	jsonOut(w, 200, map[string]any{"checkedAt": now, "sloMinutes": sloMinutes, "repositoryCount": len(items), "staleCount": stale, "sourceCounts": sourceCounts, "repositories": items})
}
func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	type metric struct{ name, help, query string }
	items := []metric{
		{"git_ctx_repositories", "Enabled source repositories", `SELECT COUNT(*) FROM repositories WHERE enabled=1`},
		{"git_ctx_document_chunks", "Indexed document chunks", `SELECT COUNT(*) FROM document_chunks`},
		{"git_ctx_api_keys_active", "Active MCP API keys", `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND disabled_at IS NULL AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP)`},
		{"git_ctx_managed_secrets_active", "Active managed secrets", `SELECT COUNT(*) FROM managed_secrets WHERE status='active'`},
		{"git_ctx_mcp_calls_total", "Recorded MCP tool calls", `SELECT COUNT(*) FROM mcp_calls`},
		{"git_ctx_index_jobs_pending", "Pending index jobs", `SELECT COUNT(*) FROM index_jobs WHERE status='pending'`},
		{"git_ctx_index_jobs_failed", "Failed index jobs", `SELECT COUNT(*) FROM index_jobs WHERE status='failed'`},
		{"git_ctx_quality_benchmark_cases", "Enabled search quality benchmark cases", `SELECT COUNT(*) FROM quality_benchmark_cases WHERE enabled=1`},
		{"git_ctx_quality_benchmark_regressions", "Search quality benchmark regression runs", `SELECT COUNT(*) FROM quality_benchmark_runs WHERE status='regressed'`},
	}
	for _, item := range items {
		var value int64
		if err := a.store.DB.QueryRowContext(r.Context(), item.query).Scan(&value); err != nil {
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", item.name, item.help, item.name, item.name, value)
	}
	databaseUp := 1
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	if err := a.store.DB.PingContext(ctx); err != nil {
		databaseUp = 0
	}
	cancel()
	fmt.Fprintf(w, "# HELP git_ctx_database_up Metadata database connectivity\n# TYPE git_ctx_database_up gauge\ngit_ctx_database_up %d\n", databaseUp)
	fmt.Fprintf(w, "# HELP git_ctx_go_goroutines Current goroutines\n# TYPE git_ctx_go_goroutines gauge\ngit_ctx_go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP git_ctx_tracing_enabled OTLP tracing provider enabled\n# TYPE git_ctx_tracing_enabled gauge\ngit_ctx_tracing_enabled %d\n", boolInt(a.traces.Enabled()))
	embeddingHealth := a.embeddingHealth(r.Context())
	fmt.Fprintf(w, "# HELP git_ctx_embedding_coverage_percent Percentage of indexed chunks compatible with the configured embedding revision\n# TYPE git_ctx_embedding_coverage_percent gauge\ngit_ctx_embedding_coverage_percent %.3f\n", embeddingHealth.CoveragePercent)
	fmt.Fprintf(w, "# HELP git_ctx_embedding_incompatible_refs Refs whose stored embeddings use another model revision\n# TYPE git_ctx_embedding_incompatible_refs gauge\ngit_ctx_embedding_incompatible_refs %d\n", embeddingHealth.IncompatibleRefs)
	fmt.Fprintf(w, "# HELP git_ctx_embedding_circuit_open Whether the embedding endpoint circuit is open\n# TYPE git_ctx_embedding_circuit_open gauge\ngit_ctx_embedding_circuit_open %d\n", boolInt(embeddingHealth.Circuit.State == "open"))
	fmt.Fprintf(w, "# HELP git_ctx_embedding_requests_total Process-local embedding provider requests\n# TYPE git_ctx_embedding_requests_total counter\ngit_ctx_embedding_requests_total %d\n", embeddingHealth.Circuit.Requests)
	fmt.Fprintf(w, "# HELP git_ctx_embedding_failures_total Process-local embedding provider failures\n# TYPE git_ctx_embedding_failures_total counter\ngit_ctx_embedding_failures_total %d\n", embeddingHealth.Circuit.Failures)
	fmt.Fprintf(w, "# HELP git_ctx_embedding_cache_hits_total Process-local query and chunk embedding cache hits\n# TYPE git_ctx_embedding_cache_hits_total counter\ngit_ctx_embedding_cache_hits_total %d\n", embeddingHealth.Circuit.CacheHits)
	fmt.Fprintf(w, "# HELP git_ctx_embedding_coalesced_total Concurrent identical query embeddings joined to one provider request\n# TYPE git_ctx_embedding_coalesced_total counter\ngit_ctx_embedding_coalesced_total %d\n", embeddingHealth.Circuit.Coalesced)
	reasons := make([]string, 0, len(embeddingHealth.Circuit.Fallbacks))
	for reason := range embeddingHealth.Circuit.Fallbacks {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	fmt.Fprint(w, "# HELP git_ctx_embedding_fallback_total Automatic embedding fallback decisions by reason\n# TYPE git_ctx_embedding_fallback_total counter\n")
	for _, reason := range reasons {
		fmt.Fprintf(w, "git_ctx_embedding_fallback_total{reason=%q} %d\n", reason, embeddingHealth.Circuit.Fallbacks[reason])
	}
}

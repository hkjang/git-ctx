package app

import (
	"bytes"
	"context"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/backup"
	runtimelogging "git-ctx/internal/logging"
	outboundnotification "git-ctx/internal/notification"
	"git-ctx/internal/observability"
	"git-ctx/internal/opensearch"
	"git-ctx/internal/scheduler"
	"git-ctx/internal/search"
	secretstore "git-ctx/internal/secret"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/vectorstore"
	"git-ctx/internal/version"
)

// Platform settings: reading, validating, masking secrets, versioning and
// restoring the configuration an operator edits.

func (a *App) listSettings(w http.ResponseWriter, r *http.Request) {
	rows, e := a.store.DB.QueryContext(r.Context(), `SELECT category,version,updated_by,updated_at FROM system_settings ORDER BY category`)
	if e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var c, u string
		var v int
		var t time.Time
		_ = rows.Scan(&c, &v, &u, &t)
		out = append(out, map[string]any{"category": c, "version": v, "updatedBy": u, "updatedAt": t})
	}
	jsonOut(w, 200, out)
}

func configuredDatabaseDSN(ctx context.Context, s *store.Store, aead cipher.AEAD) (string, error) {
	var sealed []byte
	if err := s.DB.QueryRowContext(ctx, s.Rebind(`SELECT value_encrypted FROM system_settings WHERE category=?`), "database").Scan(&sealed); err != nil {
		return "", err
	}
	nonceSize := aead.NonceSize()
	if len(sealed) < nonceSize {
		return "", errors.New("stored database setting is truncated")
	}
	raw, err := aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
	if err != nil {
		return "", errors.New("stored database setting cannot be decrypted")
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	dsn, _ := value["dsn"].(string)
	return strings.TrimSpace(dsn), nil
}

func (a *App) saveDatabaseTarget(ctx context.Context, target *store.Store, dsn, actor, reason string) error {
	raw, _ := json.Marshal(map[string]any{"dsn": dsn, "driver": "postgres", "activation": "restart"})
	sealed, err := a.seal(raw)
	if err != nil {
		return err
	}
	// Write the target first. The recovery database is the activation source;
	// updating it last prevents a partial target-setting failure from causing an
	// unreported switch on the next restart.
	for _, destination := range []*store.Store{target, a.store} {
		tx, txErr := destination.DB.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		var version int
		txErr = tx.QueryRowContext(ctx, destination.Rebind(`SELECT version FROM system_settings WHERE category=?`), "database").Scan(&version)
		if errors.Is(txErr, sql.ErrNoRows) {
			version, txErr = 0, nil
		}
		version++
		if txErr == nil {
			_, txErr = tx.ExecContext(ctx, destination.Rebind(`INSERT INTO setting_versions(category,version,value_encrypted,changed_by,reason) VALUES(?,?,?,?,?)`), "database", version, sealed, actor, reason)
		}
		if txErr == nil {
			_, txErr = tx.ExecContext(ctx, destination.Rebind(`INSERT INTO system_settings(category,version,value_encrypted,updated_by,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(category) DO UPDATE SET version=excluded.version,value_encrypted=excluded.value_encrypted,updated_by=excluded.updated_by,updated_at=excluded.updated_at`), "database", version, sealed, actor, time.Now().UTC())
		}
		if txErr != nil {
			tx.Rollback()
			return txErr
		}
		if txErr = tx.Commit(); txErr != nil {
			return txErr
		}
	}
	return nil
}

func (a *App) putSetting(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	category := r.PathValue("category")
	allowed := settingCategories()
	if !allowed[category] {
		problem(w, 400, "invalid_category", "Unsupported setting category")
		return
	}
	var value map[string]any
	if decode(r, &value) != nil {
		problem(w, 400, "invalid_request", "Invalid JSON")
		return
	}
	// expectedVersion is the version the editor loaded. It travels in the body so
	// the JSON editor and the field form can both send it, and it is removed
	// before the value is stored.
	expected := 0
	if raw, ok := value["expectedVersion"]; ok {
		if number, ok := raw.(float64); ok {
			expected = int(number)
		}
		delete(value, "expectedVersion")
	}
	// force saves a configuration whose target is unreachable right now. A
	// maintenance window on the source server must not stop an administrator from
	// preparing the configuration, but the skip is recorded and reported.
	force := r.URL.Query().Get("force") == "true"
	unresolvedSecrets := []string{}
	previous, err := a.loadSettingMapRaw(r.Context(), category)
	previouslyConfigured := err == nil
	if err != nil {
		previous = map[string]any{}
	}
	unresolvedSecrets = preserveMasked(previous, value)
	previousRetrievalMode := ""
	previousEmbeddingFingerprint := ""
	if category == "search" {
		previousRetrievalMode = retrievalModeFromSetting(previous)
	} else if category == "model" {
		previousEmbeddingFingerprint = embeddingConfigFingerprint(previous)
	}
	if err := a.normalizeSetting(r.Context(), category, value); err != nil {
		problem(w, 400, "setting_normalization_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	validationSkipped := ""
	if err := a.validateSetting(ctx, category, value); err != nil {
		if !force {
			problem(w, 400, "setting_validation_failed", err.Error())
			return
		}
		validationSkipped = err.Error()
	}
	raw, _ := json.Marshal(value)
	sealed, e := a.seal(raw)
	if e != nil {
		problem(w, 500, "internal_error", "Encryption failed")
		return
	}
	tx, e := a.store.DB.BeginTx(r.Context(), nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	// The next version continues the history rather than the live row. Deleting a
	// setting removes the live row but keeps its versions, so restarting at 1
	// collided with the history that is still there and made "delete, then
	// configure again" fail with a constraint error.
	var version, historyVersion int
	e = tx.QueryRowContext(r.Context(), a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), category).Scan(&version)
	if errors.Is(e, sql.ErrNoRows) {
		version = 0
	} else if e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	if e = tx.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COALESCE(MAX(version),0) FROM setting_versions WHERE category=?`), category).Scan(&historyVersion); e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	// A caller that loaded version N may only save over version N. Without this
	// two administrators editing the same category silently overwrite each other.
	if expected > 0 && expected != version {
		problem(w, http.StatusConflict, "setting_version_conflict",
			fmt.Sprintf("이 설정은 이미 v%d 입니다. 다른 관리자가 저장한 내용을 덮어쓰지 않도록 화면을 새로 불러온 뒤 다시 저장하세요.", version))
		return
	}
	version = max(version, historyVersion) + 1
	_, e = tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO setting_versions(category,version,value_encrypted,changed_by,reason) VALUES(?,?,?,?,?)`), category, version, sealed, p.UserID, "")
	if e == nil {
		_, e = tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO system_settings(category,version,value_encrypted,updated_by,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(category) DO UPDATE SET version=excluded.version,value_encrypted=excluded.value_encrypted,updated_by=excluded.updated_by,updated_at=excluded.updated_at`), category, version, sealed, p.UserID, time.Now().UTC())
	}
	if e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	if e = tx.Commit(); e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	if category == "observability" {
		if e = a.traces.Apply(r.Context(), observabilityConfigFromMap(value)); e != nil {
			problem(w, 500, "setting_apply_failed", "Observability setting was saved but could not be applied")
			return
		}
	}
	if category == "logging" {
		if e = runtimelogging.Apply(stringValue(value, "level")); e != nil {
			problem(w, 500, "setting_apply_failed", "Logging setting was saved but could not be applied")
			return
		}
	}
	a.audit(r, p, "settings.update", category, category, "success", map[string]any{"version": version, "validationSkipped": validationSkipped})
	result := map[string]any{"category": category, "version": version, "secretFields": "encrypted and masked", "applied": true, "appliedAt": time.Now().UTC()}
	if len(unresolvedSecrets) > 0 {
		result["missingSecrets"] = unresolvedSecrets
		result["warning"] = fmt.Sprintf("저장된 값이 없는 비밀 항목(%s)은 비워 둔 채 저장했습니다. 실제 값을 입력해 다시 저장하세요.", strings.Join(unresolvedSecrets, ", "))
	}
	if validationSkipped != "" {
		result["validationSkipped"] = validationSkipped
		result["warning"] = "연결 검증에 실패했지만 요청에 따라 저장했습니다. 대상 서버가 복구되면 [연동 테스트]로 다시 확인하세요."
	}
	if category == "operations" {
		result["restartRequired"] = true
		result["dynamicFields"] = []string{"maintenanceMode", "maintenanceMessage"}
	}
	if category == "keycloak" {
		result["issuerUrl"] = value["issuerUrl"]
		result["redirectUrl"] = value["redirectUrl"]
		result["loginTestUrl"] = "/auth/login?return_to=/"
	}
	retrievalPolicyChanged := category == "search" && (!previouslyConfigured || previousRetrievalMode != retrievalModeFromSetting(value))
	embeddingModelChanged := category == "model" && (!previouslyConfigured || previousEmbeddingFingerprint != embeddingConfigFingerprint(value))
	if retrievalPolicyChanged || embeddingModelChanged {
		queued, queueErr := a.enqueueRetrievalReindex(r.Context())
		if queueErr != nil {
			result["warning"] = "검색 실행 모드는 즉시 적용됐지만 자동 재색인 작업을 등록하지 못했습니다: " + queueErr.Error()
		} else {
			result["reindexJobsQueued"] = queued
			result["indexTransition"] = "기존 검색 데이터는 계속 제공되며, 새 정책으로 저장소를 순차 재색인합니다."
		}
	}
	jsonOut(w, 200, result)
}

func (a *App) deleteSetting(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, http.StatusNotFound, "not_found", "Setting category not found")
		return
	}
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM system_settings WHERE category=?`), category)
	if err != nil {
		problem(w, http.StatusInternalServerError, "setting_delete_failed", "Unable to delete setting")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		problem(w, http.StatusNotFound, "not_found", "Setting category not configured")
		return
	}
	if category == "logging" {
		runtimelogging.Reset()
	}
	detail := map[string]any{}
	if category == "search" || category == "model" {
		queued, queueErr := a.enqueueRetrievalReindex(r.Context())
		detail["reindexJobsQueued"] = queued
		if queueErr != nil {
			detail["reindexQueueError"] = queueErr.Error()
		}
	}
	a.audit(r, p, "settings.delete", category, category, "success", detail)
	w.WriteHeader(http.StatusNoContent)
}

// validateIntegrationSetting runs normalization and validation for any setting
// category without persisting it, so every settings tab can be checked before
// saving even when the category has no external endpoint to connect to.
func (a *App) validateIntegrationSetting(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, http.StatusBadRequest, "invalid_category", "Unsupported setting category")
		return
	}
	var value map[string]any
	if decode(r, &value) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "Invalid JSON")
		return
	}
	if previous, err := a.loadSettingMapRaw(r.Context(), category); err == nil {
		preserveMasked(previous, value)
	}
	if err := a.normalizeSetting(r.Context(), category, value); err != nil {
		problem(w, http.StatusBadRequest, "setting_normalization_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.validateSetting(ctx, category, value); err != nil {
		problem(w, http.StatusBadRequest, "setting_validation_failed", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{
		"category": category, "status": "valid", "normalized": maskedCopy(value), "checkedAt": time.Now().UTC(),
	})
}

// maskedCopy returns the value with every secret replaced, so a validation
// response can echo the effective configuration without leaking credentials.
func maskedCopy(value map[string]any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var clone map[string]any
	if json.Unmarshal(raw, &clone) != nil {
		return map[string]any{}
	}
	maskSecrets(clone)
	return clone
}

func (a *App) testIntegrationSetting(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	category := r.PathValue("category")
	allowed := map[string]bool{"keycloak": true, "bitbucket": true, "gitlab": true, "confluence": true, "jira": true, "model": true, "opensearch": true, "vault": true, "vector": true, "observability": true, "backup": true, "notifications": true}
	if !allowed[category] {
		problem(w, 400, "setting_test_unsupported", "This setting category has no external or storage connection test")
		return
	}
	var value map[string]any
	if decode(r, &value) != nil {
		problem(w, 400, "invalid_request", "Invalid JSON")
		return
	}
	if previous, err := a.loadSettingMapRaw(r.Context(), category); err == nil {
		preserveMasked(previous, value)
	}
	if err := a.normalizeSetting(r.Context(), category, value); err != nil {
		problem(w, 400, "setting_normalization_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.validateSetting(ctx, category, value); err != nil {
		a.audit(r, p, "settings.test", category, category, "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 400, "setting_connection_test_failed", err.Error())
		return
	}
	if category == "notifications" {
		cfg, cfgErr := notificationDeliveryConfigFromMap(value, time.Now().UTC())
		if cfgErr != nil {
			problem(w, 400, "setting_connection_test_failed", cfgErr.Error())
			return
		}
		if cfgErr = outboundnotification.Validate(ctx, cfg); cfgErr != nil {
			a.audit(r, p, "settings.test", category, category, "failure", map[string]any{"error": truncateText(cfgErr.Error(), 500)})
			problem(w, 400, "setting_connection_test_failed", cfgErr.Error())
			return
		}
	}
	details := map[string]any{}
	if category == "bitbucket" || category == "gitlab" {
		sourceValue := value
		if a.secrets != nil {
			resolved, resolveErr := a.secrets.Resolve(ctx, value)
			if resolveErr != nil {
				problem(w, 400, "setting_query_search_test_failed", resolveErr.Error())
				return
			}
			sourceValue = resolved
		}
		queryStatus, queryErr := testSourceQueryAPI(ctx, category, sourceValue)
		if queryErr != nil {
			a.audit(r, p, "settings.test", category, category, "failure", map[string]any{"component": "query-search", "error": truncateText(queryErr.Error(), 500)})
			problem(w, 400, "setting_query_search_test_failed", queryErr.Error())
			return
		}
		details["querySearch"] = queryStatus
	}
	a.audit(r, p, "settings.test", category, category, "success", nil)
	jsonOut(w, http.StatusOK, map[string]any{"category": category, "status": "verified", "details": details, "testedAt": time.Now().UTC()})
}

func testSourceQueryAPI(ctx context.Context, sourceType string, value map[string]any) (map[string]any, error) {
	adapter, err := sourceAdapterFromMap(sourceType, value)
	if err != nil {
		return nil, err
	}
	searcher, ok := adapter.(source.QuerySearcher)
	if !ok {
		return nil, fmt.Errorf("%s adapter does not support query search", sourceType)
	}
	projects, err := adapter.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s project discovery for query search: %w", sourceType, err)
	}
	if len(projects) == 0 {
		return map[string]any{"status": "skipped", "reason": "no accessible project"}, nil
	}
	repositories, err := adapter.ListRepositories(ctx, projects[0].Key)
	if err != nil {
		return nil, fmt.Errorf("%s repository discovery for query search: %w", sourceType, err)
	}
	if len(repositories) == 0 {
		return map[string]any{"status": "skipped", "reason": "no accessible repository"}, nil
	}
	repository := repositories[0]
	ref := repository.DefaultBranch
	if ref == "" {
		ref = "main"
	}
	query := strings.TrimSpace(stringValue(value, "searchTestQuery"))
	if query == "" {
		query = repository.Slug
	}
	hits, err := searcher.SearchQuery(ctx, source.RepositoryRef{ProjectKey: repository.ProjectKey, Slug: repository.Slug}, ref, query, 1)
	if err != nil {
		return nil, fmt.Errorf("%s query search API test: %w", sourceType, err)
	}
	return map[string]any{
		"status": "verified", "project": repository.ProjectKey, "repository": repository.Slug,
		"ref": ref, "query": query, "matches": len(hits),
	}, nil
}
func (a *App) getSetting(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, 404, "not_found", "Setting category not found")
		return
	}
	var sealed []byte
	var version int
	var updatedBy string
	var updatedAt time.Time
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT version,value_encrypted,updated_by,updated_at FROM system_settings WHERE category=?`), category).Scan(&version, &sealed, &updatedBy, &updatedAt); err != nil {
		problem(w, 404, "not_found", "Setting category not configured")
		return
	}
	raw, err := a.open(sealed)
	if err != nil {
		problem(w, 500, "decrypt_failed", "Unable to decrypt setting")
		return
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		problem(w, 500, "invalid_stored_setting", "Stored setting is invalid")
		return
	}
	maskedFields := maskSecrets(value)
	slices.Sort(maskedFields)
	jsonOut(w, 200, map[string]any{
		"category": category, "version": version, "value": value,
		"updatedBy": updatedBy, "updatedAt": updatedAt, "maskedFields": maskedFields,
	})
}
func (a *App) loadSettingMap(ctx context.Context, category string) (map[string]any, error) {
	value, err := a.loadSettingMapRaw(ctx, category)
	if err != nil || a.secrets == nil || category == "vault" {
		return value, err
	}
	return a.secrets.Resolve(ctx, value)
}
func (a *App) loadSettingMapRaw(ctx context.Context, category string) (map[string]any, error) {
	var sealed []byte
	if err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT value_encrypted FROM system_settings WHERE category=?`), category).Scan(&sealed); err != nil {
		return nil, err
	}
	raw, err := a.open(sealed)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
func (a *App) publicURL(ctx context.Context) string {
	if settings, err := a.loadSettingMap(ctx, "ui"); err == nil {
		if value, ok := settings["publicUrl"].(string); ok {
			if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
				return strings.TrimSuffix(value, "/")
			}
		}
	}
	return strings.TrimSuffix(a.cfg.PublicURL, "/")
}

func (a *App) normalizeSetting(ctx context.Context, category string, value map[string]any) error {
	if category == "search" {
		value["retrievalMode"] = retrievalModeFromSetting(value)
		delete(value, "embeddingEnabled")
		return nil
	}
	if category != "keycloak" {
		return nil
	}
	baseURL := strings.TrimSpace(stringValue(value, "baseUrl"))
	realm := strings.TrimSpace(stringValue(value, "realm"))
	if strings.TrimSpace(stringValue(value, "clientId")) == "" {
		return errors.New("keycloak clientId is required")
	}
	issuer := strings.TrimSpace(stringValue(value, "issuerUrl"))
	if baseURL != "" || realm != "" {
		if baseURL == "" || realm == "" {
			return errors.New("keycloak baseUrl and realm must be configured together")
		}
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return errors.New("keycloak.baseUrl must be an absolute URL without credentials or fragment")
		}
		issuer = strings.TrimRight(parsed.String(), "/") + "/realms/" + url.PathEscape(realm)
	} else if issuer == "" {
		return errors.New("keycloak baseUrl and realm are required")
	}
	value["issuerMode"], value["issuerUrl"] = "auto", issuer
	public := strings.TrimRight(a.publicURL(ctx), "/")
	value["redirectMode"], value["redirectUrl"], value["postLogoutRedirectUrl"] = "auto", public+"/auth/callback", public+"/"
	value["tlsVerify"] = true
	delete(value, "caCertificate")
	delete(value, "proxyUrl")
	delete(value, "timeoutSeconds")
	return nil
}
func (a *App) validateSetting(ctx context.Context, category string, value map[string]any) error {
	if a.secrets != nil && category != "vault" {
		resolved, err := a.secrets.Resolve(ctx, value)
		if err != nil {
			return err
		}
		value = resolved
	}
	switch category {
	case "keycloak":
		raw, _ := json.Marshal(value)
		var cfg auth.OIDCConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		_, err := auth.OAuthConfig(ctx, cfg)
		return err
	case "bitbucket", "gitlab", "confluence", "jira":
		adapter, err := sourceAdapterFromMap(category, value)
		if err != nil {
			return err
		}
		projects, err := adapter.ListProjects(ctx)
		if err != nil {
			return fmt.Errorf("%s connection test: %w", category, err)
		}
		if len(projects) == 0 {
			return nil
		}
		repositories, err := adapter.ListRepositories(ctx, projects[0].Key)
		if err != nil {
			return fmt.Errorf("%s repository discovery test: %w", category, err)
		}
		if len(repositories) == 0 {
			return nil
		}
		repository := source.RepositoryRef{ProjectKey: repositories[0].ProjectKey, Slug: repositories[0].Slug}
		if _, err = adapter.GetPermissions(ctx, repository); err != nil {
			return fmt.Errorf("%s ACL synchronization test: %w", category, err)
		}
		if _, err = adapter.ListBranches(ctx, repository); err != nil {
			return fmt.Errorf("%s branch discovery test: %w", category, err)
		}
	case "ui":
		if value, ok := value["publicUrl"].(string); ok && value != "" {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return errors.New("ui.publicUrl must be an absolute URL")
			}
			if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
				return errors.New("ui.publicUrl must use HTTPS outside localhost")
			}
		}
		for _, key := range []string{"logoUrl", "faviconUrl"} {
			if value, ok := value[key].(string); ok && value != "" {
				if err := validatePublicAssetURL(value); err != nil {
					return fmt.Errorf("ui.%s: %w", key, err)
				}
			}
		}
	case "mcp":
		if origins, ok := value["allowedOrigins"].([]any); ok {
			for _, item := range origins {
				origin, ok := item.(string)
				if !ok || strings.TrimSpace(origin) == "" {
					return errors.New("mcp.allowedOrigins must contain non-empty URL strings")
				}
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
					return fmt.Errorf("mcp.allowedOrigins contains invalid origin %q", origin)
				}
				if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
					return fmt.Errorf("mcp.allowedOrigins must use HTTPS outside localhost: %q", origin)
				}
			}
		}
		if size, ok := value["maxRequestBytes"].(float64); ok && (size < 1024 || size > 16<<20) {
			return errors.New("mcp.maxRequestBytes must be 1024..16777216")
		}
	case "index":
		if minutes, ok := value["pollingMinutes"].(float64); ok && (minutes < 1 || minutes > 10080) {
			return errors.New("index.pollingMinutes must be 1..10080")
		}
	case "security":
		if cidrs, ok := value["trustedProxyCidrs"].([]any); ok {
			for _, item := range cidrs {
				cidr, ok := item.(string)
				if !ok {
					return errors.New("security.trustedProxyCidrs must contain CIDR strings")
				}
				if _, err := netip.ParsePrefix(cidr); err != nil {
					return fmt.Errorf("security.trustedProxyCidrs contains invalid CIDR %q", cidr)
				}
			}
		}
	case "search":
		number := func(key string, fallback float64) float64 {
			if x, ok := value[key].(float64); ok {
				return x
			}
			return fallback
		}
		keyword, vector := number("keywordWeight", 1), number("vectorWeight", .35)
		mode := retrievalModeFromSetting(value)
		switch mode {
		case search.RetrievalKeywordOnly, search.RetrievalHybridFallback, search.RetrievalHybridRequired:
		default:
			return errors.New("search.retrievalMode must be keyword-only, hybrid-fallback, or hybrid-required")
		}
		finalK, candidates, rerankLimit := number("finalK", 8), number("candidateLimit", 5000), number("rerankLimit", 30)
		coverage := number("minimumEmbeddingCoveragePercent", 80)
		failureThreshold := number("embeddingFailureThreshold", 2)
		cooldown := number("embeddingCooldownSeconds", 60)
		cacheSeconds := number("embeddingCacheSeconds", 120)
		if keyword < 0 || vector < 0 || keyword+vector == 0 {
			return errors.New("search weights must be non-negative and not both zero")
		}
		if finalK < 1 || finalK > 50 || candidates < 10 || candidates > 20000 {
			return errors.New("search finalK must be 1..50 and candidateLimit 10..20000")
		}
		if rerankLimit < 1 || rerankLimit > 100 {
			return errors.New("search rerankLimit must be 1..100")
		}
		if coverage < 0 || coverage > 100 {
			return errors.New("search.minimumEmbeddingCoveragePercent must be 0..100")
		}
		if failureThreshold < 1 || failureThreshold > 10 {
			return errors.New("search.embeddingFailureThreshold must be 1..10")
		}
		if cooldown < 5 || cooldown > 3600 {
			return errors.New("search.embeddingCooldownSeconds must be 5..3600")
		}
		if cacheSeconds < 0 || cacheSeconds > 3600 {
			return errors.New("search.embeddingCacheSeconds must be 0..3600")
		}
		if mode == search.RetrievalHybridRequired {
			model, modelErr := a.loadSettingMap(ctx, "model")
			provider, _ := model["provider"].(string)
			if modelErr != nil || provider != "openai-compatible" {
				return errors.New("hybrid-required needs a tested OpenAI-compatible embedding model setting")
			}
		}
	case "model":
		provider, err := embeddingProviderFromMap(value)
		if err != nil {
			return err
		}
		if _, err = provider.Embed(ctx, "git-ctx embedding connection test"); err != nil {
			return fmt.Errorf("embedding connection test: %w", err)
		}
		reranker, err := rerankerProviderFromMap(value)
		if err != nil {
			return err
		}
		if reranker != nil {
			if _, err = reranker.Rerank(ctx, "git-ctx reranker connection test", []string{"relevant document", "unrelated document"}); err != nil {
				return fmt.Errorf("reranker connection test: %w", err)
			}
		}
	case "vector":
		cfg := vectorstore.FromMap(value)
		if !cfg.Enabled() {
			return nil
		}
		_, err := vectorstore.TestConnection(ctx, cfg, a.postgresDSN(ctx))
		return err
	case "opensearch":
		cfg, err := openSearchConfigFromMap(value)
		if err != nil {
			return err
		}
		if !cfg.Enabled {
			return nil
		}
		client, err := opensearch.New(cfg)
		if err != nil {
			return err
		}
		return client.Validate(ctx)
	case "vault":
		cfg, err := vaultConfigFromMap(value)
		if err != nil {
			return err
		}
		if !cfg.Enabled {
			return nil
		}
		client, err := secretstore.NewVault(cfg.VaultConfig)
		if err != nil {
			return err
		}
		return client.Validate(ctx)
	case "observability":
		return observability.Validate(ctx, observabilityConfigFromMap(value))
	case "backup":
		return backup.ValidateStorage(backupConfigFromMap(value, a.cfg.BackupDirectory))
	case "logging":
		_, err := runtimelogging.Parse(stringValue(value, "level"))
		return err
	case "operations":
		address := strings.TrimSpace(stringValue(value, "listenAddress"))
		if address != "" {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port == "" {
				return errors.New("operations.listenAddress must be a host:port address such as :4747")
			}
			if host != "" && net.ParseIP(host) == nil && host != "localhost" {
				return errors.New("operations.listenAddress host must be an IP address, localhost, or empty")
			}
			portNumber, err := strconv.Atoi(port)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return errors.New("operations.listenAddress port must be 1..65535")
			}
		}
		for _, key := range []string{"readHeaderTimeoutSeconds", "readTimeoutSeconds", "writeTimeoutSeconds", "idleTimeoutSeconds", "shutdownTimeoutSeconds"} {
			if seconds, ok := value[key].(float64); ok && (seconds < 1 || seconds > 600) {
				return fmt.Errorf("operations.%s must be 1..600", key)
			}
		}
		if message := stringValue(value, "maintenanceMessage"); len(message) > 500 {
			return errors.New("operations.maintenanceMessage must be at most 500 characters")
		}
	case "retention":
		for _, key := range []string{"auditLogDays", "mcpCallDays", "notificationDays", "webhookEventDays", "indexJobDays", "securityEventDays", "settingVersionDays"} {
			if days, ok := value[key].(float64); ok && (days < 0 || days > 3650 || days != float64(int(days))) {
				return fmt.Errorf("retention.%s must be a whole number from 0 (keep indefinitely) to 3650", key)
			}
		}
	case "notifications":
		if days, ok := value["apiKeyExpiryWarningDays"].(float64); ok && (days < 0 || days > 365 || days != float64(int(days))) {
			return errors.New("notifications.apiKeyExpiryWarningDays must be a whole number from 0 (disabled) to 365")
		}
		cfg, err := notificationDeliveryConfigFromMap(value, time.Now().UTC())
		if err != nil {
			return err
		}
		return outboundnotification.ValidateConfig(cfg)
	}
	return nil
}
func validatePublicAssetURL(value string) error {
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("must be a root-relative path or absolute HTTPS URL")
	}
	return nil
}
func (a *App) publicUIConfig(w http.ResponseWriter, r *http.Request) {
	// This response carries the running version. A proxy that cached it would
	// keep reporting the previous release long after an upgrade.
	w.Header().Set("Cache-Control", "no-store")
	bootstrapRequired, bootstrapPath := a.bootstrapStatus()
	var ssoConfigured int
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM system_settings WHERE category='keycloak'`).Scan(&ssoConfigured)
	out := map[string]any{
		"serviceName": "git-ctx", "tagline": "사내 개발 지식 MCP",
		"logoUrl": "/logo.svg", "faviconUrl": "/favicon.svg", "notice": "",
		"version":            version.Version,
		"build":              version.Full(),
		"bootstrapRequired":  bootstrapRequired,
		"bootstrapTokenFile": bootstrapPath,
		"ssoConfigured":      ssoConfigured > 0,
	}
	if settings, err := a.loadSettingMap(r.Context(), "ui"); err == nil {
		for _, key := range []string{"serviceName", "tagline", "logoUrl", "faviconUrl", "notice"} {
			if value, ok := settings[key].(string); ok && strings.TrimSpace(value) != "" {
				out[key] = value
			}
		}
	}
	jsonOut(w, http.StatusOK, out)
}
func (a *App) pollingInterval(ctx context.Context) time.Duration {
	settings, err := a.loadSettingMap(ctx, "index")
	if err != nil {
		return 30 * time.Minute
	}
	if minutes, ok := settings["pollingMinutes"].(float64); ok && minutes >= 1 {
		return time.Duration(minutes * float64(time.Minute))
	}
	return 30 * time.Minute
}

func (a *App) retentionPolicy(ctx context.Context) scheduler.RetentionPolicy {
	settings, err := a.loadSettingMap(ctx, "retention")
	if err != nil {
		settings = map[string]any{}
	}
	days := func(key string, fallback int) int {
		if value, ok := settings[key].(float64); ok {
			return int(value)
		}
		return fallback
	}
	return scheduler.RetentionPolicy{
		AuditLogDays:       days("auditLogDays", 365),
		MCPCallDays:        days("mcpCallDays", 90),
		NotificationDays:   days("notificationDays", 90),
		WebhookEventDays:   days("webhookEventDays", 30),
		IndexJobDays:       days("indexJobDays", 30),
		SecurityEventDays:  days("securityEventDays", 180),
		SettingVersionDays: days("settingVersionDays", 365),
	}
}

func settingCategories() map[string]bool {
	return map[string]bool{
		"keycloak": true, "bitbucket": true, "gitlab": true, "confluence": true, "jira": true, "mcp": true,
		"search": true, "model": true, "opensearch": true, "index": true,
		"security": true, "notifications": true, "logging": true,
		"operations": true, "ui": true,
		"observability": true, "backup": true, "retention": true, "vault": true, "vector": true,
	}
}
func maskSecrets(value map[string]any) []string {
	masked := []string{}
	maskSecretPaths(value, "", &masked)
	return masked
}
func maskSecretPaths(value map[string]any, prefix string, masked *[]string) {
	for key, item := range value {
		if reference, ok := item.(string); ok && strings.HasPrefix(reference, "secret://") {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		lower := strings.ToLower(key)
		if lower == "dsn" {
			if item != nil && item != "" {
				value[key] = "********"
				*masked = append(*masked, path)
			}
			continue
		}
		if strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "token") || strings.Contains(lower, "apikey") ||
			strings.Contains(lower, "api-key") || strings.Contains(lower, "authorization") ||
			strings.HasSuffix(lower, "pat") {
			if item != nil && item != "" {
				value[key] = "********"
				*masked = append(*masked, path)
			}
			continue
		}
		if nested, ok := item.(map[string]any); ok {
			maskSecretPaths(nested, path, masked)
		}
	}
}

// preserveMasked substitutes the stored value for every field the client sent
// back as the mask, and reports the ones it could not resolve.
//
// An unresolved mask is a real hazard: the target has no stored value, so
// keeping the literal "********" would configure a token, password or DSN whose
// value is eight asterisks. Those keys are dropped instead and returned, so the
// caller can say which secrets still have to be entered.
func preserveMasked(previous, incoming map[string]any) []string {
	unresolved := []string{}
	preserveMaskedPaths(previous, incoming, "", &unresolved)
	sort.Strings(unresolved)
	return unresolved
}

func preserveMaskedPaths(previous, incoming map[string]any, prefix string, unresolved *[]string) {
	for key, value := range incoming {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if value == "********" {
			if old, ok := previous[key]; ok {
				incoming[key] = old
				continue
			}
			delete(incoming, key)
			*unresolved = append(*unresolved, path)
			continue
		}
		next, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if old, ok := previous[key].(map[string]any); ok {
			preserveMaskedPaths(old, next, path, unresolved)
		} else {
			preserveMaskedPaths(map[string]any{}, next, path, unresolved)
		}
	}
}

// settingVersion returns one stored version with its secrets masked, so an
// administrator can read what a previous configuration actually contained
// before deciding to restore it. Storing versions without a way to read them
// made the history a list of dates and nothing else.
func (a *App) settingVersion(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, http.StatusNotFound, "not_found", "Setting category not found")
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		problem(w, http.StatusBadRequest, "invalid_request", "version must be a positive integer")
		return
	}
	value, changedBy, changedAt, err := a.settingVersionValue(r.Context(), category, version)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, http.StatusNotFound, "not_found", "Setting version not found")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	current, currentVersion := map[string]any{}, 0
	if loaded, err := a.loadSettingMapRaw(r.Context(), category); err == nil {
		current = loaded
		_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), category).Scan(&currentVersion)
	}
	// The difference is computed before masking, on the real values, and only the
	// keys are reported for secrets: "the token changed" is what the reader needs,
	// not the token.
	changes := settingDifference(current, value)
	maskSecrets(value)
	maskSecrets(current)
	jsonOut(w, http.StatusOK, map[string]any{
		"category": category, "version": version, "changedBy": changedBy, "changedAt": changedAt,
		"value": value, "currentVersion": currentVersion, "changes": changes,
	})
}

func (a *App) settingVersionValue(ctx context.Context, category string, version int) (map[string]any, string, time.Time, error) {
	var sealed []byte
	var changedBy string
	var changedAt time.Time
	err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT value_encrypted,changed_by,created_at FROM setting_versions WHERE category=? AND version=?`), category, version).
		Scan(&sealed, &changedBy, &changedAt)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	raw, err := a.open(sealed)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	value := map[string]any{}
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, "", time.Time{}, err
	}
	return value, changedBy, changedAt, nil
}

// settingDifference describes what restoring a version would change. Values are
// rendered short and secrets are reported as changed without their content.
func settingDifference(current, target map[string]any) []map[string]any {
	changes := []map[string]any{}
	keys := map[string]bool{}
	for key := range current {
		keys[key] = true
	}
	for key := range target {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		before, after := current[key], target[key]
		if fmt.Sprint(before) == fmt.Sprint(after) {
			continue
		}
		change := map[string]any{"field": key}
		if secretField(key) {
			change["before"], change["after"] = "(비밀값)", "(비밀값)"
			change["secret"] = true
		} else {
			change["before"], change["after"] = truncateText(fmt.Sprint(before), 120), truncateText(fmt.Sprint(after), 120)
		}
		switch {
		case before == nil:
			change["kind"] = "added"
		case after == nil:
			change["kind"] = "removed"
		default:
			change["kind"] = "changed"
		}
		changes = append(changes, change)
	}
	return changes
}

func secretField(key string) bool {
	lower := strings.ToLower(key)
	return lower == "dsn" || strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "api-key") || strings.Contains(lower, "authorization") || strings.HasSuffix(lower, "pat")
}

// restoreSettingVersion writes an old configuration back as a new version. The
// history stays append-only, so a restore can itself be undone, and the restored
// value goes through the same validation as a normal save unless the caller
// explicitly forces it.
func (a *App) restoreSettingVersion(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, http.StatusNotFound, "not_found", "Setting category not found")
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		problem(w, http.StatusBadRequest, "invalid_request", "version must be a positive integer")
		return
	}
	value, _, _, err := a.settingVersionValue(r.Context(), category, version)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, http.StatusNotFound, "not_found", "Setting version not found")
		return
	}
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// The stored value is complete and unmasked, so it is replayed through the
	// normal save path: one place decides validation, encryption and versioning.
	raw, err := json.Marshal(value)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	replay := r.Clone(r.Context())
	replay.Method = http.MethodPut
	replay.Body = io.NopCloser(bytes.NewReader(raw))
	replay.ContentLength = int64(len(raw))
	replay.Header.Set("Content-Type", "application/json")
	query := replay.URL.Query()
	query.Set("restoredFrom", strconv.Itoa(version))
	replay.URL.RawQuery = query.Encode()
	replay.SetPathValue("category", category)
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "settings.restore", category, fmt.Sprintf("v%d", version), "success", map[string]any{"restoredFrom": version})
	a.putSetting(w, replay)
}

// exportSettings writes the whole configuration as one document so a verified
// environment can be reproduced elsewhere. Secrets are never included: an
// export is a file that travels by mail and USB stick in an air-gapped install,
// and a credential in it would outlive the reason it was exported. Every secret
// field is written as the mask, which the import side treats as "keep whatever
// the target already has".
func (a *App) exportSettings(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	categories := make([]string, 0, len(settingCategories()))
	for category := range settingCategories() {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	out := map[string]any{}
	exported := 0
	for _, category := range categories {
		value, err := a.loadSettingMapRaw(r.Context(), category)
		if err != nil {
			continue
		}
		var version int
		_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), category).Scan(&version)
		masked := maskSecrets(value)
		out[category] = map[string]any{"value": value, "version": version, "secretFields": masked}
		exported++
	}
	a.audit(r, p, "settings.export", "settings", "all", "success", map[string]any{"categories": exported})
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"git-ctx-settings-%s.json\"", time.Now().UTC().Format("20060102")))
	jsonOut(w, http.StatusOK, map[string]any{
		"exportedAt": time.Now().UTC(), "platformVersion": version.Version, "secretsIncluded": false,
		"note":       "비밀값은 ******** 로 표기되어 있습니다. 가져오는 환경에 이미 저장된 비밀값은 그대로 유지되고, 없으면 가져온 뒤 직접 입력해야 합니다.",
		"categories": out,
	})
}

// importSettings applies an exported document. It defaults to a dry run,
// because the interesting question is not "did it import" but "what would
// change", and answering that before touching a running platform is the whole
// point of having the diff.
func (a *App) importSettings(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var document struct {
		Categories map[string]struct {
			Value map[string]any `json:"value"`
		} `json:"categories"`
	}
	if decode(r, &document) != nil || len(document.Categories) == 0 {
		problem(w, http.StatusBadRequest, "invalid_request", "내보내기 파일 형식이 아닙니다. categories 객체가 필요합니다.")
		return
	}
	apply := r.URL.Query().Get("apply") == "true"
	force := r.URL.Query().Get("force") == "true"
	names := make([]string, 0, len(document.Categories))
	for category := range document.Categories {
		names = append(names, category)
	}
	sort.Strings(names)
	results := make([]map[string]any, 0, len(names))
	applied := 0
	for _, category := range names {
		item := map[string]any{"category": category}
		if !settingCategories()[category] {
			item["status"], item["detail"] = "skipped", "이 버전이 모르는 설정 영역입니다."
			results = append(results, item)
			continue
		}
		if !a.settingCategoryAllowed(p, category) {
			item["status"], item["detail"] = "forbidden", "이 설정 영역을 변경할 권한이 없습니다."
			results = append(results, item)
			continue
		}
		value := map[string]any{}
		for key, raw := range document.Categories[category].Value {
			value[key] = raw
		}
		current, _ := a.loadSettingMapRaw(r.Context(), category)
		if current == nil {
			current = map[string]any{}
		}
		// The mask means "keep the target's secret", which is what makes an export
		// without credentials usable at all.
		missing := preserveMasked(current, value)
		if len(missing) > 0 {
			item["missingSecrets"] = missing
		}
		changes := settingDifference(current, value)
		item["changes"] = changes
		if len(changes) == 0 {
			item["status"], item["detail"] = "unchanged", "현재 값과 동일합니다."
			results = append(results, item)
			continue
		}
		normalized := map[string]any{}
		for key, raw := range value {
			normalized[key] = raw
		}
		if err := a.normalizeSetting(r.Context(), category, normalized); err != nil {
			item["status"], item["detail"] = "invalid", err.Error()
			results = append(results, item)
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		validationErr := a.validateSetting(ctx, category, normalized)
		cancel()
		if validationErr != nil && !force {
			item["status"], item["detail"] = "invalid", validationErr.Error()
			results = append(results, item)
			continue
		}
		if validationErr != nil {
			item["validationSkipped"] = validationErr.Error()
		}
		if !apply {
			item["status"], item["detail"] = "ready", fmt.Sprintf("%d개 항목이 바뀝니다.", len(changes))
			results = append(results, item)
			continue
		}
		if err := a.saveSettingValue(r.Context(), p, category, normalized); err != nil {
			item["status"], item["detail"] = "failed", err.Error()
			results = append(results, item)
			continue
		}
		applied++
		item["status"], item["detail"] = "applied", fmt.Sprintf("%d개 항목을 적용했습니다.", len(changes))
		results = append(results, item)
	}
	if apply {
		a.audit(r, p, "settings.import", "settings", "all", "success", map[string]any{"applied": applied, "categories": len(names)})
	}
	jsonOut(w, http.StatusOK, map[string]any{"dryRun": !apply, "applied": applied, "results": results})
}

// settingCategoryAllowed mirrors the per-category delegation used by the
// settings routes, so an import cannot become a way around it.
func (a *App) settingCategoryAllowed(p auth.Principal, category string) bool {
	if p.HasRole("platform-admin") {
		return true
	}
	for _, role := range settingCategoryRoles()[category] {
		if p.HasRole(role) {
			return true
		}
	}
	return false
}

// saveSettingValue persists one already normalized and validated category. It
// exists so the import path stores settings exactly the way the HTTP handler
// does: same encryption, same append-only version history.
func (a *App) saveSettingValue(ctx context.Context, p auth.Principal, category string, value map[string]any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	sealed, err := a.seal(raw)
	if err != nil {
		return err
	}
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var live, history int
	if err = tx.QueryRowContext(ctx, a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), category).Scan(&live); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = tx.QueryRowContext(ctx, a.store.Rebind(`SELECT COALESCE(MAX(version),0) FROM setting_versions WHERE category=?`), category).Scan(&history); err != nil {
		return err
	}
	next := max(live, history) + 1
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO setting_versions(category,version,value_encrypted,changed_by,reason) VALUES(?,?,?,?,?)`), category, next, sealed, p.UserID, "import"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO system_settings(category,version,value_encrypted,updated_by,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(category) DO UPDATE SET version=excluded.version,value_encrypted=excluded.value_encrypted,updated_by=excluded.updated_by,updated_at=excluded.updated_at`),
		category, next, sealed, p.UserID, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// settingVersions lists the change history metadata of a setting category.
// Stored values stay encrypted and are never returned.
func (a *App) settingVersions(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, http.StatusNotFound, "not_found", "Setting category not found")
		return
	}
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT version,changed_by,created_at FROM setting_versions WHERE category=? ORDER BY version DESC LIMIT 50`), category)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var version int
		var changedBy string
		var createdAt time.Time
		if err = rows.Scan(&version, &changedBy, &createdAt); err != nil {
			problem(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		out = append(out, map[string]any{"version": version, "changedBy": changedBy, "changedAt": createdAt})
	}
	jsonOut(w, http.StatusOK, out)
}

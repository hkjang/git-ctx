package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"git-ctx/internal/apikey"
	"git-ctx/internal/auth"
	"git-ctx/internal/backup"
	bitbucketv6 "git-ctx/internal/bitbucket/v6"
	"git-ctx/internal/config"
	"git-ctx/internal/embedding"
	gitlabsource "git-ctx/internal/gitlab"
	"git-ctx/internal/indexer"
	"git-ctx/internal/mcp"
	"git-ctx/internal/observability"
	"git-ctx/internal/opensearch"
	"git-ctx/internal/quality"
	"git-ctx/internal/rerank"
	"git-ctx/internal/scheduler"
	"git-ctx/internal/search"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/version"
	"git-ctx/internal/webhook"
	"git-ctx/internal/worker"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2"
)

type App struct {
	cfg                config.Config
	store              *store.Store
	keys               *apikey.Service
	mcp                *mcp.Server
	search             *search.Service
	mux                *http.ServeMux
	aead               cipher.AEAD
	oidc               *auth.OIDCVerifier
	hooks              *webhook.Service
	traces             *observability.Manager
	backup             *backup.Service
	quality            *quality.Service
	rootCtx            context.Context
	requestGate        sync.RWMutex
	backgroundMu       sync.Mutex
	bootstrapMu        sync.RWMutex
	bootstrapPath      string
	bootstrapPersisted bool
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
}

func New(ctx context.Context, c config.Config) (*App, error) {
	s, err := store.Open(ctx, c.DatabaseDriver, c.DatabaseDSN)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher([]byte(c.MasterKey))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	bootstrapPath, bootstrapPersisted := "", false
	if c.BootstrapAdmin == "" {
		if c.BackupDirectory == "" {
			c.BackupDirectory = "backups"
		}
		c.BootstrapAdmin, bootstrapPersisted, err = loadOrCreateBootstrapToken(ctx, s, aead)
		if err != nil {
			s.DB.Close()
			return nil, err
		}
		if c.BootstrapAdmin != "" {
			bootstrapPath = filepath.Join(c.BackupDirectory, "bootstrap-admin.token")
			if err = os.MkdirAll(c.BackupDirectory, 0700); err == nil {
				err = os.WriteFile(bootstrapPath, []byte(c.BootstrapAdmin+"\n"), 0600)
			}
			if err != nil {
				s.DB.Close()
				return nil, fmt.Errorf("write one-time bootstrap token: %w", err)
			}
			slog.Warn("Keycloak is not configured; one-time bootstrap token is available", "token_file", bootstrapPath)
		}
	}
	a := &App{cfg: c, store: s, keys: apikey.New(s, c.KeyPepper), aead: aead, mux: http.NewServeMux(), traces: observability.New(), rootCtx: ctx, bootstrapPath: bootstrapPath, bootstrapPersisted: bootstrapPersisted}
	a.backup = backup.New(s, aead, a.backupConfig)
	if settings, loadErr := a.loadSettingMap(ctx, "observability"); loadErr == nil {
		if applyErr := a.traces.Apply(ctx, observabilityConfigFromMap(settings)); applyErr != nil {
			slog.Warn("stored observability setting could not be applied", "error", applyErr)
		}
	}
	a.oidc = auth.NewOIDCVerifier(a.loadOIDCConfig)
	a.hooks = webhook.New(s)
	a.search = search.New(s)
	a.search.SetConfigLoader(a.searchConfig)
	a.search.SetEmbeddingLoader(func(ctx context.Context) embedding.Provider {
		provider, err := a.embeddingProvider(ctx)
		if err != nil {
			return embedding.Local{}
		}
		return provider
	})
	a.search.SetRerankerLoader(func(ctx context.Context) rerank.Provider {
		provider, err := a.rerankerProvider(ctx)
		if err != nil {
			return nil
		}
		return provider
	})
	a.search.SetSourceLoader(a.sourceAdapter)
	a.search.SetKeywordLoader(a.openSearchCandidates)
	a.quality = quality.New(s, a.search)
	a.mcp = mcp.New(a.search, s)
	a.routes()
	a.startBackground()
	return a, nil
}
func (a *App) startBackground() {
	a.backgroundMu.Lock()
	defer a.backgroundMu.Unlock()
	workerCtx, cancel := context.WithCancel(a.rootCtx)
	a.cancel = cancel
	backgroundWorker := worker.New(a.store, indexer.New(a.store, indexer.DefaultPolicy()), a.sourceAdapter)
	backgroundWorker.SetEmbeddingFactory(a.embeddingProvider)
	backgroundWorker.SetProjection(a.projectOpenSearch)
	backgroundScheduler := scheduler.New(a.store, a.pollingInterval)
	a.wg.Add(3)
	go func() {
		defer a.wg.Done()
		backgroundWorker.Run(workerCtx)
	}()
	go func() {
		defer a.wg.Done()
		backgroundScheduler.Run(workerCtx)
	}()
	go func() {
		defer a.wg.Done()
		a.backup.Run(workerCtx)
	}()
}
func (a *App) stopBackground() {
	a.backgroundMu.Lock()
	cancel := a.cancel
	a.cancel = nil
	a.backgroundMu.Unlock()
	if cancel != nil {
		cancel()
		a.wg.Wait()
	}
}
func (a *App) Close() {
	a.stopBackground()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.traces.Shutdown(shutdownCtx)
	_ = a.store.DB.Close()
}
func (a *App) Handler() http.Handler { return tracing(requestLogging(a.gate(securityHeaders(a.mux)))) }
func (a *App) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/admin/backups/") && strings.HasSuffix(r.URL.Path, "/restore") {
			next.ServeHTTP(w, r)
			return
		}
		a.requestGate.RLock()
		defer a.requestGate.RUnlock()
		next.ServeHTTP(w, r)
	})
}
func (a *App) routes() {
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	a.mux.HandleFunc("GET /readyz", a.readiness)
	a.mux.HandleFunc("GET /metrics", a.metrics)
	a.mux.HandleFunc("GET /api/v1/public/config", a.publicUIConfig)
	a.mux.HandleFunc("POST /api/v1/bootstrap/login", a.bootstrapLogin)
	a.mux.HandleFunc("GET /auth/login", a.login)
	a.mux.HandleFunc("GET /auth/callback", a.callback)
	a.mux.HandleFunc("POST /auth/logout", a.logout)
	a.mux.Handle("/mcp", a.mcpAccess(a.authenticate(http.HandlerFunc(a.mcp.ServeHTTP))))
	a.mux.HandleFunc("POST /webhooks/bitbucket", a.receiveWebhook)
	a.mux.HandleFunc("POST /webhooks/gitlab", a.receiveWebhook)
	a.mux.Handle("GET /api/v1/me", a.authenticate(http.HandlerFunc(a.me)))
	a.mux.Handle("GET /api/v1/me/repositories", a.authenticate(http.HandlerFunc(a.meRepositories)))
	a.mux.Handle("GET /api/v1/me/api-keys", a.authenticate(http.HandlerFunc(a.listKeys)))
	a.mux.Handle("POST /api/v1/me/api-keys", a.authenticate(http.HandlerFunc(a.createKey)))
	a.mux.Handle("POST /api/v1/me/api-keys/{id}/disable", a.authenticate(http.HandlerFunc(a.disableKey)))
	a.mux.Handle("POST /api/v1/me/api-keys/{id}/enable", a.authenticate(http.HandlerFunc(a.enableKey)))
	a.mux.Handle("POST /api/v1/me/api-keys/{id}/rotate", a.authenticate(http.HandlerFunc(a.rotateKey)))
	a.mux.Handle("DELETE /api/v1/me/api-keys/{id}", a.authenticate(http.HandlerFunc(a.revokeKey)))
	a.mux.Handle("GET /api/v1/me/usage", a.authenticate(http.HandlerFunc(a.meUsage)))
	a.mux.Handle("GET /api/v1/me/calls", a.authenticate(http.HandlerFunc(a.meCalls)))
	a.mux.Handle("GET /api/v1/me/notifications", a.authenticate(http.HandlerFunc(a.meNotifications)))
	a.mux.Handle("POST /api/v1/me/notifications/{id}/read", a.authenticate(http.HandlerFunc(a.readNotification)))
	a.mux.Handle("POST /api/v1/tools/resolve/test", a.authenticate(http.HandlerFunc(a.testResolve)))
	a.mux.Handle("POST /api/v1/tools/query/test", a.authenticate(http.HandlerFunc(a.testQuery)))
	a.mux.Handle("GET /api/v1/admin/settings", a.admin(http.HandlerFunc(a.listSettings)))
	a.mux.Handle("GET /api/v1/admin/settings/{category}", a.settingsAuthorize(http.HandlerFunc(a.getSetting)))
	a.mux.Handle("PUT /api/v1/admin/settings/{category}", a.settingsAuthorize(http.HandlerFunc(a.putSetting)))
	a.mux.Handle("POST /api/v1/admin/settings/{category}/rollback", a.settingsAuthorize(http.HandlerFunc(a.rollbackSetting)))
	a.mux.Handle("POST /api/v1/admin/settings/{category}/test", a.settingsAuthorize(http.HandlerFunc(a.testIntegrationSetting)))
	a.mux.Handle("POST /api/v1/admin/settings/keycloak/preview", a.settingsAuthorize(http.HandlerFunc(a.previewKeycloak)))
	a.mux.Handle("GET /api/v1/admin/audit-logs", a.authorize(http.HandlerFunc(a.auditLogs), "auditor", "security-admin"))
	a.mux.Handle("GET /api/v1/admin/api-keys", a.authorize(http.HandlerFunc(a.adminAPIKeys), "security-admin"))
	a.mux.Handle("POST /api/v1/admin/api-keys/{id}/revoke", a.authorize(http.HandlerFunc(a.adminRevokeKey), "security-admin"))
	a.mux.Handle("GET /api/v1/admin/security-events", a.authorize(http.HandlerFunc(a.securityEvents), "security-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/sources/{source}/discover", a.authorize(http.HandlerFunc(a.discoverSource), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/repositories", a.authorize(http.HandlerFunc(a.adminRepositories), "source-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/repositories", a.authorize(http.HandlerFunc(a.registerRepository), "source-admin"))
	a.mux.Handle("POST /api/v1/admin/repositories/{id}/index", a.authorize(http.HandlerFunc(a.enqueueIndex), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/repositories/{id}/refs", a.authorize(http.HandlerFunc(a.repositoryRefs), "source-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/repositories/{id}/policy", a.authorize(http.HandlerFunc(a.getRepositoryPolicy), "source-admin", "readonly-operator"))
	a.mux.Handle("PUT /api/v1/admin/repositories/{id}/policy", a.authorize(http.HandlerFunc(a.putRepositoryPolicy), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/index-jobs", a.authorize(http.HandlerFunc(a.indexJobs), "source-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/index-jobs/{id}/retry", a.authorize(http.HandlerFunc(a.retryIndexJob), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/health", a.authorize(http.HandlerFunc(a.adminHealth), "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/backups", a.authorize(http.HandlerFunc(a.listBackups), "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/backups", a.admin(http.HandlerFunc(a.createBackup)))
	a.mux.Handle("GET /api/v1/admin/backups/{id}/download", a.authorize(http.HandlerFunc(a.downloadBackup), "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/backups/{id}/restore", a.admin(http.HandlerFunc(a.restoreBackup)))
	a.mux.Handle("GET /api/v1/admin/quality/cases", a.authorize(http.HandlerFunc(a.listQualityCases), "search-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/quality/cases", a.authorize(http.HandlerFunc(a.createQualityCase), "search-admin"))
	a.mux.Handle("DELETE /api/v1/admin/quality/cases/{id}", a.authorize(http.HandlerFunc(a.deleteQualityCase), "search-admin"))
	a.mux.Handle("GET /api/v1/admin/quality/runs", a.authorize(http.HandlerFunc(a.listQualityRuns), "search-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/quality/runs", a.authorize(http.HandlerFunc(a.runQualityBenchmark), "search-admin"))
	a.mux.Handle("GET /api/v1/admin/quality/runs/{id}/results", a.authorize(http.HandlerFunc(a.qualityResults), "search-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/mcp/tools", a.authorize(http.HandlerFunc(a.mcpTools), "mcp-admin", "readonly-operator"))
	a.mux.Handle("PUT /api/v1/admin/mcp/tools/{id}", a.authorize(http.HandlerFunc(a.updateMCPTool), "mcp-admin"))
	a.mux.Handle("/", http.FileServer(http.Dir("web")))
}

func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("CONTEXT7_API_KEY")
		if raw == "" {
			raw = r.Header.Get("X-API-Key")
		}
		if raw != "" {
			info, err := a.keys.AuthenticateRequest(r.Context(), raw, a.requestIP(r))
			if err != nil {
				var limited *apikey.RateLimitError
				if errors.As(err, &limited) {
					w.Header().Set("Retry-After", fmt.Sprint(max(1, int(limited.RetryAfter.Seconds()))))
					problem(w, http.StatusTooManyRequests, "rate_limit_exceeded", "API key rate limit exceeded")
					return
				}
				problem(w, http.StatusUnauthorized, "invalid_token", "API key is invalid, expired, disabled, or revoked")
				return
			}
			uid, kid, prefix := info.UserID, info.KeyID, info.Prefix
			var subject, username, aclPrincipal, aclGroupText string
			if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT u.subject,u.username,COALESCE(i.bitbucket_user_slug,''),COALESCE(i.bitbucket_groups,'') FROM users u LEFT JOIN user_identities i ON i.user_id=u.id WHERE u.id=? AND u.status='active'`), uid).Scan(&subject, &username, &aclPrincipal, &aclGroupText); err != nil {
				problem(w, 401, "invalid_token", "User is inactive")
				return
			}
			roles, _ := a.userRoles(r.Context(), uid)
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: uid, Subject: subject, Username: username, ACLPrincipal: aclPrincipal, ACLPrincipals: aclPrincipals(aclPrincipal, splitCSV(aclGroupText)), Roles: roles, KeyID: kid, KeyPrefix: prefix, Scopes: info.Scopes, AllowedRepositories: info.Restrictions.AllowedRepositories})))
			return
		}
		if cookie, err := r.Cookie("git_ctx_session"); err == nil {
			if principal, ok := a.sessionPrincipal(r.Context(), cookie.Value); ok {
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
				return
			}
		}
		// Bootstrap authentication is intentionally limited to initial on-prem setup.
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "" && a.validBootstrapToken(r.Context(), token) {
			id := "bootstrap-admin"
			_, _ = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO users(id,subject,username,email) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`), id, id, id, "")
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: id, Subject: id, Username: id, Roles: []string{"platform-admin"}})))
			return
		}
		if token != "" {
			identity, err := a.oidc.Verify(r.Context(), token)
			if err != nil {
				problem(w, http.StatusUnauthorized, "invalid_token", "Keycloak access token validation failed")
				return
			}
			userID, err := a.upsertIdentity(r.Context(), identity)
			if err != nil {
				problem(w, http.StatusInternalServerError, "identity_sync_failed", "Unable to synchronize authenticated identity")
				return
			}
			aclPrincipal := identity.BitbucketUserSlug
			if aclPrincipal == "" && identity.GitLabUserID != "" {
				aclPrincipal = "gitlab:" + identity.GitLabUserID
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{
				UserID: userID, Subject: identity.Subject, Username: identity.Username,
				ACLPrincipal: aclPrincipal, ACLPrincipals: aclPrincipals(aclPrincipal, identity.ACLGroups), Roles: identity.Roles, Groups: identity.Groups,
			})))
			return
		}
		problem(w, http.StatusUnauthorized, "authentication_required", "Use a Keycloak bearer token or MCP API key")
	})
}

func (a *App) bootstrapLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		requestOrigin, err := url.Parse(origin)
		if err != nil || (requestOrigin.Scheme != "http" && requestOrigin.Scheme != "https") || !strings.EqualFold(requestOrigin.Host, r.Host) {
			problem(w, http.StatusForbidden, "origin_not_allowed", "Bootstrap login Origin is not allowed")
			return
		}
	}
	var in struct {
		Token string `json:"token"`
	}
	if decode(r, &in) != nil || !a.validBootstrapToken(r.Context(), strings.TrimSpace(in.Token)) {
		a.audit(r, auth.Principal{UserID: "anonymous"}, "bootstrap.login", "session", "bootstrap", "failure", nil)
		problem(w, http.StatusUnauthorized, "invalid_bootstrap_token", "Bootstrap token is invalid or has been revoked")
		return
	}
	const id = "bootstrap-admin"
	if _, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO users(id,subject,username,email) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`), id, id, id, ""); err != nil {
		problem(w, http.StatusInternalServerError, "bootstrap_login_failed", "Unable to create bootstrap identity")
		return
	}
	rawSession, err := randomToken(32)
	if err != nil {
		problem(w, http.StatusInternalServerError, "bootstrap_login_failed", "Unable to create bootstrap session")
		return
	}
	expires := time.Now().UTC().Add(30 * time.Minute)
	if _, err = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(?,?,?,?)`), sessionHash(rawSession), id, expires, time.Now().UTC()); err != nil {
		problem(w, http.StatusInternalServerError, "bootstrap_login_failed", "Unable to persist bootstrap session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "git_ctx_session", Value: rawSession, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(a.publicURL(r.Context()), "https://"), SameSite: http.SameSiteStrictMode, Expires: expires})
	a.audit(r, auth.Principal{UserID: id}, "bootstrap.login", "session", "bootstrap", "success", nil)
	jsonOut(w, http.StatusOK, map[string]any{"status": "authenticated", "expiresAt": expires})
}
func (a *App) mcpAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.isTrustedProxy(r.Context(), remoteIP(r.RemoteAddr)) {
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Real-IP")
		}
		origin := r.Header.Get("Origin")
		settings, _ := a.loadSettingMap(r.Context(), "mcp")
		var allowed []string
		if raw, ok := settings["allowedOrigins"].([]any); ok {
			for _, item := range raw {
				if value, ok := item.(string); ok {
					allowed = append(allowed, strings.TrimSuffix(value, "/"))
				}
			}
		}
		if len(allowed) == 0 {
			if parsed, err := url.Parse(a.publicURL(r.Context())); err == nil && parsed.Scheme != "" && parsed.Host != "" {
				allowed = []string{parsed.Scheme + "://" + parsed.Host}
			}
		}
		if origin != "" {
			ok := false
			for _, item := range allowed {
				if strings.EqualFold(strings.TrimSuffix(origin, "/"), item) {
					ok = true
					break
				}
			}
			if !ok {
				problem(w, http.StatusForbidden, "origin_not_allowed", "MCP request Origin is not allowed")
				return
			}
		}
		maxBytes := int64(1 << 20)
		if value, ok := settings["maxRequestBytes"].(float64); ok && value >= 1024 && value <= 16<<20 {
			maxBytes = int64(value)
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.loadOIDCConfig(r.Context())
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "sso_not_configured", "Keycloak login is not configured")
		return
	}
	oauthCfg, err := auth.OAuthConfig(r.Context(), cfg)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "sso_unavailable", err.Error())
		return
	}
	state, err := randomToken(32)
	if err != nil {
		problem(w, 500, "internal_error", "Unable to start login")
		return
	}
	verifier := oauth2.GenerateVerifier()
	returnTo := r.URL.Query().Get("return_to")
	if !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		returnTo = "/"
	}
	_, err = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO auth_flows(state,code_verifier,return_to,expires_at) VALUES(?,?,?,?)`), state, verifier, returnTo, time.Now().UTC().Add(10*time.Minute))
	if err != nil {
		problem(w, 500, "internal_error", "Unable to persist login state")
		return
	}
	http.Redirect(w, r, oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}
func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" || r.URL.Query().Get("error") != "" {
		problem(w, 400, "login_failed", "Keycloak login was denied or incomplete")
		return
	}
	var verifier, returnTo string
	var expires time.Time
	err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT code_verifier,return_to,expires_at FROM auth_flows WHERE state=?`), state).Scan(&verifier, &returnTo, &expires)
	_, _ = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM auth_flows WHERE state=?`), state)
	if err != nil || time.Now().After(expires) {
		problem(w, 400, "invalid_login_state", "Login state is invalid or expired")
		return
	}
	cfg, err := a.loadOIDCConfig(r.Context())
	if err != nil {
		problem(w, 503, "sso_unavailable", "Keycloak setting is unavailable")
		return
	}
	oauthCfg, err := auth.OAuthConfig(r.Context(), cfg)
	if err != nil {
		problem(w, 503, "sso_unavailable", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := oauthCfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		problem(w, 401, "token_exchange_failed", "Keycloak code exchange failed")
		return
	}
	rawIDToken, _ := token.Extra("id_token").(string)
	if rawIDToken == "" {
		problem(w, 401, "id_token_missing", "Keycloak did not return an ID token")
		return
	}
	identity, err := a.oidc.Verify(ctx, rawIDToken)
	if err != nil {
		problem(w, 401, "invalid_id_token", "Keycloak ID token validation failed")
		return
	}
	userID, err := a.upsertIdentity(ctx, identity)
	if err != nil {
		problem(w, 500, "identity_sync_failed", "Unable to synchronize authenticated identity")
		return
	}
	rawSession, err := randomToken(32)
	if err != nil {
		problem(w, 500, "internal_error", "Unable to create session")
		return
	}
	sessionExpiry := token.Expiry
	if sessionExpiry.IsZero() || sessionExpiry.After(time.Now().Add(8*time.Hour)) {
		sessionExpiry = time.Now().Add(8 * time.Hour)
	}
	_, err = a.store.DB.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(?,?,?,?)`), sessionHash(rawSession), userID, sessionExpiry.UTC(), time.Now().UTC())
	if err != nil {
		problem(w, 500, "internal_error", "Unable to persist session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "git_ctx_session", Value: rawSession, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(a.publicURL(r.Context()), "https://"), SameSite: http.SameSiteLaxMode, Expires: sessionExpiry})
	a.audit(r, auth.Principal{UserID: userID}, "login", "session", "browser", "success", nil)
	http.Redirect(w, r, returnTo, http.StatusFound)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("git_ctx_session"); err == nil {
		_, _ = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM user_sessions WHERE id_hash=?`), sessionHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "git_ctx_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(a.publicURL(r.Context()), "https://"), SameSite: http.SameSiteLaxMode})
	if cfg, err := a.loadOIDCConfig(r.Context()); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if logoutURL, err := auth.EndSessionURL(ctx, cfg); err == nil {
			jsonOut(w, 200, map[string]string{"logoutUrl": logoutURL})
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) sessionPrincipal(ctx context.Context, raw string) (auth.Principal, bool) {
	if raw == "" {
		return auth.Principal{}, false
	}
	var userID, subject, username, bitbucketSlug, gitlabID, bitbucketGroups string
	var expires time.Time
	err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT s.user_id,u.subject,u.username,COALESCE(i.bitbucket_user_slug,''),COALESCE(i.gitlab_user_id,''),COALESCE(i.bitbucket_groups,''),s.expires_at FROM user_sessions s JOIN users u ON u.id=s.user_id LEFT JOIN user_identities i ON i.user_id=u.id WHERE s.id_hash=? AND u.status='active'`), sessionHash(raw)).Scan(&userID, &subject, &username, &bitbucketSlug, &gitlabID, &bitbucketGroups, &expires)
	if err != nil || time.Now().After(expires) {
		return auth.Principal{}, false
	}
	acl := bitbucketSlug
	if acl == "" && gitlabID != "" {
		acl = "gitlab:" + gitlabID
	}
	roles, err := a.userRoles(ctx, userID)
	if err != nil {
		return auth.Principal{}, false
	}
	if userID == "bootstrap-admin" {
		if !a.bootstrapAvailable(ctx) {
			return auth.Principal{}, false
		}
		roles = []string{"platform-admin"}
	}
	_, _ = a.store.DB.ExecContext(ctx, a.store.Rebind(`UPDATE user_sessions SET last_seen_at=? WHERE id_hash=?`), time.Now().UTC(), sessionHash(raw))
	return auth.Principal{UserID: userID, Subject: subject, Username: username, ACLPrincipal: acl, ACLPrincipals: aclPrincipals(acl, splitCSV(bitbucketGroups)), Roles: roles}, true
}
func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func sessionHash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
func (a *App) requestIP(r *http.Request) string {
	remote := remoteIP(r.RemoteAddr)
	if a.isTrustedProxy(r.Context(), remote) {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			if parsed, err := netip.ParseAddr(strings.Trim(forwarded, "[]")); err == nil {
				return parsed.String()
			}
		}
	}
	return remote
}
func remoteIP(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(address, "[]")
}
func (a *App) isTrustedProxy(ctx context.Context, address string) bool {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	settings, err := a.loadSettingMap(ctx, "security")
	if err != nil {
		return false
	}
	raw, ok := settings["trustedProxyCidrs"].([]any)
	if !ok {
		return false
	}
	for _, item := range raw {
		cidr, ok := item.(string)
		if !ok {
			continue
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err == nil && prefix.Contains(ip) {
			return true
		}
	}
	return false
}
func (a *App) admin(next http.Handler) http.Handler {
	return a.authorize(next)
}
func (a *App) authorize(next http.Handler, roles ...string) http.Handler {
	return a.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		if !roleAllowed(p, roles...) {
			problem(w, 403, "forbidden", "Administrator role required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
func (a *App) settingsAuthorize(next http.Handler) http.Handler {
	return a.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		category := r.PathValue("category")
		if category == "" {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/settings/"), "/")
			if len(parts) > 0 {
				category = parts[0]
			}
		}
		if !settingRoleAllowed(p, category) {
			problem(w, 403, "forbidden", "Required setting administrator role is missing")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
func roleAllowed(p auth.Principal, roles ...string) bool {
	if p.HasRole("platform-admin") {
		return true
	}
	for _, role := range roles {
		if p.HasRole(role) {
			return true
		}
	}
	return false
}
func settingRoleAllowed(p auth.Principal, category string) bool {
	required := map[string][]string{
		"bitbucket": {"source-admin"}, "gitlab": {"source-admin"}, "index": {"source-admin"},
		"mcp": {"mcp-admin"}, "search": {"search-admin"}, "model": {"search-admin"}, "opensearch": {"search-admin"},
		"security": {"security-admin"}, "permissions": {"security-admin"},
	}
	return roleAllowed(p, required[category]...)
}
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	jsonOut(w, 200, struct {
		auth.Principal
		Version string `json:"Version"`
	}{Principal: p, Version: version.Version})
}
func (a *App) meRepositories(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	principals := aclPrincipals(p.ACLPrincipal, p.ACLPrincipals)
	if len(principals) == 0 {
		jsonOut(w, 200, []any{})
		return
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	args := make([]any, len(principals))
	for n := range principals {
		args[n] = principals[n]
	}
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT DISTINCT r.library_id,r.name,r.description,r.default_branch,r.reputation,r.indexed_at FROM repositories r JOIN repository_permissions p ON p.repository_id=r.id WHERE r.enabled=1 AND (p.principal IN (`+placeholders+`) OR p.principal='*') ORDER BY r.library_id`), args...)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, name, description, branch, reputation string
		var indexed sql.NullTime
		if err := rows.Scan(&id, &name, &description, &branch, &reputation, &indexed); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		if p.KeyID != "" && !repositoryAllowed(id, p.AllowedRepositories) {
			continue
		}
		item := map[string]any{"libraryId": id, "name": name, "description": description, "defaultBranch": branch, "reputation": reputation}
		if indexed.Valid {
			item["indexedAt"] = indexed.Time
		}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func repositoryAllowed(id string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(id, item) {
			return true
		}
	}
	return false
}
func (a *App) listKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	v, e := a.keys.List(r.Context(), p.UserID)
	if e != nil {
		problem(w, 500, "internal_error", "Unable to list keys")
		return
	}
	jsonOut(w, 200, v)
}
func (a *App) createKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Name         string              `json:"name"`
		Scopes       []string            `json:"scopes"`
		ExpiresAt    *time.Time          `json:"expiresAt"`
		Restrictions apikey.Restrictions `json:"restrictions"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "Invalid JSON")
		return
	}
	k, plain, e := a.keys.CreateWithRestrictions(r.Context(), p.UserID, in.Name, in.Scopes, in.ExpiresAt, in.Restrictions)
	if e != nil {
		problem(w, 400, "invalid_request", e.Error())
		return
	}
	a.audit(r, p, "api_key.create", "api_key", k.ID, "success", map[string]any{"prefix": k.Prefix})
	jsonOut(w, 201, map[string]any{"key": k, "secret": plain, "notice": "This value is shown once and cannot be recovered."})
}
func (a *App) disableKey(w http.ResponseWriter, r *http.Request) { a.keyStatus(w, r, "disabled") }
func (a *App) enableKey(w http.ResponseWriter, r *http.Request)  { a.keyStatus(w, r, "enabled") }
func (a *App) revokeKey(w http.ResponseWriter, r *http.Request)  { a.keyStatus(w, r, "revoked") }
func (a *App) rotateKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		OverlapMinutes int `json:"overlapMinutes"`
	}
	if r.ContentLength > 0 && decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "Invalid rotation request")
		return
	}
	if in.OverlapMinutes < 0 || in.OverlapMinutes > 1440 {
		problem(w, 400, "invalid_overlap", "overlapMinutes must be between 0 and 1440")
		return
	}
	k, secret, err := a.keys.Rotate(r.Context(), p.UserID, r.PathValue("id"), time.Duration(in.OverlapMinutes)*time.Minute)
	if err != nil {
		problem(w, 400, "rotation_failed", err.Error())
		return
	}
	a.audit(r, p, "api_key.rotate", "api_key", r.PathValue("id"), "success", map[string]any{"replacementId": k.ID, "overlapMinutes": in.OverlapMinutes})
	jsonOut(w, 201, map[string]any{"key": k, "secret": secret, "notice": "This replacement key is shown once and cannot be recovered."})
}
func (a *App) keyStatus(w http.ResponseWriter, r *http.Request, status string) {
	p, _ := auth.FromContext(r.Context())
	if e := a.keys.SetStatus(r.Context(), p.UserID, r.PathValue("id"), status); e != nil {
		problem(w, 404, "not_found", "Key not found")
		return
	}
	a.audit(r, p, "api_key."+status, "api_key", r.PathValue("id"), "success", nil)
	w.WriteHeader(204)
}
func (a *App) meUsage(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT tool,outcome,COUNT(*),COALESCE(AVG(duration_ms),0),COALESCE(MAX(duration_ms),0) FROM mcp_calls WHERE user_id=? GROUP BY tool,outcome ORDER BY tool,outcome`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var tool, outcome string
		var count int64
		var avg, maxLatency float64
		if err := rows.Scan(&tool, &outcome, &count, &avg, &maxLatency); err != nil {
			return
		}
		out = append(out, map[string]any{"tool": tool, "outcome": outcome, "calls": count, "averageLatencyMs": avg, "maximumLatencyMs": maxLatency})
	}
	jsonOut(w, 200, out)
}
func (a *App) meCalls(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT occurred_at,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip FROM mcp_calls WHERE user_id=? ORDER BY occurred_at DESC LIMIT 200`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var at time.Time
		var prefix, tool, libraryID, outcome, ip string
		var duration int64
		if err := rows.Scan(&at, &prefix, &tool, &libraryID, &outcome, &duration, &ip); err != nil {
			return
		}
		out = append(out, map[string]any{"occurredAt": at, "apiKeyPrefix": prefix, "tool": tool, "libraryId": libraryID, "outcome": outcome, "durationMs": duration, "clientIp": ip})
	}
	jsonOut(w, 200, out)
}
func (a *App) meNotifications(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT id,notification_type,title,message,read_at,created_at FROM notifications WHERE user_id=? ORDER BY created_at DESC LIMIT 200`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, kind, title, message string
		var read sql.NullTime
		var created time.Time
		if rows.Scan(&id, &kind, &title, &message, &read, &created) != nil {
			continue
		}
		item := map[string]any{"id": id, "type": kind, "title": title, "message": message, "createdAt": created, "read": read.Valid}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func (a *App) readNotification(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE notifications SET read_at=? WHERE id=? AND user_id=?`), time.Now().UTC(), r.PathValue("id"), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		problem(w, 404, "not_found", "Notification not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
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
	items, err := a.search.Resolve(r.Context(), aclPrincipals(p.ACLPrincipal, p.ACLPrincipals), in.LibraryName, in.Query)
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
	text, err := a.search.Query(r.Context(), aclPrincipals(p.ACLPrincipal, p.ACLPrincipals), in.LibraryID, in.Query)
	if err != nil {
		problem(w, 400, "search_failed", err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
}
func baseLibraryID(id string) string {
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(id), "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return "/" + parts[0] + "/" + parts[1]
}
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
	if previous, err := a.loadSettingMap(r.Context(), category); err == nil {
		preserveMasked(previous, value)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.validateSetting(ctx, category, value); err != nil {
		problem(w, 400, "setting_validation_failed", err.Error())
		return
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
	var version int
	e = tx.QueryRowContext(r.Context(), a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), category).Scan(&version)
	if errors.Is(e, sql.ErrNoRows) {
		version = 0
	} else if e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	version++
	_, e = tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO setting_versions(category,version,value_encrypted,changed_by,reason) VALUES(?,?,?,?,?)`), category, version, sealed, p.UserID, r.Header.Get("X-Change-Reason"))
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
	if category == "keycloak" {
		a.disableBootstrapAdmin()
	}
	a.audit(r, p, "settings.update", category, category, "success", map[string]any{"version": version})
	jsonOut(w, 200, map[string]any{"category": category, "version": version, "secretFields": "encrypted and masked"})
}
func (a *App) testIntegrationSetting(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	category := r.PathValue("category")
	allowed := map[string]bool{"keycloak": true, "bitbucket": true, "gitlab": true, "model": true, "opensearch": true, "observability": true, "backup": true}
	if !allowed[category] {
		problem(w, 400, "setting_test_unsupported", "This setting category has no external or storage connection test")
		return
	}
	var value map[string]any
	if decode(r, &value) != nil {
		problem(w, 400, "invalid_request", "Invalid JSON")
		return
	}
	if previous, err := a.loadSettingMap(r.Context(), category); err == nil {
		preserveMasked(previous, value)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.validateSetting(ctx, category, value); err != nil {
		a.audit(r, p, "settings.test", category, category, "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 400, "setting_connection_test_failed", err.Error())
		return
	}
	a.audit(r, p, "settings.test", category, category, "success", nil)
	jsonOut(w, http.StatusOK, map[string]any{"category": category, "status": "verified", "testedAt": time.Now().UTC()})
}
func (a *App) rollbackSetting(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, 404, "not_found", "Setting category not found")
		return
	}
	var in struct {
		TargetVersion int    `json:"targetVersion"`
		Reason        string `json:"reason"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "targetVersion and reason are required")
		return
	}
	var current int
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), category).Scan(&current); err != nil {
		problem(w, 404, "not_found", "Setting category not configured")
		return
	}
	if in.TargetVersion == 0 {
		in.TargetVersion = current - 1
	}
	if in.TargetVersion < 1 || in.TargetVersion >= current || strings.TrimSpace(in.Reason) == "" {
		problem(w, 400, "invalid_rollback", "A prior targetVersion and rollback reason are required")
		return
	}
	var sealed []byte
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT value_encrypted FROM setting_versions WHERE category=? AND version=?`), category, in.TargetVersion).Scan(&sealed); err != nil {
		problem(w, 404, "version_not_found", "Target setting version not found")
		return
	}
	raw, err := a.open(sealed)
	if err != nil {
		problem(w, 500, "decrypt_failed", "Unable to decrypt target version")
		return
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		problem(w, 500, "invalid_stored_setting", "Target setting is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := a.validateSetting(ctx, category, value); err != nil {
		problem(w, 400, "rollback_validation_failed", err.Error())
		return
	}
	newVersion := current + 1
	tx, err := a.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer tx.Rollback()
	reason := fmt.Sprintf("rollback to v%d: %s", in.TargetVersion, in.Reason)
	if _, err = tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO setting_versions(category,version,value_encrypted,changed_by,reason) VALUES(?,?,?,?,?)`), category, newVersion, sealed, p.UserID, reason); err == nil {
		_, err = tx.ExecContext(r.Context(), a.store.Rebind(`UPDATE system_settings SET version=?,value_encrypted=?,updated_by=?,updated_at=? WHERE category=?`), newVersion, sealed, p.UserID, time.Now().UTC(), category)
	}
	if err != nil {
		problem(w, 500, "rollback_failed", err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		problem(w, 500, "rollback_failed", err.Error())
		return
	}
	if category == "observability" {
		if err = a.traces.Apply(r.Context(), observabilityConfigFromMap(value)); err != nil {
			problem(w, 500, "setting_apply_failed", "Rolled back observability setting was saved but could not be applied")
			return
		}
	}
	a.audit(r, p, "settings.rollback", category, category, "success", map[string]any{"fromVersion": current, "targetVersion": in.TargetVersion, "newVersion": newVersion})
	jsonOut(w, 200, map[string]any{"category": category, "version": newVersion, "restoredFrom": in.TargetVersion})
}
func (a *App) getSetting(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("category")
	if !settingCategories()[category] {
		problem(w, 404, "not_found", "Setting category not found")
		return
	}
	var sealed []byte
	var version int
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT version,value_encrypted FROM system_settings WHERE category=?`), category).Scan(&version, &sealed); err != nil {
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
	maskSecrets(value)
	jsonOut(w, 200, map[string]any{"category": category, "version": version, "value": value})
}
func (a *App) previewKeycloak(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Config auth.OIDCConfig `json:"config"`
		Token  string          `json:"token"`
	}
	if decode(r, &in) != nil || in.Token == "" {
		problem(w, 400, "invalid_request", "Candidate config and a short-lived test token are required")
		return
	}
	verifier := auth.NewOIDCVerifier(func(context.Context) (auth.OIDCConfig, error) { return in.Config, nil })
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	identity, err := verifier.Verify(ctx, in.Token)
	if err != nil {
		problem(w, 400, "claim_preview_failed", err.Error())
		return
	}
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "keycloak.claim_preview", "keycloak", "candidate", "success", nil)
	jsonOut(w, 200, map[string]any{"subject": identity.Subject, "username": identity.Username, "email": identity.Email, "groups": identity.Groups, "roles": identity.Roles, "bitbucketUserSlug": identity.BitbucketUserSlug, "gitlabUserId": identity.GitLabUserID})
}
func (a *App) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	sourceType := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	settings, err := a.loadSettingMap(r.Context(), sourceType)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "webhook_not_configured", "Webhook receiver is not configured")
		return
	}
	secret, _ := settings["webhookSecret"].(string)
	if secret == "" {
		problem(w, http.StatusServiceUnavailable, "webhook_not_configured", "Webhook secret is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid_payload", "Webhook payload is too large or unreadable")
		return
	}
	eventID, eventType := "", ""
	valid := false
	if sourceType == "bitbucket" {
		eventID = r.Header.Get("X-Request-Id")
		eventType = r.Header.Get("X-Event-Key")
		signature := strings.TrimPrefix(r.Header.Get("X-Hub-Signature"), "sha256=")
		expected := hmac.New(sha256.New, []byte(secret))
		expected.Write(payload)
		actual, decodeErr := hex.DecodeString(signature)
		valid = decodeErr == nil && hmac.Equal(expected.Sum(nil), actual)
	} else {
		eventID = r.Header.Get("X-Gitlab-Event-UUID")
		eventType = r.Header.Get("X-Gitlab-Event")
		valid = hmac.Equal([]byte(r.Header.Get("X-Gitlab-Token")), []byte(secret))
	}
	if !valid {
		problem(w, http.StatusUnauthorized, "invalid_webhook_signature", "Webhook authentication failed")
		return
	}
	result, err := a.hooks.Enqueue(r.Context(), sourceType, eventID, eventType, payload)
	if err != nil {
		problem(w, http.StatusNotFound, "webhook_rejected", err.Error())
		return
	}
	status := http.StatusAccepted
	if result.Duplicate {
		status = http.StatusOK
	}
	jsonOut(w, status, result)
}
func (a *App) loadSettingMap(ctx context.Context, category string) (map[string]any, error) {
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
func (a *App) sourceAdapter(ctx context.Context, sourceType string) (source.RepositorySource, error) {
	settings, err := a.loadSettingMap(ctx, sourceType)
	if err != nil {
		return nil, fmt.Errorf("%s setting is unavailable: %w", sourceType, err)
	}
	return sourceAdapterFromMap(sourceType, settings)
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
		username, _ := settings["username"].(string)
		password, _ := settings["password"].(string)
		return bitbucketv6.New(bitbucketv6.Config{BaseURL: baseURL, APIPrefix: apiPrefix, Token: token, Username: username, Password: password, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: caCertificate, ProxyURL: proxyURL})
	case "gitlab":
		return gitlabsource.New(gitlabsource.Config{BaseURL: baseURL, Token: token, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: caCertificate, ProxyURL: proxyURL})
	default:
		return nil, errors.New("unsupported source type")
	}
}
func (a *App) validateSetting(ctx context.Context, category string, value map[string]any) error {
	switch category {
	case "keycloak":
		raw, _ := json.Marshal(value)
		var cfg auth.OIDCConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		return auth.ValidateOIDCConfig(ctx, cfg)
	case "bitbucket", "gitlab":
		adapter, err := sourceAdapterFromMap(category, value)
		if err != nil {
			return err
		}
		_, err = adapter.ListProjects(ctx)
		if err != nil {
			return fmt.Errorf("%s connection test: %w", category, err)
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
	case "search":
		number := func(key string, fallback float64) float64 {
			if x, ok := value[key].(float64); ok {
				return x
			}
			return fallback
		}
		keyword, vector := number("keywordWeight", 1), number("vectorWeight", .35)
		finalK, candidates, rerankLimit := number("finalK", 8), number("candidateLimit", 5000), number("rerankLimit", 30)
		if keyword < 0 || vector < 0 || keyword+vector == 0 {
			return errors.New("search weights must be non-negative and not both zero")
		}
		if finalK < 1 || finalK > 50 || candidates < 10 || candidates > 20000 {
			return errors.New("search finalK must be 1..50 and candidateLimit 10..20000")
		}
		if rerankLimit < 1 || rerankLimit > 100 {
			return errors.New("search rerankLimit must be 1..100")
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
	case "observability":
		return observability.Validate(ctx, observabilityConfigFromMap(value))
	case "backup":
		return backup.ValidateStorage(backupConfigFromMap(value, a.cfg.BackupDirectory))
	}
	return nil
}
func (a *App) backupConfig(ctx context.Context) backup.Config {
	settings, err := a.loadSettingMap(ctx, "backup")
	if err != nil {
		settings = map[string]any{}
	}
	return backupConfigFromMap(settings, a.cfg.BackupDirectory)
}
func backupConfigFromMap(settings map[string]any, defaultDirectory string) backup.Config {
	cfg := backup.Config{Directory: defaultDirectory, Interval: 24 * time.Hour, RetentionCount: 7, MaxBytes: 512 << 20}
	cfg.Enabled, _ = settings["enabled"].(bool)
	if value, ok := settings["directory"].(string); ok && value != "" {
		cfg.Directory = value
	}
	if value, ok := settings["intervalHours"].(float64); ok && value > 0 {
		cfg.Interval = time.Duration(value * float64(time.Hour))
	}
	if value, ok := settings["retentionCount"].(float64); ok {
		cfg.RetentionCount = int(value)
	}
	if value, ok := settings["maxBytes"].(float64); ok {
		cfg.MaxBytes = int64(value)
	}
	return cfg
}
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
	bootstrapRequired, bootstrapPath := a.bootstrapStatus()
	out := map[string]any{
		"serviceName": "git-ctx", "tagline": "사내 개발 지식 MCP",
		"logoUrl": "/logo.svg", "faviconUrl": "/favicon.svg", "notice": "",
		"version":            version.Version,
		"bootstrapRequired":  bootstrapRequired,
		"bootstrapTokenFile": bootstrapPath,
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
func (a *App) bootstrapAdminToken() string {
	a.bootstrapMu.RLock()
	defer a.bootstrapMu.RUnlock()
	return a.cfg.BootstrapAdmin
}
func (a *App) validBootstrapToken(ctx context.Context, candidate string) bool {
	a.bootstrapMu.RLock()
	expected, persisted := a.cfg.BootstrapAdmin, a.bootstrapPersisted
	a.bootstrapMu.RUnlock()
	if expected == "" || !hmac.Equal([]byte(candidate), []byte(expected)) {
		return false
	}
	if !persisted {
		return true
	}
	var count int
	return a.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM platform_bootstrap WHERE id='initial-admin'`).Scan(&count) == nil && count == 1
}
func (a *App) bootstrapAvailable(ctx context.Context) bool {
	a.bootstrapMu.RLock()
	expected, persisted := a.cfg.BootstrapAdmin, a.bootstrapPersisted
	a.bootstrapMu.RUnlock()
	if expected == "" {
		return false
	}
	if !persisted {
		return true
	}
	var count int
	return a.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM platform_bootstrap WHERE id='initial-admin'`).Scan(&count) == nil && count == 1
}
func loadOrCreateBootstrapToken(ctx context.Context, s *store.Store, aead cipher.AEAD) (string, bool, error) {
	var keycloakConfigured int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM system_settings WHERE category='keycloak'`).Scan(&keycloakConfigured); err != nil || keycloakConfigured > 0 {
		return "", false, err
	}
	decrypt := func(sealed []byte) (string, error) {
		if len(sealed) < aead.NonceSize() {
			return "", errors.New("stored bootstrap token is truncated")
		}
		raw, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("git-ctx/bootstrap/v1"))
		return string(raw), err
	}
	var sealed []byte
	err := s.DB.QueryRowContext(ctx, `SELECT token_encrypted FROM platform_bootstrap WHERE id='initial-admin'`).Scan(&sealed)
	if err == nil {
		token, openErr := decrypt(sealed)
		return token, true, openErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", false, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", false, err
	}
	sealed = aead.Seal(nonce, nonce, []byte(token), []byte("git-ctx/bootstrap/v1"))
	result, err := s.DB.ExecContext(ctx, s.Rebind(`INSERT INTO platform_bootstrap(id,token_encrypted) VALUES('initial-admin',?) ON CONFLICT(id) DO NOTHING`), sealed)
	if err != nil {
		return "", false, err
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return token, true, nil
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT token_encrypted FROM platform_bootstrap WHERE id='initial-admin'`).Scan(&sealed); err != nil {
		return "", false, err
	}
	token, err = decrypt(sealed)
	return token, true, err
}
func (a *App) bootstrapStatus() (bool, string) {
	a.bootstrapMu.RLock()
	defer a.bootstrapMu.RUnlock()
	required := a.cfg.BootstrapAdmin != ""
	if required && a.bootstrapPersisted {
		var count int
		if err := a.store.DB.QueryRow(`SELECT COUNT(*) FROM platform_bootstrap WHERE id='initial-admin'`).Scan(&count); err != nil || count == 0 {
			required = false
		}
	}
	return required, a.bootstrapPath
}
func (a *App) disableBootstrapAdmin() {
	a.bootstrapMu.Lock()
	a.cfg.BootstrapAdmin = ""
	path := a.bootstrapPath
	a.bootstrapPath = ""
	a.bootstrapMu.Unlock()
	_, _ = a.store.DB.Exec(`DELETE FROM platform_bootstrap WHERE id='initial-admin'`)
	_, _ = a.store.DB.Exec(a.store.Rebind(`DELETE FROM user_sessions WHERE user_id=?`), "bootstrap-admin")
	_, _ = a.store.DB.Exec(a.store.Rebind(`UPDATE api_keys SET revoked_at=CURRENT_TIMESTAMP WHERE user_id=? AND revoked_at IS NULL`), "bootstrap-admin")
	if path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("one-time bootstrap token file could not be removed", "path", path, "error", err)
		}
	}
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
func (a *App) searchConfig(ctx context.Context) search.Config {
	cfg := search.Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 8, CandidateLimit: 5000}
	modelSettings, modelErr := a.loadSettingMap(ctx, "model")
	provider, _ := modelSettings["provider"].(string)
	noRemoteModel := modelErr != nil || provider == "" || provider == "local"
	settings, err := a.loadSettingMap(ctx, "search")
	if err != nil {
		if noRemoteModel {
			cfg.VectorWeight = 0
			cfg.SourceQuerySearch = true
		}
		return cfg
	}
	if value, ok := settings["keywordWeight"].(float64); ok {
		cfg.KeywordWeight = value
	}
	if value, ok := settings["vectorWeight"].(float64); ok {
		cfg.VectorWeight = value
	}
	if value, ok := settings["finalK"].(float64); ok {
		cfg.FinalK = int(value)
	}
	if value, ok := settings["candidateLimit"].(float64); ok {
		cfg.CandidateLimit = int(value)
	}
	if value, ok := settings["rerankLimit"].(float64); ok {
		cfg.RerankLimit = int(value)
	}
	if noRemoteModel {
		cfg.VectorWeight = 0
		cfg.SourceQuerySearch = true
	}
	return cfg
}
func (a *App) rerankerProvider(ctx context.Context) (rerank.Provider, error) {
	settings, err := a.loadSettingMap(ctx, "model")
	if err != nil {
		return nil, nil
	}
	return rerankerProviderFromMap(settings)
}
func rerankerProviderFromMap(settings map[string]any) (rerank.Provider, error) {
	enabled, _ := settings["rerankerEnabled"].(bool)
	if !enabled {
		return nil, nil
	}
	provider, _ := settings["rerankerProvider"].(string)
	if provider != "openai-compatible" {
		return nil, errors.New("model.rerankerProvider must be openai-compatible when enabled")
	}
	baseURL, _ := settings["rerankerBaseUrl"].(string)
	model, _ := settings["rerankerModel"].(string)
	apiKey, _ := settings["rerankerApiKey"].(string)
	timeout := 15 * time.Second
	if seconds, ok := settings["rerankerTimeoutSeconds"].(float64); ok && seconds > 0 {
		timeout = time.Duration(seconds * float64(time.Second))
	}
	var tlsVerify *bool
	if value, ok := settings["tlsVerify"].(bool); ok {
		tlsVerify = &value
	}
	ca, _ := settings["caCertificate"].(string)
	proxy, _ := settings["proxyUrl"].(string)
	return rerank.NewOpenAI(rerank.OpenAIConfig{BaseURL: baseURL, Model: model, APIKey: apiKey, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: ca, ProxyURL: proxy})
}
func (a *App) embeddingProvider(ctx context.Context) (embedding.Provider, error) {
	settings, err := a.loadSettingMap(ctx, "model")
	if err != nil {
		return embedding.Local{}, nil
	}
	return embeddingProviderFromMap(settings)
}

func openSearchConfigFromMap(settings map[string]any) (opensearch.Config, error) {
	cfg := opensearch.Config{Index: "git-ctx-chunks", Timeout: 30 * time.Second}
	cfg.Enabled, _ = settings["enabled"].(bool)
	cfg.BaseURL, _ = settings["baseUrl"].(string)
	if value, ok := settings["index"].(string); ok && value != "" {
		cfg.Index = value
	}
	cfg.Username, _ = settings["username"].(string)
	cfg.Password, _ = settings["password"].(string)
	cfg.APIKey, _ = settings["apiKey"].(string)
	if seconds, ok := settings["timeoutSeconds"].(float64); ok && seconds > 0 {
		cfg.Timeout = time.Duration(seconds * float64(time.Second))
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	cfg.CACertificate, _ = settings["caCertificate"].(string)
	cfg.ProxyURL, _ = settings["proxyUrl"].(string)
	if cfg.Enabled && strings.TrimSpace(cfg.BaseURL) == "" {
		return cfg, errors.New("opensearch.baseUrl is required when enabled")
	}
	return cfg, nil
}

func (a *App) openSearchClient(ctx context.Context) (*opensearch.Client, bool, error) {
	settings, err := a.loadSettingMap(ctx, "opensearch")
	if err != nil {
		return nil, false, nil
	}
	cfg, err := openSearchConfigFromMap(settings)
	if err != nil || !cfg.Enabled {
		return nil, cfg.Enabled, err
	}
	client, err := opensearch.New(cfg)
	return client, true, err
}

func (a *App) projectOpenSearch(ctx context.Context, repositoryID, ref string) error {
	client, enabled, err := a.openSearchClient(ctx)
	if err != nil || !enabled {
		return err
	}
	return client.SyncRef(ctx, a.store, repositoryID, ref)
}

func (a *App) openSearchCandidates(ctx context.Context, repositoryID, ref string, principals []string, query string, limit int) ([]search.KeywordCandidate, error) {
	client, enabled, err := a.openSearchClient(ctx)
	if err != nil || !enabled {
		return nil, err
	}
	candidates, err := client.Search(ctx, repositoryID, ref, principals, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]search.KeywordCandidate, len(candidates))
	for i, candidate := range candidates {
		out[i] = search.KeywordCandidate{ID: candidate.ID, Score: candidate.Score}
	}
	return out, nil
}
func embeddingProviderFromMap(settings map[string]any) (embedding.Provider, error) {
	provider, _ := settings["provider"].(string)
	if provider == "" || provider == "local" {
		return embedding.Local{}, nil
	}
	if provider != "openai-compatible" {
		return nil, errors.New("model.provider must be local or openai-compatible")
	}
	baseURL, _ := settings["baseUrl"].(string)
	model, _ := settings["model"].(string)
	apiKey, _ := settings["apiKey"].(string)
	timeout := 30 * time.Second
	if seconds, ok := settings["timeoutSeconds"].(float64); ok && seconds > 0 {
		timeout = time.Duration(seconds * float64(time.Second))
	}
	var tlsVerify *bool
	if value, ok := settings["tlsVerify"].(bool); ok {
		tlsVerify = &value
	}
	ca, _ := settings["caCertificate"].(string)
	proxy, _ := settings["proxyUrl"].(string)
	return embedding.NewOpenAI(embedding.OpenAIConfig{BaseURL: baseURL, Model: model, APIKey: apiKey, Timeout: timeout, TLSVerify: tlsVerify, CACertificate: ca, ProxyURL: proxy})
}
func settingCategories() map[string]bool {
	return map[string]bool{
		"keycloak": true, "bitbucket": true, "gitlab": true, "mcp": true,
		"search": true, "model": true, "opensearch": true, "index": true, "permissions": true,
		"security": true, "notifications": true, "logging": true,
		"operations": true, "ui": true,
		"observability": true, "backup": true, "retention": true,
	}
}
func maskSecrets(value map[string]any) {
	for key, item := range value {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "token") || strings.Contains(lower, "apikey") ||
			strings.Contains(lower, "api-key") || strings.Contains(lower, "authorization") ||
			strings.HasSuffix(lower, "pat") {
			if item != nil && item != "" {
				value[key] = "********"
			}
			continue
		}
		if nested, ok := item.(map[string]any); ok {
			maskSecrets(nested)
		}
	}
}
func preserveMasked(previous, incoming map[string]any) {
	for key, value := range incoming {
		if value == "********" {
			if old, ok := previous[key]; ok {
				incoming[key] = old
			}
			continue
		}
		next, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if old, ok := previous[key].(map[string]any); ok {
			preserveMasked(old, next)
		}
	}
}
func (a *App) auditLogs(w http.ResponseWriter, r *http.Request) {
	rows, e := a.store.DB.QueryContext(r.Context(), `SELECT occurred_at,actor_id,action,resource_type,resource_id,outcome,ip_address,metadata FROM audit_logs ORDER BY occurred_at DESC LIMIT 200`)
	if e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var t time.Time
		var actor, action, rt, rid, outcome, ip, metadata string
		_ = rows.Scan(&t, &actor, &action, &rt, &rid, &outcome, &ip, &metadata)
		out = append(out, map[string]any{"at": t, "actor": actor, "action": action, "resourceType": rt, "resourceId": rid, "outcome": outcome, "ip": ip})
	}
	jsonOut(w, 200, out)
}
func (a *App) adminAPIKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT k.id,u.username,k.name,k.prefix,k.scopes,k.expires_at,k.disabled_at,k.revoked_at,k.last_used_at,k.created_at FROM api_keys k JOIN users u ON u.id=k.user_id ORDER BY k.created_at DESC LIMIT 1000`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, username, name, prefix, scopes string
		var expires, disabled, revoked, used sql.NullTime
		var created time.Time
		if err := rows.Scan(&id, &username, &name, &prefix, &scopes, &expires, &disabled, &revoked, &used, &created); err != nil {
			return
		}
		status := "active"
		if disabled.Valid {
			status = "disabled"
		}
		if revoked.Valid {
			status = "revoked"
		}
		if expires.Valid && time.Now().After(expires.Time) {
			status = "expired"
		}
		item := map[string]any{"id": id, "username": username, "name": name, "prefix": prefix, "scopes": strings.Split(scopes, ","), "status": status, "createdAt": created}
		if expires.Valid {
			item["expiresAt"] = expires.Time
		}
		if used.Valid {
			item["lastUsedAt"] = used.Time
		}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func (a *App) adminRevokeKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`), time.Now().UTC(), r.PathValue("id"))
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		problem(w, 404, "not_found", "Active API key not found")
		return
	}
	a.audit(r, p, "api_key.admin_revoke", "api_key", r.PathValue("id"), "success", map[string]any{"reason": r.Header.Get("X-Revoke-Reason")})
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) securityEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT occurred_at,repository_id,ref_name,file_path,finding_type,action FROM index_security_events ORDER BY occurred_at DESC LIMIT 500`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var at time.Time
		var repo, ref, path, finding, action string
		if err := rows.Scan(&at, &repo, &ref, &path, &finding, &action); err != nil {
			return
		}
		out = append(out, map[string]any{"occurredAt": at, "repositoryId": repo, "refName": ref, "filePath": path, "findingType": finding, "action": action})
	}
	jsonOut(w, 200, out)
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
	if decode(r, &in) != nil || (in.SourceType != "bitbucket" && in.SourceType != "gitlab") || in.Repository.ID == 0 || in.Repository.ProjectKey == "" || in.Repository.Slug == "" {
		problem(w, 400, "invalid_request", "sourceType and a discovered repository are required")
		return
	}
	id := in.SourceType + ":" + fmt.Sprint(in.Repository.ID)
	libraryID := indexer.LibraryID(in.Repository.ProjectKey, in.Repository.Slug)
	settings, err := a.loadSettingMap(r.Context(), in.SourceType)
	if err != nil {
		problem(w, 400, "source_not_configured", "Source setting is required before repository registration")
		return
	}
	autoWebhook := true
	if configured, ok := settings["autoRegisterWebhook"].(bool); ok {
		autoWebhook = configured
	}
	var alreadyRegistered int
	_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM repositories WHERE id=?`), id).Scan(&alreadyRegistered)
	if autoWebhook && alreadyRegistered == 0 {
		secret, _ := settings["webhookSecret"].(string)
		if secret == "" {
			problem(w, 400, "webhook_secret_required", "webhookSecret is required when autoRegisterWebhook is enabled")
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
func (a *App) mcpTools(w http.ResponseWriter, r *http.Request) {
	catalog := mcp.Catalog()
	for _, tool := range catalog {
		name, _ := tool["name"].(string)
		var enabled, timeout, cache int
		if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT enabled,timeout_ms,cache_seconds FROM mcp_tools WHERE name=?`), name).Scan(&enabled, &timeout, &cache); err == nil {
			tool["enabled"] = enabled == 1
			tool["timeoutMs"] = timeout
			tool["cacheSeconds"] = cache
		}
	}
	jsonOut(w, 200, catalog)
}
func (a *App) updateMCPTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if name != "resolve-library-id" && name != "query-docs" {
		problem(w, 404, "not_found", "MCP tool not found")
		return
	}
	var in struct {
		Enabled      bool `json:"enabled"`
		TimeoutMS    int  `json:"timeoutMs"`
		CacheSeconds int  `json:"cacheSeconds"`
	}
	if decode(r, &in) != nil || in.TimeoutMS < 100 || in.TimeoutMS > 120000 || in.CacheSeconds < 0 || in.CacheSeconds > 86400 {
		problem(w, 400, "invalid_request", "timeoutMs must be 100..120000 and cacheSeconds 0..86400")
		return
	}
	p, _ := auth.FromContext(r.Context())
	_, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE mcp_tools SET enabled=?,timeout_ms=?,cache_seconds=?,updated_by=?,updated_at=? WHERE name=?`), boolInt(in.Enabled), in.TimeoutMS, in.CacheSeconds, p.UserID, time.Now().UTC(), name)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	a.audit(r, p, "mcp_tool.update", "mcp_tool", name, "success", map[string]any{"enabled": in.Enabled, "timeoutMs": in.TimeoutMS, "cacheSeconds": in.CacheSeconds})
	jsonOut(w, 200, map[string]any{"name": name, "enabled": in.Enabled, "timeoutMs": in.TimeoutMS, "cacheSeconds": in.CacheSeconds})
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func truncateText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
func stringContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
func aclPrincipals(primary string, groups []string) []string {
	var out []string
	if primary != "" {
		out = append(out, primary)
	}
	for _, group := range groups {
		if group != "" && !stringContains(out, group) {
			out = append(out, group)
		}
	}
	return out
}
func (a *App) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.DB.PingContext(ctx); err != nil {
		jsonOut(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "database": "unavailable"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"status": "ready", "database": "ok"})
}
func (a *App) adminHealth(w http.ResponseWriter, r *http.Request) {
	var repositories, chunks, pending, failed, activeKeys int64
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM repositories WHERE enabled=1`).Scan(&repositories)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM document_chunks`).Scan(&chunks)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM index_jobs WHERE status='pending'`).Scan(&pending)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM index_jobs WHERE status='failed'`).Scan(&failed)
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND disabled_at IS NULL AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP)`).Scan(&activeKeys)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	jsonOut(w, 200, map[string]any{"status": "ok", "version": version.Version, "database": "ok", "repositories": repositories, "chunks": chunks, "indexJobs": map[string]int64{"pending": pending, "failed": failed}, "activeApiKeys": activeKeys, "observability": map[string]bool{"tracingEnabled": a.traces.Enabled()}, "go": map[string]any{"goroutines": runtime.NumGoroutine(), "allocatedBytes": memory.Alloc}})
}
func (a *App) listBackups(w http.ResponseWriter, r *http.Request) {
	records, err := a.backup.List(r.Context())
	if err != nil {
		problem(w, 500, "backup_list_failed", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, records)
}
func (a *App) createBackup(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	record, err := a.backup.Create(r.Context(), p.UserID, "manual")
	if err != nil {
		a.audit(r, p, "backup.create", "backup", "", "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 500, "backup_create_failed", err.Error())
		return
	}
	a.audit(r, p, "backup.create", "backup", record.ID, "success", map[string]any{"sizeBytes": record.SizeBytes, "sha256": record.SHA256})
	jsonOut(w, http.StatusCreated, record)
}
func (a *App) downloadBackup(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	file, record, err := a.backup.Open(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "backup_unavailable", "Backup does not exist or failed integrity verification")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+record.Filename+`"`)
	w.Header().Set("X-Content-SHA256", record.SHA256)
	w.Header().Set("Cache-Control", "no-store")
	a.audit(r, p, "backup.download", "backup", record.ID, "success", map[string]any{"sizeBytes": record.SizeBytes})
	http.ServeContent(w, r, record.Filename, record.CreatedAt, file)
}
func (a *App) restoreBackup(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id := r.PathValue("id")
	reason := strings.TrimSpace(r.Header.Get("X-Change-Reason"))
	if r.Header.Get("X-Restore-Confirmation") != "RESTORE "+id || reason == "" {
		problem(w, 400, "restore_confirmation_required", "Exact restore confirmation and change reason are required")
		return
	}
	a.requestGate.Lock()
	a.stopBackground()
	err := a.backup.Restore(r.Context(), id)
	a.startBackground()
	a.requestGate.Unlock()
	if err != nil {
		a.audit(r, p, "backup.restore", "backup", id, "failure", map[string]any{"reason": reason, "error": truncateText(err.Error(), 500)})
		problem(w, 400, "backup_restore_failed", err.Error())
		return
	}
	a.audit(r, p, "backup.restore", "backup", id, "success", map[string]any{"reason": reason})
	jsonOut(w, http.StatusOK, map[string]any{"id": id, "status": "restored", "sessionsInvalidated": true})
}
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
func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	type metric struct{ name, help, query string }
	items := []metric{
		{"git_ctx_repositories", "Enabled source repositories", `SELECT COUNT(*) FROM repositories WHERE enabled=1`},
		{"git_ctx_document_chunks", "Indexed document chunks", `SELECT COUNT(*) FROM document_chunks`},
		{"git_ctx_api_keys_active", "Active MCP API keys", `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND disabled_at IS NULL AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP)`},
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
	fmt.Fprintf(w, "# HELP git_ctx_go_goroutines Current goroutines\n# TYPE git_ctx_go_goroutines gauge\ngit_ctx_go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP git_ctx_tracing_enabled OTLP tracing provider enabled\n# TYPE git_ctx_tracing_enabled gauge\ngit_ctx_tracing_enabled %d\n", boolInt(a.traces.Enabled()))
}
func (a *App) audit(r *http.Request, p auth.Principal, action, rt, rid, outcome string, metadata any) {
	raw, _ := json.Marshal(metadata)
	_, _ = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO audit_logs(id,actor_id,action,resource_type,resource_id,outcome,ip_address,metadata) VALUES(?,?,?,?,?,?,?,?)`), time.Now().Format("20060102150405.000000000"), p.UserID, action, rt, rid, outcome, r.RemoteAddr, string(raw))
}
func (a *App) seal(in []byte) ([]byte, error) {
	nonce := make([]byte, a.aead.NonceSize())
	if _, e := io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return a.aead.Seal(nonce, nonce, in, nil), nil
}
func (a *App) open(in []byte) ([]byte, error) {
	if len(in) < a.aead.NonceSize() {
		return nil, errors.New("encrypted setting is truncated")
	}
	nonce, ciphertext := in[:a.aead.NonceSize()], in[a.aead.NonceSize():]
	return a.aead.Open(nil, nonce, ciphertext, nil)
}
func (a *App) loadOIDCConfig(ctx context.Context) (auth.OIDCConfig, error) {
	var sealed []byte
	err := a.store.DB.QueryRowContext(ctx, `SELECT value_encrypted FROM system_settings WHERE category='keycloak'`).Scan(&sealed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return auth.OIDCConfig{}, errors.New("Keycloak is not configured")
		}
		return auth.OIDCConfig{}, err
	}
	raw, err := a.open(sealed)
	if err != nil {
		return auth.OIDCConfig{}, err
	}
	var cfg auth.OIDCConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return auth.OIDCConfig{}, err
	}
	return cfg, nil
}
func (a *App) upsertIdentity(ctx context.Context, identity auth.Identity) (string, error) {
	userID := identity.Subject
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO users(id,subject,username,email,status) VALUES(?,?,?,?,'active') ON CONFLICT(subject) DO UPDATE SET username=excluded.username,email=excluded.email,status='active'`), userID, identity.Subject, identity.Username, identity.Email)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_identities(user_id,bitbucket_user_slug,gitlab_user_id,bitbucket_groups,mapping_source,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET bitbucket_user_slug=excluded.bitbucket_user_slug,gitlab_user_id=excluded.gitlab_user_id,bitbucket_groups=excluded.bitbucket_groups,mapping_source=excluded.mapping_source,updated_at=excluded.updated_at`), userID, identity.BitbucketUserSlug, identity.GitLabUserID, strings.Join(identity.ACLGroups, ","), "keycloak-claims", time.Now().UTC())
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM user_roles WHERE user_id=?`), userID); err != nil {
		return "", err
	}
	for _, role := range identity.Roles {
		if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_roles(user_id,role_code) VALUES(?,?) ON CONFLICT(user_id,role_code) DO NOTHING`), userID, role); err != nil {
			return "", err
		}
	}
	return userID, tx.Commit()
}
func (a *App) userRoles(ctx context.Context, userID string) ([]string, error) {
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(`SELECT role_code FROM user_roles WHERE user_id=?`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}
func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code, detail string) {
	jsonOut(w, status, map[string]any{"type": "about:blank", "title": code, "status": status, "detail": detail})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}
func tracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("git-ctx/http").Start(ctx, r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.path", r.URL.Path)))
		defer span.End()
		if span.SpanContext().IsValid() {
			w.Header().Set("X-Trace-Id", span.SpanContext().TraceID().String())
		}
		wrapped := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r.WithContext(ctx))
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if r.Pattern != "" {
			span.SetName(r.Method + " " + r.Pattern)
			span.SetAttributes(attribute.String("http.route", r.Pattern))
		}
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}
func requestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if len(requestID) < 8 || len(requestID) > 128 {
			requestID, _ = randomToken(12)
		}
		w.Header().Set("X-Request-ID", requestID)
		started := time.Now()
		wrapped := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		slog.InfoContext(r.Context(), "http_request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "status", status, "bytes", wrapped.bytes, "duration_ms", time.Since(started).Milliseconds(), "remote_ip", remoteIP(r.RemoteAddr))
	})
}

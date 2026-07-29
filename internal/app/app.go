package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"git-ctx/internal/apikey"
	"git-ctx/internal/auth"
	"git-ctx/internal/backup"
	bitbucketv6 "git-ctx/internal/bitbucket/v6"
	"git-ctx/internal/calltrace"
	"git-ctx/internal/config"
	confluencesource "git-ctx/internal/confluence"
	"git-ctx/internal/embedding"
	gitlabsource "git-ctx/internal/gitlab"
	"git-ctx/internal/indexer"
	jirasource "git-ctx/internal/jira"
	runtimelogging "git-ctx/internal/logging"
	"git-ctx/internal/mcp"
	outboundnotification "git-ctx/internal/notification"
	"git-ctx/internal/observability"
	"git-ctx/internal/opensearch"
	"git-ctx/internal/quality"
	"git-ctx/internal/recovery"
	"git-ctx/internal/rerank"
	"git-ctx/internal/scheduler"
	"git-ctx/internal/search"
	secretstore "git-ctx/internal/secret"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/vectorstore"
	"git-ctx/internal/version"
	"git-ctx/internal/webhook"
	"git-ctx/internal/worker"
	webfs "git-ctx/web"
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
	secrets            *secretstore.Service
	notifier           *outboundnotification.Service
	rootCtx            context.Context
	requestGate        sync.RWMutex
	backgroundMu       sync.Mutex
	adapterMu          sync.RWMutex
	adapters           map[string]cachedAdapter
	breakers           *source.Breakers
	bootstrapMu        sync.RWMutex
	bootstrapPath      string
	bootstrapPersisted bool
	recoveryMode       bool
	databaseStartupErr string
	recoveryDatabase   string
	databaseRestart    atomic.Bool
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	credentialAttempts attemptLimiter
}

// attemptLimiter throttles the credential endpoints per client address. The
// bootstrap and recovery tokens are high entropy, but an unthrottled endpoint
// still lets an attacker probe continuously and floods the audit log.
type attemptLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

const (
	credentialAttemptLimit  = 10
	credentialAttemptWindow = 5 * time.Minute
)

// allow records an attempt and reports whether it stays within the window.
func (l *attemptLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = map[string][]time.Time{}
	}
	now := time.Now()
	kept := l.attempts[key][:0]
	for _, at := range l.attempts[key] {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	// Drop idle keys so a long running instance does not accumulate addresses.
	if len(kept) == 0 {
		delete(l.attempts, key)
	}
	if len(kept) >= limit {
		l.attempts[key] = kept
		return false
	}
	l.attempts[key] = append(kept, now)
	return true
}

func New(ctx context.Context, c config.Config) (*App, error) {
	block, err := aes.NewCipher([]byte(c.MasterKey))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if c.BackupDirectory == "" {
		c.BackupDirectory = "backups"
	}
	s, openErr := store.Open(ctx, c.DatabaseDriver, c.DatabaseDSN)
	recoveryMode, startupDBError, recoveryPath := false, "", ""
	if openErr != nil && c.DatabaseDriver == "postgres" {
		startupDBError = safeDatabaseError(openErr, c.DatabaseDSN)
		recoveryPath = filepath.Join(c.BackupDirectory, "recovery.db")
		if err = os.MkdirAll(c.BackupDirectory, 0700); err != nil {
			return nil, fmt.Errorf("create recovery database directory: %w", err)
		}
		recoveryDSN := "file:" + recoveryPath + "?_foreign_keys=on&_busy_timeout=5000"
		s, err = store.Open(ctx, "sqlite", recoveryDSN)
		if err != nil {
			return nil, fmt.Errorf("primary database unavailable (%s), recovery database failed: %w", safeDatabaseError(openErr, c.DatabaseDSN), err)
		}
		if err = os.Chmod(recoveryPath, 0600); err != nil {
			s.DB.Close()
			return nil, fmt.Errorf("protect recovery database: %w", err)
		}
		recoveryMode = true
		if desired, loadErr := configuredDatabaseDSN(ctx, s, aead); loadErr == nil && desired != "" && desired != c.DatabaseDSN {
			if target, targetErr := store.Open(ctx, store.DriverForDSN(desired), desired); targetErr == nil {
				s.DB.Close()
				s = target
				c.DatabaseDSN, c.DatabaseDriver = desired, store.DriverForDSN(desired)
				recoveryMode, startupDBError = false, ""
			} else {
				startupDBError = safeDatabaseError(targetErr, desired)
			}
		}
		if recoveryMode {
			slog.Warn("primary PostgreSQL is unavailable; SQLite recovery mode is active", "recovery_database", recoveryPath, "error", startupDBError)
		} else {
			slog.Info("configured PostgreSQL target activated after bootstrap DSN failure")
		}
	} else if openErr != nil {
		return nil, openErr
	}
	bootstrapPath, bootstrapPersisted := "", false
	if c.BootstrapAdmin == "" {
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
	a := &App{cfg: c, store: s, keys: apikey.New(s, c.KeyPepper), aead: aead, mux: http.NewServeMux(), traces: observability.New(), rootCtx: ctx, bootstrapPath: bootstrapPath, bootstrapPersisted: bootstrapPersisted, recoveryMode: recoveryMode, databaseStartupErr: startupDBError, recoveryDatabase: recoveryPath}
	a.keys.SetRateLimitAlertLoader(a.rateLimitAlertsEnabled)
	a.secrets = secretstore.New(s, a.seal, a.open, a.vaultClient)
	a.notifier = outboundnotification.New(s, a.notificationDeliveryConfig)
	a.backup = backup.New(s, aead, a.backupConfig)
	if settings, loadErr := a.loadSettingMap(ctx, "logging"); loadErr == nil {
		if applyErr := runtimelogging.Apply(stringValue(settings, "level")); applyErr != nil {
			slog.Warn("stored logging setting could not be applied", "error", applyErr)
		}
	}
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
	a.breakers = source.NewBreakers()
	a.search.SetBreakers(a)
	a.search.SetKeywordLoader(a.openSearchCandidates)
	a.search.SetVectorLoader(a.vectorCandidates)
	a.search.SetGlobalVectorLoader(a.globalVectorCandidates)
	a.quality = quality.New(s, a.search)
	a.mcp = mcp.New(a.search, s)
	a.mcp.SetHealthLoader(func() []source.BreakerState { return a.breakers.States() })
	a.mcp.SetStrictCompatibilityLoader(a.strictMCPCompatibility)
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
	backgroundWorker.SetProjection(a.projectSearchStores)
	backgroundWorker.SetSourceHealth(a)
	backgroundScheduler := scheduler.New(a.store, a.pollingInterval)
	backgroundScheduler.SetRetentionLoader(a.retentionPolicy)
	backgroundScheduler.SetNotificationLoader(a.notificationPolicy)
	a.wg.Add(4)
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
	go func() {
		defer a.wg.Done()
		a.notifier.Run(workerCtx)
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

type HTTPServerConfig struct {
	ListenAddress     string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func (a *App) HTTPServerConfig(ctx context.Context) HTTPServerConfig {
	cfg := HTTPServerConfig{
		ListenAddress:     a.cfg.ListenAddress,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
		ShutdownTimeout:   15 * time.Second,
	}
	settings, err := a.loadSettingMap(ctx, "operations")
	if err != nil {
		return cfg
	}
	if value := strings.TrimSpace(stringValue(settings, "listenAddress")); value != "" {
		cfg.ListenAddress = value
	}
	duration := func(key string, current time.Duration) time.Duration {
		if seconds, ok := settings[key].(float64); ok && seconds > 0 {
			return time.Duration(seconds * float64(time.Second))
		}
		return current
	}
	cfg.ReadHeaderTimeout = duration("readHeaderTimeoutSeconds", cfg.ReadHeaderTimeout)
	cfg.ReadTimeout = duration("readTimeoutSeconds", cfg.ReadTimeout)
	cfg.WriteTimeout = duration("writeTimeoutSeconds", cfg.WriteTimeout)
	cfg.IdleTimeout = duration("idleTimeoutSeconds", cfg.IdleTimeout)
	cfg.ShutdownTimeout = duration("shutdownTimeoutSeconds", cfg.ShutdownTimeout)
	return cfg
}

func (a *App) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.databaseRestart.Load() && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && r.URL.Path != "/api/v1/public/status" && r.URL.Path != "/api/v1/admin/database/status" {
			problem(w, http.StatusServiceUnavailable, "database_restart_required", "PostgreSQL migration completed; restart the service to activate it")
			return
		}
		if enabled, message := a.maintenanceMode(r.Context()); enabled && !maintenanceAllowedPath(r.URL.Path) {
			w.Header().Set("Retry-After", "60")
			if message == "" {
				message = "The service is temporarily in maintenance mode"
			}
			problem(w, http.StatusServiceUnavailable, "maintenance_mode", message)
			return
		}
		if (r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/admin/backups/") && strings.HasSuffix(r.URL.Path, "/restore")) || r.URL.Path == "/api/v1/admin/database/migrate" {
			next.ServeHTTP(w, r)
			return
		}
		a.requestGate.RLock()
		defer a.requestGate.RUnlock()
		next.ServeHTTP(w, r)
	})
}

func (a *App) maintenanceMode(ctx context.Context) (bool, string) {
	settings, err := a.loadSettingMap(ctx, "operations")
	if err != nil {
		return false, ""
	}
	enabled, _ := settings["maintenanceMode"].(bool)
	return enabled, strings.TrimSpace(stringValue(settings, "maintenanceMessage"))
}

func maintenanceAllowedPath(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/metrics" ||
		path == "/api/v1/public/config" || path == "/api/v1/public/status" ||
		path == "/api/v1/bootstrap/login" || path == "/api/v1/recovery/login" || strings.HasPrefix(path, "/auth/") ||
		path == "/admin" || strings.HasPrefix(path, "/admin/") ||
		strings.HasPrefix(path, "/api/v1/admin/") ||
		(!strings.HasPrefix(path, "/api/") && path != "/mcp")
}
func (a *App) routes() {
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	a.mux.HandleFunc("GET /readyz", a.readiness)
	a.mux.HandleFunc("GET /metrics", a.metrics)
	a.mux.HandleFunc("GET /api/v1/public/config", a.publicUIConfig)
	a.mux.HandleFunc("GET /api/v1/public/status", a.publicDatabaseStatus)
	a.mux.HandleFunc("POST /api/v1/bootstrap/login", a.bootstrapLogin)
	a.mux.HandleFunc("POST /api/v1/recovery/login", a.recoveryLogin)
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
	a.mux.Handle("PUT /api/v1/me/api-keys/{id}/scopes", a.authenticate(http.HandlerFunc(a.updateKeyScopes)))
	a.mux.Handle("DELETE /api/v1/me/api-keys/{id}", a.authenticate(http.HandlerFunc(a.revokeKey)))
	a.mux.Handle("GET /api/v1/me/usage", a.authenticate(http.HandlerFunc(a.meUsage)))
	a.mux.Handle("GET /api/v1/me/calls", a.authenticate(http.HandlerFunc(a.meCalls)))
	a.mux.Handle("GET /api/v1/me/notifications", a.authenticate(http.HandlerFunc(a.meNotifications)))
	a.mux.Handle("POST /api/v1/me/notifications/{id}/read", a.authenticate(http.HandlerFunc(a.readNotification)))
	a.mux.Handle("POST /api/v1/tools/resolve/test", a.authenticate(http.HandlerFunc(a.testResolve)))
	a.mux.Handle("POST /api/v1/tools/query/test", a.authenticate(http.HandlerFunc(a.testQuery)))
	a.mux.Handle("POST /api/v1/tools/search-code/test", a.authenticate(http.HandlerFunc(a.testSearchCode)))
	a.mux.Handle("POST /api/v1/tools/find-file/test", a.authenticate(http.HandlerFunc(a.testFindFile)))
	a.mux.Handle("POST /api/v1/tools/read-file/test", a.authenticate(http.HandlerFunc(a.testReadFile)))
	a.mux.Handle("POST /api/v1/tools/semantic/test", a.authenticate(http.HandlerFunc(a.testSemanticSearch)))
	a.mux.Handle("POST /api/v1/tools/dependents/test", a.authenticate(http.HandlerFunc(a.testDependents)))
	a.mux.Handle("POST /api/v1/tools/merge-requests/test", a.authenticate(http.HandlerFunc(a.testMergeRequests)))
	a.mux.Handle("POST /api/v1/tools/file-history/test", a.authenticate(http.HandlerFunc(a.testFileHistory)))
	a.mux.Handle("POST /api/v1/tools/directory/test", a.authenticate(http.HandlerFunc(a.testDirectory)))
	a.mux.Handle("POST /api/v1/tools/repository-map/test", a.authenticate(http.HandlerFunc(a.testRepositoryMap)))
	a.mux.Handle("POST /api/v1/tools/symbols/test", a.authenticate(http.HandlerFunc(a.testSymbols)))
	a.mux.Handle("POST /api/v1/tools/symbol-context/test", a.authenticate(http.HandlerFunc(a.testSymbolContext)))
	a.mux.Handle("POST /api/v1/tools/dependencies/test", a.authenticate(http.HandlerFunc(a.testDependencies)))
	a.mux.Handle("POST /api/v1/tools/compare-refs/test", a.authenticate(http.HandlerFunc(a.testCompareRefs)))
	a.mux.Handle("POST /api/v1/tools/change-impact/test", a.authenticate(http.HandlerFunc(a.testChangeImpact)))
	a.mux.Handle("POST /api/v1/tools/context-pack/test", a.authenticate(http.HandlerFunc(a.testContextPack)))
	a.mux.Handle("POST /api/v1/tools/runbooks/test", a.authenticate(http.HandlerFunc(a.testRunbooks)))
	a.mux.Handle("POST /api/v1/tools/export/test", a.authenticate(http.HandlerFunc(a.testContextExport)))
	a.mux.Handle("GET /api/v1/me/access", a.authenticate(http.HandlerFunc(a.accessDiagnostics)))
	a.mux.Handle("GET /api/v1/admin/settings", a.admin(http.HandlerFunc(a.listSettings)))
	a.mux.Handle("GET /api/v1/admin/settings/{category}", a.settingsAuthorize(http.HandlerFunc(a.getSetting)))
	a.mux.Handle("DELETE /api/v1/admin/settings/{category}", a.settingsAuthorize(http.HandlerFunc(a.deleteSetting)))
	a.mux.Handle("PUT /api/v1/admin/settings/{category}", a.settingsAuthorize(http.HandlerFunc(a.putSetting)))
	a.mux.Handle("POST /api/v1/admin/settings/{category}/test", a.settingsAuthorize(http.HandlerFunc(a.testIntegrationSetting)))
	a.mux.Handle("POST /api/v1/admin/settings/{category}/validate", a.settingsAuthorize(http.HandlerFunc(a.validateIntegrationSetting)))
	a.mux.Handle("POST /api/v1/admin/settings/keycloak/preview", a.settingsAuthorize(http.HandlerFunc(a.previewKeycloak)))
	a.mux.Handle("GET /api/v1/admin/settings/keycloak/status", a.settingsAuthorize(http.HandlerFunc(a.keycloakStatus)))
	a.mux.Handle("GET /api/v1/admin/audit-logs", a.authorize(http.HandlerFunc(a.auditLogs), "auditor", "security-admin"))
	a.mux.Handle("GET /api/v1/admin/users", a.authorize(http.HandlerFunc(a.adminUsers), "platform-admin"))
	a.mux.Handle("POST /api/v1/admin/users", a.authorize(http.HandlerFunc(a.createAdminUser), "platform-admin"))
	a.mux.Handle("PUT /api/v1/admin/users/{id}", a.authorize(http.HandlerFunc(a.updateAdminUser), "platform-admin"))
	a.mux.Handle("DELETE /api/v1/admin/users/{id}", a.authorize(http.HandlerFunc(a.deleteAdminUser), "platform-admin"))
	a.mux.Handle("GET /api/v1/admin/context-packs", a.authorize(http.HandlerFunc(a.contextPacks), "platform-admin", "search-admin"))
	a.mux.Handle("POST /api/v1/admin/context-packs", a.authorize(http.HandlerFunc(a.createContextPack), "platform-admin", "search-admin"))
	a.mux.Handle("PUT /api/v1/admin/context-packs/{id}", a.authorize(http.HandlerFunc(a.updateContextPack), "platform-admin", "search-admin"))
	a.mux.Handle("DELETE /api/v1/admin/context-packs/{id}", a.authorize(http.HandlerFunc(a.deleteContextPack), "platform-admin", "search-admin"))
	a.mux.Handle("GET /api/v1/admin/api-keys", a.authorize(http.HandlerFunc(a.adminAPIKeys), "security-admin"))
	a.mux.Handle("POST /api/v1/admin/api-keys/{id}/revoke", a.authorize(http.HandlerFunc(a.adminRevokeKey), "security-admin"))
	a.mux.Handle("PUT /api/v1/admin/api-keys/{id}/scopes", a.authorize(http.HandlerFunc(a.adminUpdateKeyScopes), "security-admin"))
	a.mux.Handle("GET /api/v1/admin/secrets", a.authorize(http.HandlerFunc(a.listManagedSecrets), "security-admin"))
	a.mux.Handle("POST /api/v1/admin/secrets", a.authorize(http.HandlerFunc(a.putManagedSecret), "security-admin"))
	a.mux.Handle("POST /api/v1/admin/secrets/{name}/rotate", a.authorize(http.HandlerFunc(a.putManagedSecret), "security-admin"))
	a.mux.Handle("POST /api/v1/admin/secrets/{name}/disable", a.authorize(http.HandlerFunc(a.disableManagedSecret), "security-admin"))
	a.mux.Handle("GET /api/v1/admin/security-events", a.authorize(http.HandlerFunc(a.securityEvents), "security-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/notification-deliveries", a.authorize(http.HandlerFunc(a.notificationDeliveries), "security-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/notification-deliveries/{id}/retry", a.authorize(http.HandlerFunc(a.retryNotificationDelivery), "security-admin"))
	a.mux.Handle("POST /api/v1/admin/sources/{source}/discover", a.authorize(http.HandlerFunc(a.discoverSource), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/repositories", a.authorize(http.HandlerFunc(a.adminRepositories), "source-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/repositories", a.authorize(http.HandlerFunc(a.registerRepository), "source-admin"))
	a.mux.Handle("POST /api/v1/admin/repositories/{id}/index", a.authorize(http.HandlerFunc(a.enqueueIndex), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/repositories/{id}/refs", a.authorize(http.HandlerFunc(a.repositoryRefs), "source-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/index-policy-defaults", a.authorize(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jsonOut(w, http.StatusOK, indexer.DefaultPolicy())
	}), "source-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/repositories/{id}/policy", a.authorize(http.HandlerFunc(a.getRepositoryPolicy), "source-admin", "readonly-operator"))
	a.mux.Handle("PUT /api/v1/admin/repositories/{id}/policy", a.authorize(http.HandlerFunc(a.putRepositoryPolicy), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/index-jobs", a.authorize(http.HandlerFunc(a.indexJobs), "source-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/index-jobs/{id}/retry", a.authorize(http.HandlerFunc(a.retryIndexJob), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/setup-status", a.authorize(http.HandlerFunc(a.setupStatus), "readonly-operator", "source-admin", "search-admin", "mcp-admin", "security-admin"))
	a.mux.Handle("POST /api/v1/admin/search-diagnostics", a.authorize(http.HandlerFunc(a.searchDiagnostics), "search-admin"))
	a.mux.Handle("GET /api/v1/admin/settings/{category}/versions", a.settingsAuthorize(http.HandlerFunc(a.settingVersions)))
	a.mux.Handle("GET /api/v1/admin/health", a.authorize(http.HandlerFunc(a.adminHealth), "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/vector/status", a.authorize(http.HandlerFunc(a.vectorStatus), "search-admin", "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/vector/rebuild", a.authorize(http.HandlerFunc(a.vectorRebuild), "search-admin"))
	a.mux.Handle("GET /api/v1/admin/index-diagnostics", a.authorize(http.HandlerFunc(a.indexDiagnostics), "source-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/freshness", a.authorize(http.HandlerFunc(a.adminFreshness), "source-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/database/status", a.authorize(http.HandlerFunc(a.adminDatabaseStatus), "readonly-operator"))
	a.mux.Handle("POST /api/v1/admin/database/test", a.admin(http.HandlerFunc(a.testDatabaseTarget)))
	a.mux.Handle("POST /api/v1/admin/database/migrate", a.admin(http.HandlerFunc(a.migrateDatabaseTarget)))
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
	a.mux.Handle("GET /api/v1/admin/mcp/analytics", a.authorize(http.HandlerFunc(a.mcpAnalytics), "mcp-admin", "readonly-operator", "auditor"))
	// The call log carries user identifiers and query text, so it stays with the
	// roles that already see audit data.
	a.mux.Handle("GET /api/v1/admin/mcp/calls", a.authorize(http.HandlerFunc(a.mcpCalls), "mcp-admin", "auditor", "security-admin"))
	a.mux.Handle("GET /api/v1/admin/mcp/calls/{id}", a.authorize(http.HandlerFunc(a.mcpCallTrace), "mcp-admin", "auditor", "security-admin"))
	// A whole-configuration export or import crosses every category, so it stays
	// with platform-admin rather than any delegated settings role.
	a.mux.Handle("GET /api/v1/admin/settings-export", a.authorize(http.HandlerFunc(a.exportSettings), "platform-admin"))
	a.mux.Handle("POST /api/v1/admin/settings-import", a.authorize(http.HandlerFunc(a.importSettings), "platform-admin"))
	a.mux.Handle("GET /api/v1/admin/settings/{category}/versions/{version}", a.settingsAuthorize(http.HandlerFunc(a.settingVersion)))
	a.mux.Handle("POST /api/v1/admin/settings/{category}/versions/{version}/restore", a.settingsAuthorize(http.HandlerFunc(a.restoreSettingVersion)))
	a.mux.Handle("GET /api/v1/admin/source-health", a.authorize(http.HandlerFunc(a.sourceHealth), "source-admin", "readonly-operator", "mcp-admin"))
	a.mux.Handle("POST /api/v1/admin/source-health/{source}/reset", a.authorize(http.HandlerFunc(a.resetSourceHealth), "source-admin"))
	a.mux.Handle("GET /api/v1/admin/mcp/sessions", a.authorize(http.HandlerFunc(a.mcpSessions), "mcp-admin", "auditor", "security-admin"))
	a.mux.Handle("POST /api/v1/admin/mcp/selfcheck", a.authorize(http.HandlerFunc(a.mcpSelfCheck), "mcp-admin", "source-admin", "search-admin"))
	// A developer debugging their own agent needs the same X-ray for their own
	// calls, without an administrator role.
	a.mux.Handle("GET /api/v1/me/calls/{id}", a.authenticate(http.HandlerFunc(a.mcpCallTrace)))
	a.mux.HandleFunc("GET /admin", a.serveWebApp)
	a.mux.HandleFunc("GET /admin/", a.serveWebApp)
	// 업그레이드 후 브라우저나 내부 프록시가 이전 화면을 섞어 쓰지 못하게 합니다.
	a.mux.Handle("/", revalidate(http.FileServer(webRoot())))
}

func (a *App) serveWebApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// /admin is a client-side route. Serve the same embedded index as / instead
	// of reaching back into the process working directory, which could contain
	// an older web tree (or no web tree at all).
	request := r.Clone(r.Context())
	request.URL.Path = "/"
	http.FileServer(webRoot()).ServeHTTP(w, request)
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
			var subject, username, bitbucketSlug, gitlabID, aclGroupText string
			if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT u.subject,u.username,COALESCE(i.bitbucket_user_slug,''),COALESCE(i.gitlab_user_id,''),COALESCE(i.bitbucket_groups,'') FROM users u LEFT JOIN user_identities i ON i.user_id=u.id WHERE u.id=? AND u.status='active'`), uid).Scan(&subject, &username, &bitbucketSlug, &gitlabID, &aclGroupText); err != nil {
				problem(w, 401, "invalid_token", "User is inactive")
				return
			}
			roles, _ := a.userRoles(r.Context(), uid)
			if uid == "bootstrap-admin" {
				if !a.bootstrapAvailable(r.Context()) {
					problem(w, 401, "invalid_token", "Bootstrap administrator is no longer available")
					return
				}
				roles = []string{"platform-admin"}
			}
			aclPrincipal := bitbucketSlug
			if aclPrincipal == "" && gitlabID != "" {
				aclPrincipal = "gitlab:" + gitlabID
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: uid, Subject: subject, Username: username, ACLPrincipal: aclPrincipal, ACLPrincipals: sourceACLPrincipals(bitbucketSlug, gitlabID, splitCSV(aclGroupText)), Roles: roles, KeyID: kid, KeyPrefix: prefix, Scopes: info.Scopes, AllowedRepositories: info.Restrictions.AllowedRepositories})))
			return
		}
		if cookie, err := r.Cookie("git_ctx_session"); err == nil {
			if principal, expires, ok := a.sessionPrincipal(r.Context(), cookie.Value); ok {
				a.renewSession(w, r, cookie.Value, principal, expires)
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
				ACLPrincipal: aclPrincipal, ACLPrincipals: sourceACLPrincipals(identity.BitbucketUserSlug, identity.GitLabUserID, identity.ACLGroups), Roles: identity.Roles, Groups: identity.Groups,
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
	if !a.credentialAttempts.allow("bootstrap:"+a.requestIP(r), credentialAttemptLimit, credentialAttemptWindow) {
		w.Header().Set("Retry-After", fmt.Sprint(int(credentialAttemptWindow.Seconds())))
		a.audit(r, auth.Principal{UserID: "anonymous"}, "bootstrap.login", "session", "bootstrap", "throttled", nil)
		problem(w, http.StatusTooManyRequests, "too_many_attempts", "Too many bootstrap login attempts from this address")
		return
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
	http.SetCookie(w, recoverySessionCookie(r, rawSession, expires))
	a.audit(r, auth.Principal{UserID: id}, "bootstrap.login", "session", "bootstrap", "success", nil)
	jsonOut(w, http.StatusOK, map[string]any{"status": "authenticated", "expiresAt": expires})
}
func (a *App) recoveryLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		requestOrigin, err := url.Parse(origin)
		if err != nil || (requestOrigin.Scheme != "http" && requestOrigin.Scheme != "https") || !strings.EqualFold(requestOrigin.Host, r.Host) {
			problem(w, http.StatusForbidden, "origin_not_allowed", "Recovery login Origin is not allowed")
			return
		}
	}
	if !a.credentialAttempts.allow("recovery:"+a.requestIP(r), credentialAttemptLimit, credentialAttemptWindow) {
		w.Header().Set("Retry-After", fmt.Sprint(int(credentialAttemptWindow.Seconds())))
		a.audit(r, auth.Principal{UserID: "anonymous"}, "recovery.login", "session", "break-glass", "throttled", nil)
		problem(w, http.StatusTooManyRequests, "too_many_attempts", "Too many recovery login attempts from this address")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var in struct {
		Token string `json:"token"`
	}
	if decodeErr := decode(r, &in); decodeErr != nil {
		a.audit(r, auth.Principal{UserID: "anonymous"}, "recovery.login", "session", "break-glass", "failure", nil)
		problem(w, http.StatusUnauthorized, "invalid_recovery_token", "Recovery token is invalid, expired, or already used")
		return
	}
	tokenHash, tokenExpires, err := recovery.Verify(strings.TrimSpace(in.Token), a.cfg.KeyPepper, time.Now().UTC())
	if err != nil {
		a.audit(r, auth.Principal{UserID: "anonymous"}, "recovery.login", "session", "break-glass", "failure", nil)
		problem(w, http.StatusUnauthorized, "invalid_recovery_token", "Recovery token is invalid, expired, or already used")
		return
	}
	rawSession, err := randomToken(32)
	if err != nil {
		problem(w, http.StatusInternalServerError, "recovery_login_failed", "Unable to create recovery session")
		return
	}
	now := time.Now().UTC()
	sessionExpires := now.Add(30 * time.Minute)
	tx, err := a.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, http.StatusInternalServerError, "recovery_login_failed", "Unable to start recovery session")
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO admin_recovery_tokens(token_hash,expires_at,used_at) VALUES(?,?,?) ON CONFLICT(token_hash) DO NOTHING`), tokenHash, tokenExpires, now)
	if err != nil {
		problem(w, http.StatusInternalServerError, "recovery_login_failed", "Unable to consume recovery token")
		return
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		_ = tx.Rollback()
		a.audit(r, auth.Principal{UserID: "anonymous"}, "recovery.login", "session", "break-glass", "failure", nil)
		problem(w, http.StatusUnauthorized, "invalid_recovery_token", "Recovery token is invalid, expired, or already used")
		return
	}
	const id = "break-glass-admin"
	if _, err = tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO users(id,subject,username,email,status) VALUES(?,?,?,?,'active') ON CONFLICT(id) DO UPDATE SET status='active'`), id, id, id, ""); err == nil {
		_, err = tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO user_roles(user_id,role_code) VALUES(?,'platform-admin') ON CONFLICT(user_id,role_code) DO NOTHING`), id)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM user_sessions WHERE user_id=?`), id)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(?,?,?,?)`), sessionHash(rawSession), id, sessionExpires, now)
	}
	if err != nil || tx.Commit() != nil {
		problem(w, http.StatusInternalServerError, "recovery_login_failed", "Unable to persist recovery session")
		return
	}
	http.SetCookie(w, recoverySessionCookie(r, rawSession, sessionExpires))
	a.audit(r, auth.Principal{UserID: id}, "recovery.login", "session", "break-glass", "success", map[string]any{"expiresAt": sessionExpires})
	jsonOut(w, http.StatusOK, map[string]any{"status": "authenticated", "expiresAt": sessionExpires})
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
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := auth.ExchangeCode(ctx, cfg, code, verifier)
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
	if accessIdentity, accessErr := a.oidc.VerifyAccessToken(ctx, token.AccessToken); accessErr == nil {
		for _, role := range accessIdentity.Roles {
			if !slices.Contains(identity.Roles, role) {
				identity.Roles = append(identity.Roles, role)
			}
		}
		if len(accessIdentity.Groups) > 0 {
			identity.Groups = accessIdentity.Groups
		}
		if len(accessIdentity.ACLGroups) > 0 {
			identity.ACLGroups = accessIdentity.ACLGroups
		}
	}
	userID, err := a.upsertIdentity(ctx, identity)
	if err != nil {
		if errors.Is(err, errUserDisabled) {
			problem(w, 403, "user_disabled", "This user account is disabled")
			return
		}
		problem(w, 500, "identity_sync_failed", "Unable to synchronize authenticated identity")
		return
	}
	rawSession, err := randomToken(32)
	if err != nil {
		problem(w, 500, "internal_error", "Unable to create session")
		return
	}
	// The browser session is intentionally not tied to token.Expiry. Keycloak
	// access tokens usually live a few minutes, which previously signed the user
	// out on the next page refresh.
	sessionExpiry := time.Now().UTC().Add(browserSessionLifetime)
	_, err = a.store.DB.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(?,?,?,?)`), sessionHash(rawSession), userID, sessionExpiry.UTC(), time.Now().UTC())
	if err != nil {
		problem(w, 500, "internal_error", "Unable to persist session")
		return
	}
	http.SetCookie(w, sessionCookie(r, rawSession, sessionExpiry))
	a.audit(r, auth.Principal{UserID: userID}, "login", "session", "browser", "success", nil)
	if stringContains(identity.Roles, "platform-admin") {
		a.disableBootstrapAdmin()
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("git_ctx_session"); err == nil {
		_, _ = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM user_sessions WHERE id_hash=?`), sessionHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "git_ctx_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode})
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

// browserSessionLifetime is how long a browser session stays valid. It is
// deliberately independent from the Keycloak access token lifetime, which is
// often five minutes: binding the cookie to that value logged administrators
// out again on the next page refresh.
const browserSessionLifetime = 12 * time.Hour

// sessionRenewalThreshold slides the session forward while the user is active
// so long admin work is never interrupted by a hard expiry.
const sessionRenewalThreshold = 2 * time.Hour

// requestIsSecure reports whether the browser reached this instance over TLS.
// The Secure attribute must follow the actual request scheme: deriving it from
// the configured public URL sets Secure on plain HTTP deployments, and the
// browser then silently discards the session cookie on every refresh.
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); strings.EqualFold(proto, "https") {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Ssl"), "on")
}

// sessionCookie issues the SSO browser session. Lax keeps the cookie on the top
// level redirect back from Keycloak while still blocking cross site form posts.
func sessionCookie(r *http.Request, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name: "git_ctx_session", Value: value, Path: "/", HttpOnly: true,
		Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode, Expires: expires,
	}
}

// recoverySessionCookie issues the short lived bootstrap and break-glass
// sessions. They never involve an external redirect, so they stay Strict.
func recoverySessionCookie(r *http.Request, value string, expires time.Time) *http.Cookie {
	cookie := sessionCookie(r, value, expires)
	cookie.SameSite = http.SameSiteStrictMode
	return cookie
}

// renewSession extends an active browser session that is close to expiry.
// Bootstrap and break-glass sessions keep their deliberately short lifetime.
func (a *App) renewSession(w http.ResponseWriter, r *http.Request, raw string, p auth.Principal, expires time.Time) {
	if p.UserID == "bootstrap-admin" || p.UserID == "break-glass-admin" {
		return
	}
	if time.Until(expires) > sessionRenewalThreshold {
		return
	}
	next := time.Now().UTC().Add(browserSessionLifetime)
	if _, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE user_sessions SET expires_at=? WHERE id_hash=?`), next, sessionHash(raw)); err != nil {
		return
	}
	http.SetCookie(w, sessionCookie(r, raw, next))
}

func (a *App) sessionPrincipal(ctx context.Context, raw string) (auth.Principal, time.Time, bool) {
	if raw == "" {
		return auth.Principal{}, time.Time{}, false
	}
	var userID, subject, username, bitbucketSlug, gitlabID, bitbucketGroups string
	var expires time.Time
	err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT s.user_id,u.subject,u.username,COALESCE(i.bitbucket_user_slug,''),COALESCE(i.gitlab_user_id,''),COALESCE(i.bitbucket_groups,''),s.expires_at FROM user_sessions s JOIN users u ON u.id=s.user_id LEFT JOIN user_identities i ON i.user_id=u.id WHERE s.id_hash=? AND u.status='active'`), sessionHash(raw)).Scan(&userID, &subject, &username, &bitbucketSlug, &gitlabID, &bitbucketGroups, &expires)
	if err != nil || time.Now().After(expires) {
		return auth.Principal{}, time.Time{}, false
	}
	acl := bitbucketSlug
	if acl == "" && gitlabID != "" {
		acl = "gitlab:" + gitlabID
	}
	roles, err := a.userRoles(ctx, userID)
	if err != nil {
		return auth.Principal{}, time.Time{}, false
	}
	if userID == "bootstrap-admin" {
		if !a.bootstrapAvailable(ctx) {
			return auth.Principal{}, time.Time{}, false
		}
		roles = []string{"platform-admin"}
	}
	_, _ = a.store.DB.ExecContext(ctx, a.store.Rebind(`UPDATE user_sessions SET last_seen_at=? WHERE id_hash=?`), time.Now().UTC(), sessionHash(raw))
	return auth.Principal{UserID: userID, Subject: subject, Username: username, ACLPrincipal: acl, ACLPrincipals: sourceACLPrincipals(bitbucketSlug, gitlabID, splitCSV(bitbucketGroups)), Roles: roles}, expires, true
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
			// State exactly which role is missing and which roles the caller has.
			// A bare "forbidden" leaves an administrator with no way to tell a
			// Keycloak role mapping problem from a revoked account.
			required := append([]string{"platform-admin"}, settingCategoryRoles()[category]...)
			current := "none"
			if len(p.Roles) > 0 {
				current = strings.Join(p.Roles, ", ")
			}
			problem(w, http.StatusForbidden, "insufficient_role", fmt.Sprintf(
				"Changing the %q setting requires one of these platform roles: %s. This account currently has: %s. Map a Keycloak realm role to a platform role in the Keycloak setting, or assign the role in user management.",
				category, strings.Join(required, ", "), current))
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

// settingCategoryRoles lists the delegated role that may change each setting
// category in addition to platform-admin. Categories that are absent are
// reserved for platform-admin only.
func settingCategoryRoles() map[string][]string {
	return map[string][]string{
		"bitbucket": {"source-admin"}, "gitlab": {"source-admin"}, "confluence": {"source-admin"}, "jira": {"source-admin"}, "index": {"source-admin"},
		"mcp": {"mcp-admin"}, "search": {"search-admin"}, "model": {"search-admin"}, "opensearch": {"search-admin"}, "vector": {"search-admin"},
		"security": {"security-admin"}, "vault": {"security-admin"},
	}
}
func settingRoleAllowed(p auth.Principal, category string) bool {
	return roleAllowed(p, settingCategoryRoles()[category]...)
}

// accessDiagnostics explains why the signed in account can or cannot change
// each setting category, and whether the source ACL identity required for code
// search is present. The administration UI renders it in the ACL guide.
func (a *App) accessDiagnostics(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	categories := make([]map[string]any, 0, len(settingCategories()))
	names := make([]string, 0, len(settingCategories()))
	for category := range settingCategories() {
		names = append(names, category)
	}
	slices.Sort(names)
	for _, category := range names {
		categories = append(categories, map[string]any{
			"category": category,
			"allowed":  settingRoleAllowed(p, category),
			"roles":    append([]string{"platform-admin"}, settingCategoryRoles()[category]...),
		})
	}
	var rolesManaged int
	_ = a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT roles_managed FROM users WHERE id=?`), p.UserID).Scan(&rolesManaged)
	keycloak := map[string]any{"configured": false}
	if cfg, err := a.loadOIDCConfig(r.Context()); err == nil {
		mapped := make([]string, 0, len(cfg.RealmRoleMappings)+len(cfg.ClientRoleMappings))
		for keycloakRole, platformRole := range cfg.RealmRoleMappings {
			mapped = append(mapped, "realm:"+keycloakRole+" → "+platformRole)
		}
		for keycloakRole, platformRole := range cfg.ClientRoleMappings {
			mapped = append(mapped, "client:"+keycloakRole+" → "+platformRole)
		}
		slices.Sort(mapped)
		keycloak = map[string]any{
			"configured": true, "issuerUrl": cfg.IssuerURL, "clientId": cfg.ClientID,
			"roleMappings": mapped, "groupsClaim": cfg.GroupsClaim,
			"bitbucketUserSlugClaim": cfg.BitbucketUserSlugClaim, "gitlabUserIdClaim": cfg.GitLabUserIDClaim,
		}
	}
	principals := aclPrincipals(p.ACLPrincipal, p.ACLPrincipals)
	unrestricted := search.GrantsUnrestrictedSearch(p.Roles)
	jsonOut(w, http.StatusOK, map[string]any{
		"username": p.Username, "subject": p.Subject, "roles": p.Roles, "groups": p.Groups,
		"rolesManagedLocally": rolesManaged == 1,
		"aclPrincipal":        p.ACLPrincipal, "aclPrincipals": principals,
		"unrestrictedSearch": unrestricted,
		"aclReady":           len(principals) > 0 || unrestricted,
		"settings":           categories,
		"keycloak":           keycloak,
		"platformAdmin":      p.HasRole("platform-admin"),
		"platformRoles":      auth.PlatformRoles(),
		"recoveryEntry":      p.UserID == "bootstrap-admin" || p.UserID == "break-glass-admin",
		"sessionSeconds":     int(browserSessionLifetime.Seconds()),
	})
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
	principals := searchPrincipals(p)
	if len(principals) == 0 {
		jsonOut(w, 200, []any{})
		return
	}
	join, predicate, args := search.RepositoryACLClause(principals)
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT DISTINCT r.library_id,r.name,r.description,r.default_branch,r.reputation,r.indexed_at FROM repositories r `+join+` WHERE r.enabled=1 AND `+predicate+` ORDER BY r.library_id`), args...)
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
	if p.UserID == "break-glass-admin" {
		problem(w, http.StatusForbidden, "recovery_session_restricted", "Recovery sessions cannot create persistent MCP API keys")
		return
	}
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
	if !keyScopesAllowed(p, in.Scopes) {
		problem(w, http.StatusForbidden, "forbidden_scope", "The current administrator role cannot grant the requested MCP management tool")
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

func keyScopesAllowed(p auth.Principal, scopes []string) bool {
	for _, scope := range scopes {
		switch scope {
		case "get-platform-status":
			if !roleAllowed(p, "readonly-operator", "source-admin", "mcp-admin", "search-admin", "security-admin", "auditor") {
				return false
			}
		case "list-index-jobs":
			if !roleAllowed(p, "source-admin", "readonly-operator") {
				return false
			}
		case "reindex-repository":
			if !roleAllowed(p, "source-admin") {
				return false
			}
		}
	}
	return true
}

func (a *App) updateKeyScopes(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Scopes []string `json:"scopes"`
	}
	if decode(r, &in) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "scopes are required")
		return
	}
	if !keyScopesAllowed(p, in.Scopes) {
		problem(w, http.StatusForbidden, "forbidden_scope", "The current administrator role cannot grant the requested MCP management tool")
		return
	}
	if err := a.keys.UpdateScopes(r.Context(), p.UserID, r.PathValue("id"), in.Scopes); err != nil {
		problem(w, http.StatusBadRequest, "scope_update_failed", err.Error())
		return
	}
	a.audit(r, p, "api_key.scopes_update", "api_key", r.PathValue("id"), "success", map[string]any{"scopes": in.Scopes})
	w.WriteHeader(http.StatusNoContent)
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
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT tool,outcome,COUNT(*),COALESCE(AVG(duration_ms),0),COALESCE(MAX(duration_ms),0),COALESCE(AVG(response_bytes),0),COALESCE(MAX(response_bytes),0),COALESCE(SUM(truncated),0) FROM mcp_calls WHERE user_id=? GROUP BY tool,outcome ORDER BY tool,outcome`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var tool, outcome string
		var count, truncated, maxBytes int64
		var avg, maxLatency, averageBytes float64
		if err := rows.Scan(&tool, &outcome, &count, &avg, &maxLatency, &averageBytes, &maxBytes, &truncated); err != nil {
			return
		}
		out = append(out, map[string]any{"tool": tool, "outcome": outcome, "calls": count, "averageLatencyMs": avg, "maximumLatencyMs": maxLatency,
			"averageResponseBytes": averageBytes, "maximumResponseBytes": maxBytes, "truncatedCalls": truncated})
	}
	jsonOut(w, 200, out)
}
func (a *App) meCalls(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT id,occurred_at,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,result_count,trace_summary FROM mcp_calls WHERE user_id=? ORDER BY occurred_at DESC LIMIT 200`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var at time.Time
		var id, prefix, tool, libraryID, outcome, ip, summary string
		var duration, results int64
		if err := rows.Scan(&id, &at, &prefix, &tool, &libraryID, &outcome, &duration, &ip, &results, &summary); err != nil {
			return
		}
		out = append(out, map[string]any{"id": id, "occurredAt": at, "apiKeyPrefix": prefix, "tool": tool, "libraryId": libraryID,
			"outcome": outcome, "durationMs": duration, "clientIp": ip, "resultCount": results, "traceSummary": summary})
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
	if err != nil {
		previous = map[string]any{}
	}
	unresolvedSecrets = preserveMasked(previous, value)
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
	a.audit(r, p, "settings.delete", category, category, "success", nil)
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
func (a *App) previewKeycloak(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Config map[string]any `json:"config"`
		Token  string         `json:"token"`
	}
	if decode(r, &in) != nil || in.Token == "" {
		problem(w, 400, "invalid_request", "Candidate config and a short-lived test token are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := a.normalizeSetting(ctx, "keycloak", in.Config); err != nil {
		problem(w, 400, "claim_preview_failed", err.Error())
		return
	}
	resolved, err := a.secrets.Resolve(ctx, in.Config)
	if err != nil {
		problem(w, 400, "claim_preview_failed", err.Error())
		return
	}
	raw, _ := json.Marshal(resolved)
	var candidate auth.OIDCConfig
	if err := json.Unmarshal(raw, &candidate); err != nil {
		problem(w, 400, "claim_preview_failed", err.Error())
		return
	}
	verifier := auth.NewOIDCVerifier(func(context.Context) (auth.OIDCConfig, error) { return candidate, nil })
	identity, err := verifier.Verify(ctx, in.Token)
	if err != nil {
		problem(w, 400, "claim_preview_failed", err.Error())
		return
	}
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "keycloak.claim_preview", "keycloak", "candidate", "success", nil)
	jsonOut(w, 200, map[string]any{"subject": identity.Subject, "username": identity.Username, "email": identity.Email, "groups": identity.Groups, "roles": identity.Roles, "bitbucketUserSlug": identity.BitbucketUserSlug, "gitlabUserId": identity.GitLabUserID})
}
func (a *App) keycloakStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.loadOIDCConfig(r.Context())
	if err != nil {
		problem(w, http.StatusNotFound, "sso_not_configured", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	metadata, err := auth.InspectOIDC(ctx, cfg)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "sso_discovery_failed", err.Error())
		return
	}
	var version int
	_ = a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT version FROM system_settings WHERE category=?`), "keycloak").Scan(&version)
	jsonOut(w, http.StatusOK, map[string]any{"status": "active", "version": version, "issuerUrl": cfg.IssuerURL, "clientId": cfg.ClientID, "redirectUrl": cfg.RedirectURL, "tlsVerify": cfg.TLSVerify == nil || *cfg.TLSVerify, "metadata": metadata, "checkedAt": time.Now().UTC()})
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
func (a *App) normalizeSetting(ctx context.Context, category string, value map[string]any) error {
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
	case "vector":
		cfg := vectorstore.FromMap(value)
		if !cfg.Enabled() {
			return nil
		}
		store, err := vectorstore.Open(cfg, a.postgresDSN(ctx))
		if err != nil {
			return err
		}
		defer store.Close()
		// The connection test also creates the collection when the dimensions are
		// known, so the first projection does not fail on a missing table.
		if _, err = store.Status(ctx); err != nil {
			return err
		}
		if dimensions := cfg.Dimensions; dimensions > 0 {
			return store.Ensure(ctx, dimensions)
		}
		return nil
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
	var keycloakConfigured int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM system_settings WHERE category='keycloak'`).Scan(&keycloakConfigured); err != nil || keycloakConfigured > 0 {
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

func (a *App) notificationPolicy(ctx context.Context) scheduler.NotificationPolicy {
	settings, err := a.loadSettingMap(ctx, "notifications")
	if err != nil {
		return scheduler.NotificationPolicy{Enabled: true, APIKeyExpiryWarningDays: 7}
	}
	enabled := true
	if value, ok := settings["inAppEnabled"].(bool); ok {
		enabled = value
	}
	days := 7
	if value, ok := settings["apiKeyExpiryWarningDays"].(float64); ok {
		days = int(value)
	}
	return scheduler.NotificationPolicy{Enabled: enabled, APIKeyExpiryWarningDays: days}
}

func (a *App) notificationDeliveryConfig(ctx context.Context) (outboundnotification.Config, error) {
	settings, err := a.loadSettingMap(ctx, "notifications")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notificationDeliveryConfigFromMap(map[string]any{}, time.Now().UTC())
		}
		return outboundnotification.Config{}, err
	}
	if a.secrets != nil {
		settings, err = a.secrets.Resolve(ctx, settings)
		if err != nil {
			return outboundnotification.Config{}, err
		}
	}
	activeSince := time.Now().UTC()
	_ = a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT updated_at FROM system_settings WHERE category=?`), "notifications").Scan(&activeSince)
	return notificationDeliveryConfigFromMap(settings, activeSince)
}

func notificationDeliveryConfigFromMap(settings map[string]any, activeSince time.Time) (outboundnotification.Config, error) {
	cfg := outboundnotification.Config{
		ActiveSince: activeSince,
		SMTPPort:    587,
		SMTPTLSMode: "starttls",
		Timeout:     15 * time.Second,
		MaxAttempts: 5,
	}
	cfg.Enabled, _ = settings["externalEnabled"].(bool)
	cfg.WebhookURL, _ = settings["webhookUrl"].(string)
	cfg.WebhookAuthorization, _ = settings["webhookAuthorization"].(string)
	cfg.MessengerWebhookURL, _ = settings["messengerWebhookUrl"].(string)
	cfg.MessengerAuthorization, _ = settings["messengerAuthorization"].(string)
	cfg.SMTPEnabled, _ = settings["smtpEnabled"].(bool)
	cfg.SMTPHost, _ = settings["smtpHost"].(string)
	cfg.SMTPUsername, _ = settings["smtpUsername"].(string)
	cfg.SMTPPassword, _ = settings["smtpPassword"].(string)
	cfg.SMTPFrom, _ = settings["smtpFrom"].(string)
	cfg.TestRecipient, _ = settings["testRecipient"].(string)
	if value, ok := settings["smtpPort"].(float64); ok {
		cfg.SMTPPort = int(value)
	}
	if value, ok := settings["smtpTlsMode"].(string); ok && value != "" {
		cfg.SMTPTLSMode = value
	}
	if value, ok := settings["timeoutSeconds"].(float64); ok {
		cfg.Timeout = time.Duration(value * float64(time.Second))
	}
	if value, ok := settings["maxAttempts"].(float64); ok {
		cfg.MaxAttempts = int(value)
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	cfg.CACertificate, _ = settings["caCertificate"].(string)
	cfg.ProxyURL, _ = settings["proxyUrl"].(string)
	return cfg, nil
}

func (a *App) rateLimitAlertsEnabled(ctx context.Context) bool {
	settings, err := a.loadSettingMap(ctx, "notifications")
	if err != nil {
		return true
	}
	if enabled, ok := settings["inAppEnabled"].(bool); ok && !enabled {
		return false
	}
	if enabled, ok := settings["rateLimitAlertsEnabled"].(bool); ok {
		return enabled
	}
	return true
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

func (a *App) strictMCPCompatibility(ctx context.Context) bool {
	settings, err := a.loadSettingMap(ctx, "mcp")
	if err != nil {
		return false
	}
	strict, _ := settings["strictCompatibility"].(bool)
	return strict
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

type vaultSetting struct {
	Enabled bool
	secretstore.VaultConfig
}

func vaultConfigFromMap(settings map[string]any) (vaultSetting, error) {
	cfg := vaultSetting{VaultConfig: secretstore.VaultConfig{Mount: "secret", Prefix: "git-ctx", Timeout: 30 * time.Second}}
	cfg.Enabled, _ = settings["enabled"].(bool)
	cfg.BaseURL, _ = settings["baseUrl"].(string)
	cfg.Token, _ = settings["token"].(string)
	cfg.Namespace, _ = settings["namespace"].(string)
	if value, ok := settings["mount"].(string); ok && value != "" {
		cfg.Mount = value
	}
	if value, ok := settings["prefix"].(string); ok && value != "" {
		cfg.Prefix = value
	}
	if seconds, ok := settings["timeoutSeconds"].(float64); ok && seconds > 0 {
		cfg.Timeout = time.Duration(seconds * float64(time.Second))
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	cfg.CACertificate, _ = settings["caCertificate"].(string)
	cfg.ProxyURL, _ = settings["proxyUrl"].(string)
	if cfg.Enabled && (strings.TrimSpace(cfg.BaseURL) == "" || cfg.Token == "") {
		return cfg, errors.New("vault.baseUrl and vault.token are required when enabled")
	}
	return cfg, nil
}

func (a *App) vaultClient(ctx context.Context) (*secretstore.Vault, error) {
	settings, err := a.loadSettingMapRaw(ctx, "vault")
	if err != nil {
		return nil, errors.New("vault backend is not configured")
	}
	cfg, err := vaultConfigFromMap(settings)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("vault backend is disabled")
	}
	return secretstore.NewVault(cfg.VaultConfig)
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

// vectorStore opens the configured vector database. The zero configuration case
// is not an error: git-ctx scores the embeddings stored beside the text instead.
func (a *App) vectorStore(ctx context.Context) (vectorstore.Store, error) {
	settings, err := a.loadSettingMap(ctx, "vector")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, vectorstore.ErrNotConfigured
		}
		return nil, err
	}
	cfg := vectorstore.FromMap(settings)
	if !cfg.Enabled() {
		return nil, vectorstore.ErrNotConfigured
	}
	return vectorstore.Open(cfg, a.postgresDSN(ctx))
}

// postgresDSN returns the platform database DSN when it is PostgreSQL, so a
// pgvector deployment can reuse it instead of storing a second credential.
func (a *App) postgresDSN(ctx context.Context) string {
	if a.store.Driver() != "postgres" {
		return ""
	}
	if dsn, err := configuredDatabaseDSN(ctx, a.store, a.aead); err == nil && dsn != "" {
		return dsn
	}
	return a.cfg.DatabaseDSN
}

// projectSearchStores keeps every optional search backend in step with a ref
// that finished indexing. A failure here retries the job, so both projections
// report their errors.
func (a *App) projectSearchStores(ctx context.Context, repositoryID, ref string) error {
	if err := a.projectOpenSearch(ctx, repositoryID, ref); err != nil {
		return err
	}
	return a.projectVectors(ctx, repositoryID, ref)
}

// projectVectors republishes the embeddings of one ref to the vector database.
func (a *App) projectVectors(ctx context.Context, repositoryID, ref string) error {
	store, err := a.vectorStore(ctx)
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vector database: %w", err)
	}
	defer store.Close()
	if err = store.DeleteRef(ctx, repositoryID, ref); err != nil {
		return fmt.Errorf("vector database: %w", err)
	}
	_, err = a.streamVectors(ctx, store, repositoryID, ref)
	if err != nil {
		return fmt.Errorf("vector database: %w", err)
	}
	return nil
}

// streamVectors reads stored embeddings in batches and upserts them, so a full
// rebuild never loads the whole corpus into memory.
func (a *App) streamVectors(ctx context.Context, store vectorstore.Store, repositoryID, ref string) (int, error) {
	statement := `SELECT c.id,c.repository_id,c.ref_name,COALESCE(r.library_id,''),c.file_path,c.embedding
FROM document_chunks c LEFT JOIN repositories r ON r.id=c.repository_id
WHERE c.embedding IS NOT NULL`
	var args []any
	if repositoryID != "" {
		statement += ` AND c.repository_id=?`
		args = append(args, repositoryID)
	}
	if ref != "" {
		statement += ` AND c.ref_name=?`
		args = append(args, ref)
	}
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(statement), args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	batch := make([]vectorstore.Chunk, 0, 500)
	total := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := store.Upsert(ctx, batch); err != nil {
			return err
		}
		total += len(batch)
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		var chunk vectorstore.Chunk
		var raw []byte
		if err = rows.Scan(&chunk.ID, &chunk.RepositoryID, &chunk.Ref, &chunk.LibraryID, &chunk.FilePath, &raw); err != nil {
			return total, err
		}
		chunk.Vector = embedding.Decode(raw)
		if len(chunk.Vector) == 0 {
			continue
		}
		batch = append(batch, chunk)
		if len(batch) == cap(batch) {
			if err = flush(); err != nil {
				return total, err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return total, err
	}
	return total, flush()
}

// vectorCandidates is the search side of the integration.
func (a *App) vectorCandidates(ctx context.Context, repositoryID, ref, query string, limit int) ([]search.VectorCandidate, error) {
	store, err := a.vectorStore(ctx)
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer store.Close()
	provider, providerErr := a.embeddingProvider(ctx)
	if providerErr != nil {
		return nil, providerErr
	}
	vector, embedErr := provider.Embed(ctx, query)
	if embedErr != nil {
		return nil, embedErr
	}
	matches, searchErr := store.Search(ctx, repositoryID, ref, vector, limit)
	if searchErr != nil {
		return nil, searchErr
	}
	out := make([]search.VectorCandidate, 0, len(matches))
	for _, match := range matches {
		out = append(out, search.VectorCandidate{ID: match.ID, Score: match.Score})
	}
	return out, nil
}

// globalVectorCandidates asks the vector database for nearest neighbours across
// every repository. The repository ACL is applied by the caller afterwards.
func (a *App) globalVectorCandidates(ctx context.Context, query string, limit int) ([]search.VectorCandidate, error) {
	store, err := a.vectorStore(ctx)
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer store.Close()
	provider, providerErr := a.embeddingProvider(ctx)
	if providerErr != nil {
		return nil, providerErr
	}
	vector, embedErr := provider.Embed(ctx, query)
	if embedErr != nil {
		return nil, embedErr
	}
	matches, searchErr := store.SearchGlobal(ctx, vector, limit)
	if searchErr != nil {
		return nil, searchErr
	}
	out := make([]search.VectorCandidate, 0, len(matches))
	for _, match := range matches {
		out = append(out, search.VectorCandidate{ID: match.ID, Score: match.Score})
	}
	return out, nil
}

func (a *App) openSearchCandidates(ctx context.Context, repositoryID, ref string, principals []string, query string, limit int) ([]search.KeywordCandidate, error) {
	client, enabled, err := a.openSearchClient(ctx)
	if err != nil {
		return nil, err
	}
	if enabled {
		candidates, searchErr := client.Search(ctx, repositoryID, ref, principals, query, limit)
		if searchErr != nil {
			return nil, searchErr
		}
		out := make([]search.KeywordCandidate, len(candidates))
		for i, candidate := range candidates {
			out[i] = search.KeywordCandidate{ID: candidate.ID, Score: candidate.Score}
		}
		return out, nil
	}
	if a.store.Driver() != "postgres" || len(principals) == 0 {
		return nil, nil
	}
	if limit < 1 || limit > 20000 {
		limit = 5000
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(principals)), ",")
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(`SELECT c.id,
ts_rank_cd(to_tsvector('simple',COALESCE(c.heading,'') || ' ' || COALESCE(c.content,'')),plainto_tsquery('simple',?))
FROM document_chunks c WHERE c.repository_id=? AND c.ref_name=? AND
to_tsvector('simple',COALESCE(c.heading,'') || ' ' || COALESCE(c.content,'')) @@ plainto_tsquery('simple',?) AND
EXISTS (SELECT 1 FROM repository_permissions p WHERE p.repository_id=c.repository_id AND (p.principal IN (`+placeholders+`) OR p.principal='*'))
ORDER BY 2 DESC LIMIT ?`), append([]any{query, repositoryID, ref, query}, append(principalArgs(principals), limit)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []search.KeywordCandidate
	for rows.Next() {
		var item search.KeywordCandidate
		if err = rows.Scan(&item.ID, &item.Score); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func principalArgs(principals []string) []any {
	out := make([]any, len(principals))
	for index, principal := range principals {
		out[index] = principal
	}
	return out
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

var platformRoles = []string{
	"platform-admin", "security-admin", "mcp-admin", "source-admin",
	"search-admin", "auditor", "developer", "service-account", "readonly-operator",
}
var errUserDisabled = errors.New("user is disabled")

type adminUserInput struct {
	Subject  string   `json:"subject"`
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Status   string   `json:"status"`
	Roles    []string `json:"roles"`
}

type contextPackInput struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
	Items       []struct {
		LibraryID string `json:"libraryId"`
		Ref       string `json:"ref"`
		QueryHint string `json:"queryHint"`
	} `json:"items"`
}

func (a *App) contextPacks(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT id,slug,name,description,enabled,created_by,created_at,updated_at FROM context_packs ORDER BY name`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	var packs []map[string]any
	for rows.Next() {
		var id, slug, name, description, createdBy string
		var enabled int
		var createdAt, updatedAt time.Time
		if rows.Scan(&id, &slug, &name, &description, &enabled, &createdBy, &createdAt, &updatedAt) != nil {
			continue
		}
		packs = append(packs, map[string]any{"id": id, "slug": slug, "name": name, "description": description, "enabled": enabled != 0, "createdBy": createdBy, "createdAt": createdAt, "updatedAt": updatedAt})
	}
	rows.Close()
	for _, pack := range packs {
		itemRows, queryErr := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT library_id,ref_name,query_hint FROM context_pack_items WHERE pack_id=? ORDER BY position,library_id`), pack["id"])
		if queryErr != nil {
			continue
		}
		var items []map[string]string
		for itemRows.Next() {
			var libraryID, ref, hint string
			if itemRows.Scan(&libraryID, &ref, &hint) == nil {
				items = append(items, map[string]string{"libraryId": libraryID, "ref": ref, "queryHint": hint})
			}
		}
		itemRows.Close()
		pack["items"] = items
	}
	jsonOut(w, 200, packs)
}

func (a *App) createContextPack(w http.ResponseWriter, r *http.Request) {
	var input contextPackInput
	if decode(r, &input) != nil || strings.TrimSpace(input.Slug) == "" || strings.TrimSpace(input.Name) == "" {
		problem(w, 400, "invalid_request", "slug and name are required")
		return
	}
	id, err := randomToken(18)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err = a.saveContextPack(r.Context(), id, input, p.UserID, true); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			problem(w, 409, "duplicate", "Context pack slug already exists")
		} else {
			problem(w, 400, "invalid_request", err.Error())
		}
		return
	}
	jsonOut(w, 201, map[string]string{"id": id})
}

func (a *App) updateContextPack(w http.ResponseWriter, r *http.Request) {
	var input contextPackInput
	if decode(r, &input) != nil || strings.TrimSpace(input.Slug) == "" || strings.TrimSpace(input.Name) == "" {
		problem(w, 400, "invalid_request", "slug and name are required")
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := a.saveContextPack(r.Context(), r.PathValue("id"), input, p.UserID, false); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) saveContextPack(ctx context.Context, id string, input contextPackInput, userID string, create bool) error {
	if len(input.Items) == 0 || len(input.Items) > 50 {
		return errors.New("one to fifty context pack items are required")
	}
	for _, item := range input.Items {
		if baseLibraryID(item.LibraryID) == "" {
			return errors.New("each item requires a valid libraryId")
		}
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO context_packs(id,slug,name,description,enabled,created_by) VALUES(?,?,?,?,?,?)`), id, strings.ToLower(strings.TrimSpace(input.Slug)), strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), enabled, userID)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, a.store.Rebind(`UPDATE context_packs SET slug=?,name=?,description=?,enabled=?,updated_at=? WHERE id=?`), strings.ToLower(strings.TrimSpace(input.Slug)), strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), enabled, time.Now().UTC(), id)
		if err == nil {
			if count, _ := result.RowsAffected(); count == 0 {
				return errors.New("context pack not found")
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM context_pack_items WHERE pack_id=?`), id); err != nil {
		return err
	}
	for position, item := range input.Items {
		if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO context_pack_items(pack_id,library_id,ref_name,query_hint,position) VALUES(?,?,?,?,?)`), id, baseLibraryID(item.LibraryID), strings.TrimSpace(item.Ref), strings.TrimSpace(item.QueryHint), position); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) deleteContextPack(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM context_packs WHERE id=?`), r.PathValue("id"))
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		problem(w, 404, "not_found", "Context pack not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT id,subject,username,email,status,created_at FROM users WHERE id NOT IN ('bootstrap-admin','break-glass-admin') ORDER BY username`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	type listedUser struct {
		id, subject, username, email, status string
		created                              time.Time
	}
	users := make([]listedUser, 0)
	for rows.Next() {
		var user listedUser
		if err = rows.Scan(&user.id, &user.subject, &user.username, &user.email, &user.status, &user.created); err != nil {
			rows.Close()
			problem(w, 500, "internal_error", err.Error())
			return
		}
		users = append(users, user)
	}
	if err = rows.Close(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		roles, roleErr := a.userRoles(r.Context(), user.id)
		if roleErr != nil {
			problem(w, 500, "internal_error", roleErr.Error())
			return
		}
		out = append(out, map[string]any{"id": user.id, "subject": user.subject, "username": user.username, "email": user.email, "status": user.status, "roles": roles, "createdAt": user.created})
	}
	jsonOut(w, 200, map[string]any{"users": out, "roles": platformRoles})
}

func validateAdminUserInput(in *adminUserInput, requireSubject bool) error {
	in.Subject, in.Username, in.Email, in.Status = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Username), strings.TrimSpace(in.Email), strings.TrimSpace(in.Status)
	if in.Username == "" || (requireSubject && in.Subject == "") {
		return errors.New("subject and username are required")
	}
	if len(in.Username) > 200 || len(in.Subject) > 500 || len(in.Email) > 320 {
		return errors.New("user field is too long")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.Status != "active" && in.Status != "disabled" {
		return errors.New("status must be active or disabled")
	}
	uniqueRoles := make([]string, 0, len(in.Roles))
	for _, role := range in.Roles {
		if !slices.Contains(platformRoles, role) {
			return fmt.Errorf("unsupported role: %s", role)
		}
		if !slices.Contains(uniqueRoles, role) {
			uniqueRoles = append(uniqueRoles, role)
		}
	}
	in.Roles = uniqueRoles
	if len(in.Roles) == 0 {
		in.Roles = []string{"developer"}
	}
	return nil
}

func (a *App) createAdminUser(w http.ResponseWriter, r *http.Request) {
	var in adminUserInput
	if decode(r, &in) != nil || validateAdminUserInput(&in, true) != nil {
		problem(w, 400, "invalid_user", "Subject, username, status, and roles must be valid")
		return
	}
	id, err := randomToken(18)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	if err = a.writeAdminUser(r.Context(), id, in, true); err != nil {
		problem(w, 409, "user_create_failed", "Subject must be unique and roles must be valid")
		return
	}
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "user.create", "user", id, "success", map[string]any{"roles": in.Roles, "status": in.Status})
	jsonOut(w, 201, map[string]any{"id": id})
}

func (a *App) updateAdminUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in adminUserInput
	if id == "" || decode(r, &in) != nil || validateAdminUserInput(&in, false) != nil {
		problem(w, 400, "invalid_user", "Username, status, and roles must be valid")
		return
	}
	p, _ := auth.FromContext(r.Context())
	if id == p.UserID && (in.Status != "active" || !slices.Contains(in.Roles, "platform-admin")) {
		problem(w, 409, "self_lockout", "You cannot disable yourself or remove your own platform-admin role")
		return
	}
	if err := a.writeAdminUser(r.Context(), id, in, false); err != nil {
		problem(w, 404, "user_not_found", "User was not found")
		return
	}
	a.audit(r, p, "user.update", "user", id, "success", map[string]any{"roles": in.Roles, "status": in.Status})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) writeAdminUser(ctx context.Context, id string, in adminUserInput, create bool) error {
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO users(id,subject,username,email,status,roles_managed) VALUES(?,?,?,?,?,1)`), id, in.Subject, in.Username, in.Email, in.Status)
	} else {
		result, updateErr := tx.ExecContext(ctx, a.store.Rebind(`UPDATE users SET username=?,email=?,status=?,roles_managed=1 WHERE id=? AND id NOT IN ('bootstrap-admin','break-glass-admin')`), in.Username, in.Email, in.Status, id)
		err = updateErr
		if err == nil {
			if affected, _ := result.RowsAffected(); affected == 0 {
				return sql.ErrNoRows
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM user_roles WHERE user_id=?`), id); err != nil {
		return err
	}
	for _, role := range in.Roles {
		if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_roles(user_id,role_code) VALUES(?,?)`), id, role); err != nil {
			return err
		}
	}
	if in.Status != "active" {
		_, _ = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM user_sessions WHERE user_id=?`), id)
		_, _ = tx.ExecContext(ctx, a.store.Rebind(`UPDATE api_keys SET revoked_at=CURRENT_TIMESTAMP WHERE user_id=? AND revoked_at IS NULL`), id)
	}
	return tx.Commit()
}

func (a *App) deleteAdminUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := auth.FromContext(r.Context())
	if id == "" || id == p.UserID {
		problem(w, 409, "self_lockout", "You cannot delete your own administrator account")
		return
	}
	tx, err := a.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), a.store.Rebind(`UPDATE users SET status='deleted' WHERE id=? AND id NOT IN ('bootstrap-admin','break-glass-admin')`), id)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		problem(w, 404, "user_not_found", "User was not found")
		return
	}
	_, _ = tx.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM user_sessions WHERE user_id=?`), id)
	_, _ = tx.ExecContext(r.Context(), a.store.Rebind(`UPDATE api_keys SET revoked_at=CURRENT_TIMESTAMP WHERE user_id=? AND revoked_at IS NULL`), id)
	if err = tx.Commit(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	a.audit(r, p, "user.delete", "user", id, "success", nil)
	w.WriteHeader(http.StatusNoContent)
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
	a.audit(r, p, "api_key.admin_revoke", "api_key", r.PathValue("id"), "success", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) adminUpdateKeyScopes(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Scopes []string `json:"scopes"`
	}
	if decode(r, &in) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "scopes are required")
		return
	}
	if !keyScopesAllowed(p, in.Scopes) {
		problem(w, http.StatusForbidden, "forbidden_scope", "The current administrator role cannot grant the requested MCP management tool")
		return
	}
	if err := a.keys.UpdateScopesAdmin(r.Context(), r.PathValue("id"), in.Scopes); err != nil {
		problem(w, http.StatusBadRequest, "scope_update_failed", err.Error())
		return
	}
	a.audit(r, p, "api_key.admin_scopes_update", "api_key", r.PathValue("id"), "success", map[string]any{"scopes": in.Scopes})
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) listManagedSecrets(w http.ResponseWriter, r *http.Request) {
	items, err := a.secrets.List(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, items)
}
func (a *App) putManagedSecret(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Name, Backend, Value string
	}
	if decode(r, &in) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "name, backend, and value are required")
		return
	}
	if pathName := r.PathValue("name"); pathName != "" {
		in.Name = pathName
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	item, err := a.secrets.Put(ctx, in.Name, in.Backend, in.Value, p.UserID, "")
	if err != nil {
		a.audit(r, p, "secret.write", "managed_secret", in.Name, "failure", map[string]any{"backend": in.Backend, "error": truncateText(err.Error(), 300)})
		problem(w, http.StatusBadRequest, "secret_write_failed", err.Error())
		return
	}
	a.audit(r, p, "secret.write", "managed_secret", in.Name, "success", map[string]any{"backend": item.Backend, "version": item.Version})
	jsonOut(w, http.StatusCreated, item)
}
func (a *App) disableManagedSecret(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	name := r.PathValue("name")
	if err := a.secrets.Disable(r.Context(), name, p.UserID); err != nil {
		problem(w, http.StatusNotFound, "secret_not_found", err.Error())
		return
	}
	a.audit(r, p, "secret.disable", "managed_secret", name, "success", nil)
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
func (a *App) notificationDeliveries(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT d.id,d.channel,d.status,d.attempts,d.next_attempt_at,d.last_error,d.delivered_at,d.created_at,n.notification_type,n.title,u.username
FROM notification_deliveries d
JOIN notifications n ON n.id=d.notification_id
JOIN users u ON u.id=n.user_id
ORDER BY d.created_at DESC LIMIT 500`)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, channel, status, lastError, notificationType, title, username string
		var attempts int
		var nextAttempt, createdAt time.Time
		var deliveredAt sql.NullTime
		if err = rows.Scan(&id, &channel, &status, &attempts, &nextAttempt, &lastError, &deliveredAt, &createdAt, &notificationType, &title, &username); err != nil {
			problem(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		item := map[string]any{
			"id": id, "channel": channel, "status": status, "attempts": attempts,
			"nextAttemptAt": nextAttempt, "lastError": lastError, "createdAt": createdAt,
			"notificationType": notificationType, "title": title, "username": username,
		}
		if deliveredAt.Valid {
			item["deliveredAt"] = deliveredAt.Time
		}
		out = append(out, item)
	}
	jsonOut(w, http.StatusOK, out)
}
func (a *App) retryNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id := r.PathValue("id")
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE notification_deliveries SET status='pending',next_attempt_at=?,last_error='',delivered_at=NULL,updated_at=? WHERE id=? AND status IN ('failed','dead')`), time.Now().UTC(), time.Now().UTC(), id)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		problem(w, http.StatusConflict, "delivery_not_retryable", "Only failed or dead notification deliveries can be retried")
		return
	}
	a.audit(r, p, "notification_delivery.retry", "notification_delivery", id, "success", nil)
	w.WriteHeader(http.StatusAccepted)
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
func (a *App) mcpTools(w http.ResponseWriter, r *http.Request) {
	catalog := mcp.Catalog()
	for _, tool := range catalog {
		name, _ := tool["name"].(string)
		var enabled, timeout, cache, budget int
		if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT enabled,timeout_ms,cache_seconds,max_response_bytes FROM mcp_tools WHERE name=?`), name).Scan(&enabled, &timeout, &cache, &budget); err == nil {
			tool["enabled"] = enabled == 1
			tool["timeoutMs"] = timeout
			tool["cacheSeconds"] = cache
			// 0 means the server default, which the screen shows explicitly.
			tool["maxResponseBytes"] = budget
			tool["effectiveResponseBytes"] = budget
			if budget <= 0 {
				tool["effectiveResponseBytes"] = mcp.DefaultResponseBytes
			}
		}
	}
	jsonOut(w, 200, catalog)
}
func (a *App) updateMCPTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	found := false
	for _, tool := range mcp.Catalog() {
		if tool["name"] == name {
			found = true
			break
		}
	}
	if !found {
		problem(w, 404, "not_found", "MCP tool not found")
		return
	}
	var in struct {
		Enabled          bool `json:"enabled"`
		TimeoutMS        int  `json:"timeoutMs"`
		CacheSeconds     int  `json:"cacheSeconds"`
		MaxResponseBytes int  `json:"maxResponseBytes"`
	}
	if decode(r, &in) != nil || in.TimeoutMS < 100 || in.TimeoutMS > 120000 || in.CacheSeconds < 0 || in.CacheSeconds > 86400 {
		problem(w, 400, "invalid_request", "timeoutMs must be 100..120000 and cacheSeconds 0..86400")
		return
	}
	// 0 keeps the server default; anything else has to be a budget an answer can
	// actually fit in.
	if in.MaxResponseBytes != 0 && (in.MaxResponseBytes < mcp.MinResponseBytes || in.MaxResponseBytes > mcp.MaxResponseBytes) {
		problem(w, 400, "invalid_request", fmt.Sprintf("maxResponseBytes must be 0 for the default, or %d..%d", mcp.MinResponseBytes, mcp.MaxResponseBytes))
		return
	}
	p, _ := auth.FromContext(r.Context())
	_, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE mcp_tools SET enabled=?,timeout_ms=?,cache_seconds=?,max_response_bytes=?,updated_by=?,updated_at=? WHERE name=?`), boolInt(in.Enabled), in.TimeoutMS, in.CacheSeconds, in.MaxResponseBytes, p.UserID, time.Now().UTC(), name)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	a.audit(r, p, "mcp_tool.update", "mcp_tool", name, "success", map[string]any{"enabled": in.Enabled, "timeoutMs": in.TimeoutMS, "cacheSeconds": in.CacheSeconds, "maxResponseBytes": in.MaxResponseBytes})
	jsonOut(w, 200, map[string]any{"name": name, "enabled": in.Enabled, "timeoutMs": in.TimeoutMS, "cacheSeconds": in.CacheSeconds, "maxResponseBytes": in.MaxResponseBytes})
}

// mcpWindow maps the screen's window selector to a duration and a bucket size.
// Only three windows exist on purpose: an operator tuning a tool wants today,
// this week or this month, and every one of them has to stay a cheap query.
func mcpWindow(value string) (time.Duration, string, string) {
	switch value {
	case "24h":
		return 24 * time.Hour, "24h", "hour"
	case "30d":
		return 30 * 24 * time.Hour, "30d", "day"
	default:
		return 7 * 24 * time.Hour, "7d", "day"
	}
}

// bucketExpression renders a timestamp column as a bucket label. The two
// supported databases spell this differently and neither accepts the other's
// form, so the driver decides.
func (a *App) bucketExpression(column, unit string) string {
	if a.store.Driver() == "postgres" {
		return fmt.Sprintf(`to_char(date_trunc('%s', %s), 'YYYY-MM-DD"T"HH24:00:00Z')`, unit, column)
	}
	if unit == "hour" {
		return fmt.Sprintf(`strftime('%%Y-%%m-%%dT%%H:00:00Z', %s)`, column)
	}
	return fmt.Sprintf(`strftime('%%Y-%%m-%%dT00:00:00Z', %s)`, column)
}

// mcpToolStats is one row of the analytics table.
type mcpToolStats struct {
	Tool             string  `json:"tool"`
	Calls            int64   `json:"calls"`
	Success          int64   `json:"success"`
	Empty            int64   `json:"empty"`
	Errors           int64   `json:"errors"`
	CacheHits        int64   `json:"cacheHits"`
	Truncated        int64   `json:"truncated"`
	Users            int64   `json:"users"`
	AverageLatencyMS float64 `json:"averageLatencyMs"`
	P50LatencyMS     int64   `json:"p50LatencyMs"`
	P95LatencyMS     int64   `json:"p95LatencyMs"`
	MaxLatencyMS     int64   `json:"maxLatencyMs"`
	AverageBytes     float64 `json:"averageResponseBytes"`
	MaxBytes         int64   `json:"maximumResponseBytes"`
	AverageResults   float64 `json:"averageResultCount"`
	TimeoutMS        int     `json:"timeoutMs"`
	CacheSeconds     int     `json:"cacheSeconds"`
	BudgetBytes      int     `json:"responseBudgetBytes"`
}

// mcpAnalytics answers the questions an operator has about MCP traffic: which
// tools are used, which ones fail or come back empty, how long they take, how
// much context they spend, and what should be changed as a result. Every number
// is scoped to one window so a long-running deployment does not average away a
// problem that started yesterday.
func (a *App) mcpAnalytics(w http.ResponseWriter, r *http.Request) {
	duration, window, unit := mcpWindow(r.URL.Query().Get("window"))
	from := time.Now().UTC().Add(-duration)
	out := map[string]any{"window": window, "from": from, "generatedAt": time.Now().UTC()}

	var summary struct {
		Calls, Success, Empty, Errors, CacheHits, Truncated, Sessions int64
		AverageLatency, AverageBytes                                  float64
	}
	err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*),
COALESCE(SUM(CASE WHEN outcome='success' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(cache_hit),0), COALESCE(SUM(truncated),0),
COUNT(DISTINCT CASE WHEN session_id<>'' THEN session_id END),
COALESCE(AVG(duration_ms),0), COALESCE(AVG(response_bytes),0)
FROM mcp_calls WHERE occurred_at>=?`), from).Scan(&summary.Calls, &summary.Success, &summary.Empty, &summary.Errors,
		&summary.CacheHits, &summary.Truncated, &summary.Sessions, &summary.AverageLatency, &summary.AverageBytes)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	out["summary"] = map[string]any{
		"calls": summary.Calls, "success": summary.Success, "empty": summary.Empty, "errors": summary.Errors,
		"cacheHits": summary.CacheHits, "truncated": summary.Truncated, "sessions": summary.Sessions,
		"averageLatencyMs": summary.AverageLatency, "averageResponseBytes": summary.AverageBytes,
	}

	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT tool, COUNT(*),
COALESCE(SUM(CASE WHEN outcome='success' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(cache_hit),0), COALESCE(SUM(truncated),0), COUNT(DISTINCT user_id),
COALESCE(AVG(duration_ms),0), COALESCE(MAX(duration_ms),0),
COALESCE(AVG(response_bytes),0), COALESCE(MAX(response_bytes),0), COALESCE(AVG(result_count),0)
FROM mcp_calls WHERE occurred_at>=? GROUP BY tool ORDER BY COUNT(*) DESC`), from)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	var tools []mcpToolStats
	for rows.Next() {
		var item mcpToolStats
		var maxLatency float64
		if err = rows.Scan(&item.Tool, &item.Calls, &item.Success, &item.Empty, &item.Errors, &item.CacheHits,
			&item.Truncated, &item.Users, &item.AverageLatencyMS, &maxLatency, &item.AverageBytes, &item.MaxBytes, &item.AverageResults); err != nil {
			rows.Close()
			problem(w, 500, "internal_error", err.Error())
			return
		}
		item.MaxLatencyMS = int64(maxLatency)
		tools = append(tools, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	// Percentiles are read with an offset per tool rather than computed over the
	// whole table: the index on (tool, occurred_at) keeps each one small, and
	// neither SQLite nor an older PostgreSQL needs a window function for it.
	for index := range tools {
		tools[index].P50LatencyMS = a.latencyPercentile(r.Context(), tools[index].Tool, from, tools[index].Calls, 0.50)
		tools[index].P95LatencyMS = a.latencyPercentile(r.Context(), tools[index].Tool, from, tools[index].Calls, 0.95)
		// A tool that is no longer in the catalog still has calls in the window, so
		// the effective defaults are filled in before the lookup rather than left
		// at zero, which would produce a "raise the budget to 0" recommendation.
		tools[index].TimeoutMS, tools[index].BudgetBytes = 30000, mcp.DefaultResponseBytes
		var timeout, cache, budget int
		if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT timeout_ms,cache_seconds,max_response_bytes FROM mcp_tools WHERE name=?`), tools[index].Tool).Scan(&timeout, &cache, &budget); err == nil {
			tools[index].TimeoutMS, tools[index].CacheSeconds = timeout, cache
			if budget > 0 {
				tools[index].BudgetBytes = budget
			}
		}
	}
	out["tools"] = tools
	out["recommendations"] = mcpRecommendations(tools)

	group := func(key string, statement string) {
		list, err := a.groupedCounts(r.Context(), statement, from)
		if err == nil {
			out[key] = list
		}
	}
	group("clients", `SELECT CASE WHEN client_name='' THEN 'unknown' ELSE client_name || ' ' || client_version END, COUNT(*) FROM mcp_calls WHERE occurred_at>=? GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)
	group("libraries", `SELECT library_id, COUNT(*) FROM mcp_calls WHERE occurred_at>=? AND library_id<>'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)
	group("retrieval", `SELECT CASE WHEN retrieval_mode='' THEN 'unknown' ELSE retrieval_mode END, COUNT(*) FROM mcp_calls WHERE occurred_at>=? AND outcome<>'error' GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)
	group("errors", `SELECT CASE WHEN error_code='' THEN 'unknown' ELSE error_code END, COUNT(*) FROM mcp_calls WHERE occurred_at>=? AND outcome='error' GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)

	// The questions that came back empty are the most actionable list on this
	// screen: each one is a repository that is not indexed, an ACL that is too
	// narrow, or a tool description that misleads the agent.
	emptyRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT tool, arguments_preview, COUNT(*) FROM mcp_calls
WHERE occurred_at>=? AND outcome='empty' AND arguments_preview<>'' GROUP BY tool, arguments_hash, arguments_preview ORDER BY COUNT(*) DESC LIMIT 15`), from)
	if err == nil {
		var unanswered []map[string]any
		for emptyRows.Next() {
			var tool, preview string
			var count int64
			if emptyRows.Scan(&tool, &preview, &count) == nil {
				unanswered = append(unanswered, map[string]any{"tool": tool, "arguments": preview, "calls": count})
			}
		}
		emptyRows.Close()
		out["unanswered"] = unanswered
	}

	bucket := a.bucketExpression("occurred_at", unit)
	timelineRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(fmt.Sprintf(`SELECT %s AS bucket, COUNT(*),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0)
FROM mcp_calls WHERE occurred_at>=? GROUP BY bucket ORDER BY bucket`, bucket)), from)
	if err == nil {
		var timeline []map[string]any
		for timelineRows.Next() {
			var label string
			var calls, errorCount, emptyCount int64
			if timelineRows.Scan(&label, &calls, &errorCount, &emptyCount) == nil {
				timeline = append(timeline, map[string]any{"bucket": label, "calls": calls, "errors": errorCount, "empty": emptyCount})
			}
		}
		timelineRows.Close()
		out["timeline"] = timeline
	}
	jsonOut(w, 200, out)
}

// latencyPercentile reads one ordered row instead of sorting in the process.
func (a *App) latencyPercentile(ctx context.Context, tool string, from time.Time, calls int64, quantile float64) int64 {
	if calls <= 0 {
		return 0
	}
	offset := int64(float64(calls-1) * quantile)
	var value int64
	if err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT duration_ms FROM mcp_calls WHERE tool=? AND occurred_at>=? ORDER BY duration_ms LIMIT 1 OFFSET ?`), tool, from, offset).Scan(&value); err != nil {
		return 0
	}
	return value
}

func (a *App) groupedCounts(ctx context.Context, statement string, from time.Time) ([]map[string]any, error) {
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(statement), from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var label string
		var count int64
		if err = rows.Scan(&label, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"label": label, "calls": count})
	}
	return out, rows.Err()
}

// mcpRecommendations turns the measurements into the change an operator should
// make. Each one carries the field and value the settings form would set, so
// the screen can offer to apply it instead of asking the reader to do the
// arithmetic.
func mcpRecommendations(tools []mcpToolStats) []map[string]any {
	out := []map[string]any{}
	add := func(tool, severity, message, field string, value int) {
		item := map[string]any{"tool": tool, "severity": severity, "message": message}
		if field != "" {
			item["field"], item["value"] = field, value
		}
		out = append(out, item)
	}
	for _, item := range tools {
		if item.Calls < 5 {
			continue
		}
		share := func(count int64) float64 { return float64(count) / float64(item.Calls) }
		if rate := share(item.Truncated); rate >= 0.2 {
			suggested := min(item.BudgetBytes*2, mcp.MaxResponseBytes)
			add(item.Tool, "warning", fmt.Sprintf("응답의 %.0f%%가 예산(%d KB)에서 잘렸습니다. 에이전트가 같은 질문을 반복하게 됩니다.", rate*100, item.BudgetBytes/1024),
				"maxResponseBytes", suggested)
		}
		if item.TimeoutMS > 0 && item.P95LatencyMS > int64(float64(item.TimeoutMS)*0.8) {
			suggested := min(item.TimeoutMS*2, 120000)
			add(item.Tool, "warning", fmt.Sprintf("p95 지연 %d ms가 Timeout %d ms에 근접합니다. 타임아웃 직전 응답은 결과가 잘린 채 반환됩니다.", item.P95LatencyMS, item.TimeoutMS),
				"timeoutMs", suggested)
		}
		if rate := share(item.Empty); rate >= 0.4 {
			add(item.Tool, "critical", fmt.Sprintf("빈 응답 비율 %.0f%%. 색인 진단과 저장소 ACL을 먼저 확인하세요. 아래 '답하지 못한 질문' 목록이 원인을 좁혀 줍니다.", rate*100), "", 0)
		}
		if rate := share(item.Errors); rate >= 0.1 {
			add(item.Tool, "critical", fmt.Sprintf("오류율 %.0f%%. 오류 코드 분포에서 timeout과 forbidden을 구분해 대응하세요.", rate*100), "", 0)
		}
		if item.CacheSeconds == 0 && item.Calls >= 50 && item.AverageLatencyMS > 1000 {
			add(item.Tool, "info", fmt.Sprintf("캐시가 꺼져 있고 평균 %.0f ms가 걸립니다. 짧은 캐시만으로 반복 호출을 크게 줄일 수 있습니다.", item.AverageLatencyMS),
				"cacheSeconds", 30)
		}
	}
	return out
}

// mcpCalls is the audit view of individual calls. It is deliberately separate
// from the aggregate screen: aggregates tune the platform, this reconstructs
// what one credential or one session actually did.
func (a *App) mcpCalls(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	duration, window, _ := mcpWindow(query.Get("window"))
	from := time.Now().UTC().Add(-duration)
	where := []string{"occurred_at>=?"}
	args := []any{from}
	// Fixed order: a map would build a differently ordered statement on every
	// request, which defeats the driver's prepared-statement cache and makes the
	// slow query log unreadable.
	for _, filter := range []struct{ column, value string }{
		{"tool", query.Get("tool")}, {"outcome", query.Get("outcome")}, {"user_id", query.Get("user")},
		{"api_key_prefix", query.Get("keyPrefix")}, {"session_id", query.Get("session")}, {"error_code", query.Get("errorCode")},
	} {
		if filter.value != "" {
			where = append(where, filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	if search := strings.TrimSpace(query.Get("q")); search != "" {
		where = append(where, "(arguments_preview LIKE ? OR library_id LIKE ?)")
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	condition := strings.Join(where, " AND ")
	limit := 100
	if value, err := strconv.Atoi(query.Get("limit")); err == nil && value > 0 && value <= 1000 {
		limit = value
	}
	offset := 0
	if value, err := strconv.Atoi(query.Get("offset")); err == nil && value > 0 {
		offset = value
	}
	var total int64
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM mcp_calls WHERE `+condition), args...).Scan(&total); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	statement := `SELECT id,occurred_at,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,result_count,cache_hit,error_code,retrieval_mode,trace_summary
FROM mcp_calls WHERE ` + condition + ` ORDER BY occurred_at DESC LIMIT ? OFFSET ?`
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(statement), append(args, limit, offset)...)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var at time.Time
		var id, user, prefix, tool, libraryID, outcome, ip, session, requestID, clientName, clientVersion, preview, code, mode, summary string
		var latency, bytes, results int64
		var truncated, cacheHit int
		if err = rows.Scan(&id, &at, &user, &prefix, &tool, &libraryID, &outcome, &latency, &ip, &bytes, &truncated, &session, &requestID,
			&clientName, &clientVersion, &preview, &results, &cacheHit, &code, &mode, &summary); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		items = append(items, map[string]any{
			"id": id, "traceSummary": summary,
			"occurredAt": at, "userId": user, "apiKeyPrefix": prefix, "tool": tool, "libraryId": libraryID,
			"outcome": outcome, "durationMs": latency, "clientIp": ip, "responseBytes": bytes, "truncated": truncated == 1,
			"sessionId": session, "requestId": requestID, "client": strings.TrimSpace(clientName + " " + clientVersion),
			"arguments": preview, "resultCount": results, "cacheHit": cacheHit == 1, "errorCode": code, "retrievalMode": mode,
		})
	}
	if query.Get("format") == "csv" {
		p, _ := auth.FromContext(r.Context())
		a.audit(r, p, "mcp_calls.export", "mcp_calls", window, "success", map[string]any{"rows": len(items)})
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"mcp-calls-%s.csv\"", window))
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"call_id", "occurred_at", "user_id", "api_key_prefix", "client", "session_id", "request_id", "tool", "library_id", "outcome", "error_code", "retrieval_mode", "duration_ms", "response_bytes", "truncated", "result_count", "cache_hit", "client_ip", "arguments", "trace_summary"})
		for _, item := range items {
			_ = writer.Write([]string{
				item["id"].(string), item["occurredAt"].(time.Time).Format(time.RFC3339), item["userId"].(string), item["apiKeyPrefix"].(string),
				item["client"].(string), item["sessionId"].(string), item["requestId"].(string), item["tool"].(string),
				item["libraryId"].(string), item["outcome"].(string), item["errorCode"].(string), item["retrievalMode"].(string),
				fmt.Sprint(item["durationMs"]), fmt.Sprint(item["responseBytes"]), fmt.Sprint(item["truncated"]),
				fmt.Sprint(item["resultCount"]), fmt.Sprint(item["cacheHit"]), item["clientIp"].(string), item["arguments"].(string),
				item["traceSummary"].(string),
			})
		}
		writer.Flush()
		return
	}
	jsonOut(w, 200, map[string]any{"window": window, "total": total, "limit": limit, "offset": offset, "items": items})
}

// mcpCallTrace is the X-ray of one call: the stage-by-stage record of what ran,
// what each stage looked at and what it passed on, plus the neighbouring calls
// of the same session so the sequence the agent followed is visible. Reading a
// result as good or bad is only possible with both.
func (a *App) mcpCallTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := auth.FromContext(r.Context())
	// A caller reaching this through the personal route may only open their own
	// calls; the administrative route is already role-checked.
	ownOnly := strings.HasPrefix(r.URL.Path, "/api/v1/me/")
	statement := `SELECT id,occurred_at,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,result_count,cache_hit,error_code,retrieval_mode,trace_summary FROM mcp_calls WHERE id=?`
	args := []any{id}
	if ownOnly {
		statement += ` AND user_id=?`
		args = append(args, p.UserID)
	}
	var at time.Time
	var callID, user, prefix, tool, libraryID, outcome, ip, session, requestID, clientName, clientVersion, preview, code, mode, summary string
	var latency, bytes, results int64
	var truncated, cacheHit int
	err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(statement), args...).Scan(&callID, &at, &user, &prefix, &tool, &libraryID, &outcome,
		&latency, &ip, &bytes, &truncated, &session, &requestID, &clientName, &clientVersion, &preview, &results, &cacheHit, &code, &mode, &summary)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "not_found", "MCP call not found")
		return
	}
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	call := map[string]any{
		"id": callID, "occurredAt": at, "userId": user, "apiKeyPrefix": prefix, "tool": tool, "libraryId": libraryID,
		"outcome": outcome, "durationMs": latency, "clientIp": ip, "responseBytes": bytes, "truncated": truncated == 1,
		"sessionId": session, "requestId": requestID, "client": strings.TrimSpace(clientName + " " + clientVersion),
		"arguments": preview, "resultCount": results, "cacheHit": cacheHit == 1, "errorCode": code,
		"retrievalMode": mode, "traceSummary": summary,
	}
	steps := []map[string]any{}
	stepRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT sequence,stage,target,status,detail,candidates,results,duration_ms,offset_ms FROM mcp_call_steps WHERE call_id=? ORDER BY sequence`), callID)
	if err == nil {
		for stepRows.Next() {
			var sequence, candidates, stepResults, duration, offset int64
			var stage, target, status, detail string
			if stepRows.Scan(&sequence, &stage, &target, &status, &detail, &candidates, &stepResults, &duration, &offset) == nil {
				steps = append(steps, map[string]any{"sequence": sequence, "stage": stage, "target": target, "status": status,
					"detail": detail, "candidates": candidates, "results": stepResults, "durationMs": duration, "offsetMs": offset})
			}
		}
		stepRows.Close()
	}
	// Stages do not cover the whole call: connection handling, formatting and the
	// response budget run outside them. Reporting the remainder keeps the
	// waterfall honest instead of implying the stages add up to the total.
	var traced int64
	for _, step := range steps {
		traced += step["durationMs"].(int64)
	}
	call["tracedMs"], call["untracedMs"] = traced, max(latency-traced, 0)

	sequence := []map[string]any{}
	if session != "" {
		sequenceStatement := `SELECT id,occurred_at,tool,outcome,result_count,duration_ms,error_code,retrieval_mode,arguments_preview FROM mcp_calls WHERE session_id=?`
		sequenceArgs := []any{session}
		if ownOnly {
			sequenceStatement += ` AND user_id=?`
			sequenceArgs = append(sequenceArgs, p.UserID)
		}
		sequenceStatement += ` ORDER BY occurred_at LIMIT 200`
		sequenceRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(sequenceStatement), sequenceArgs...)
		if err == nil {
			for sequenceRows.Next() {
				var rowID, rowTool, rowOutcome, rowCode, rowMode, rowArgs string
				var rowAt time.Time
				var rowResults, rowDuration int64
				if sequenceRows.Scan(&rowID, &rowAt, &rowTool, &rowOutcome, &rowResults, &rowDuration, &rowCode, &rowMode, &rowArgs) == nil {
					sequence = append(sequence, map[string]any{"id": rowID, "occurredAt": rowAt, "tool": rowTool, "outcome": rowOutcome,
						"resultCount": rowResults, "durationMs": rowDuration, "errorCode": rowCode, "retrievalMode": rowMode,
						"arguments": rowArgs, "current": rowID == callID})
				}
			}
			sequenceRows.Close()
		}
	}
	jsonOut(w, 200, map[string]any{"call": call, "steps": steps, "sessionSequence": sequence})
}

// mcpSessions groups the calls of one agent conversation. A single call rarely
// tells the whole story: an agent that searched, got nothing, searched again
// with different words and gave up looks fine call by call and is a failure as
// a conversation. Sessions that ended without an answer are ranked first
// because they are the ones worth reading.
func (a *App) mcpSessions(w http.ResponseWriter, r *http.Request) {
	duration, window, _ := mcpWindow(r.URL.Query().Get("window"))
	from := time.Now().UTC().Add(-duration)
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT session_id,
MIN(occurred_at), MAX(occurred_at), COUNT(*),
COALESCE(SUM(CASE WHEN outcome='success' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(duration_ms),0), COALESCE(SUM(response_bytes),0),
MIN(user_id), MIN(client_name), MIN(client_version)
FROM mcp_calls WHERE occurred_at>=? AND session_id<>'' GROUP BY session_id ORDER BY MAX(occurred_at) DESC LIMIT 100`), from)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	type sessionRow struct {
		id, user, client                  string
		first, last                       time.Time
		calls, success, empty, errorCount int64
		durationMS, bytes                 int64
	}
	var sessions []sessionRow
	for rows.Next() {
		var item sessionRow
		var clientName, clientVersion string
		// MIN and MAX over a timestamp lose the column type that lets the SQLite
		// driver hand back a time.Time, so both are scanned loosely and converted.
		var first, last any
		if err = rows.Scan(&item.id, &first, &last, &item.calls, &item.success, &item.empty, &item.errorCount,
			&item.durationMS, &item.bytes, &item.user, &clientName, &clientVersion); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		item.first, item.last = scanTime(first), scanTime(last)
		item.client = strings.TrimSpace(clientName + " " + clientVersion)
		sessions = append(sessions, item)
	}
	if err = rows.Err(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, item := range sessions {
		// The tool chain is what the conversation actually did, and the outcome of
		// the last call is how it ended.
		chainRows, chainErr := a.store.DB.QueryContext(r.Context(), a.store.Rebind(
			`SELECT id,tool,outcome FROM mcp_calls WHERE session_id=? AND occurred_at>=? ORDER BY occurred_at LIMIT 50`), item.id, from)
		chain := []string{}
		lastOutcome, lastCallID := "", ""
		if chainErr == nil {
			for chainRows.Next() {
				var callID, tool, outcome string
				if chainRows.Scan(&callID, &tool, &outcome) == nil {
					chain = append(chain, tool)
					lastOutcome, lastCallID = outcome, callID
				}
			}
			chainRows.Close()
		}
		out = append(out, map[string]any{
			"sessionId": item.id, "userId": item.user, "client": item.client,
			"firstCallAt": item.first, "lastCallAt": item.last, "calls": item.calls,
			"success": item.success, "empty": item.empty, "errors": item.errorCount,
			"durationMs": item.durationMS, "responseBytes": item.bytes,
			"toolChain": chain, "lastOutcome": lastOutcome, "lastCallId": lastCallID,
			// A conversation that ended on an empty or failed call is one where the
			// agent walked away without an answer.
			"unresolved": lastOutcome == "empty" || lastOutcome == "error",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i]["unresolved"].(bool), out[j]["unresolved"].(bool)
		if left != right {
			return left
		}
		return out[i]["lastCallAt"].(time.Time).After(out[j]["lastCallAt"].(time.Time))
	})
	jsonOut(w, 200, map[string]any{"window": window, "sessions": out})
}

// mcpSelfCheck runs the retrieval path end to end as the calling administrator
// and returns the same stage trace an MCP call would produce.
//
// "설정은 저장됐는데 검색이 되는가"는 설정 화면에서 답할 수 없던 질문이었습니다.
// This runs the real service with the caller's own principals, so it proves the
// ACL mapping, the index, the source connectors and the embedding path together
// rather than testing each one in isolation.
func (a *App) mcpSelfCheck(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	principals := searchPrincipals(p)
	var in struct {
		Query   string `json:"query"`
		Pattern string `json:"pattern"`
	}
	_ = decode(r, &in)
	if strings.TrimSpace(in.Query) == "" {
		in.Query = "README"
	}
	if strings.TrimSpace(in.Pattern) == "" {
		in.Pattern = "README*"
	}
	started := time.Now()
	type check struct {
		Name    string            `json:"name"`
		Status  string            `json:"status"`
		Detail  string            `json:"detail"`
		Action  string            `json:"action"`
		Steps   []calltrace.Step  `json:"steps"`
		Elapsed int64             `json:"durationMs"`
		Extra   map[string]string `json:"extra,omitempty"`
	}
	checks := []check{}
	// A check runs one retrieval call under its own recorder, so the caller sees
	// the same stage breakdown the MCP X-ray shows.
	run := func(name string, fn func(ctx context.Context) (int, error)) {
		ctx, recorder := calltrace.New(r.Context())
		at := time.Now()
		found, err := fn(ctx)
		item := check{Name: name, Steps: recorder.Steps(), Elapsed: time.Since(at).Milliseconds()}
		switch {
		case err != nil:
			item.Status, item.Detail = "fail", err.Error()
			item.Action = "오류 메시지의 단계를 X-ray에서 확인하고 해당 연동 설정을 점검하세요."
		case found == 0:
			item.Status = "warn"
			item.Detail = "결과가 없습니다."
			if summary := recorder.Summary(); summary != "" {
				item.Detail += " " + summary
			}
			item.Action = "색인 진단과 저장소 ACL을 확인하세요. 단계별 후보 수가 0이면 권한, 후보는 있는데 통과가 0이면 색인 내용 문제입니다."
		default:
			item.Status, item.Detail = "ok", fmt.Sprintf("%d건", found)
		}
		checks = append(checks, item)
	}

	var accessible int
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM repositories WHERE enabled=1`)).Scan(&accessible); err == nil {
		status, detail, action := "ok", fmt.Sprintf("등록 저장소 %d개", accessible), ""
		if accessible == 0 {
			status, detail = "fail", "등록된 저장소가 없습니다."
			action = "소스·색인 화면에서 저장소를 탐색·등록하세요."
		}
		checks = append(checks, check{Name: "저장소 카탈로그", Status: status, Detail: detail, Action: action})
	}
	if len(principals) == 0 {
		checks = append(checks, check{Name: "ACL 주체", Status: "fail",
			Detail: "이 계정에 매핑된 소스 주체가 없습니다.",
			Action: "Keycloak 매핑에서 bitbucket_user_slug 또는 gitlab_user_id 클레임을 연결하세요."})
	} else {
		scope := strings.Join(principals, ", ")
		if search.Unrestricted(principals) {
			scope = "관리자 역할 - ACL 우회"
		}
		checks = append(checks, check{Name: "ACL 주체", Status: "ok", Detail: scope})
	}

	run("코드 검색 (search-code)", func(ctx context.Context) (int, error) {
		result, err := a.search.SearchCode(ctx, principals, in.Query, "", "", "", "", 5)
		return len(result.Repositories) + len(result.Hits), err
	})
	run("파일명 검색 (find-file)", func(ctx context.Context) (int, error) {
		result, err := a.search.FindFiles(ctx, principals, in.Pattern, "", "", "", "", "", 5)
		return len(result.Files), err
	})
	run("의미 검색 (search-semantic)", func(ctx context.Context) (int, error) {
		result, err := a.search.SemanticSearch(ctx, principals, in.Query, "", "", 5)
		return len(result.Hits), err
	})

	verdict := "ok"
	for _, item := range checks {
		if item.Status == "fail" {
			verdict = "fail"
			break
		}
		if item.Status == "warn" {
			verdict = "warn"
		}
	}
	a.audit(r, p, "mcp.selfcheck", "mcp", verdict, "success", map[string]any{"query": in.Query})
	jsonOut(w, 200, map[string]any{"verdict": verdict, "durationMs": time.Since(started).Milliseconds(),
		"query": in.Query, "pattern": in.Pattern, "checks": checks})
}

// scanTime converts a loosely scanned timestamp. An aggregate such as
// MIN(occurred_at) comes back as a string from SQLite and as a time.Time from
// PostgreSQL, and the layouts differ between drivers, so every form the two
// produce is accepted rather than failing the whole query on one of them.
func scanTime(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC()
	case []byte:
		return parseTimestamp(string(typed))
	case string:
		return parseTimestamp(typed)
	default:
		return time.Time{}
	}
}

func parseTimestamp(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
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

// Allow and Report implement search.BreakerRegistry. Keeping the registry on
// the App means the administration screen and the MCP status tool read the same
// state the search path is acting on.
func (a *App) Allow(sourceType string) (bool, string) {
	return a.breakers.Get(sourceType).Allow()
}

func (a *App) Report(sourceType string, err error) {
	breaker := a.breakers.Get(sourceType)
	if err == nil {
		breaker.Success()
		return
	}
	breaker.Failure(err)
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

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// truncateText cuts on a rune boundary; a byte cut would corrupt Korean text,
// which is most of what this platform stores.
func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
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
func supportedSourceType(value string) bool {
	switch value {
	case "bitbucket", "gitlab", "confluence", "jira":
		return true
	default:
		return false
	}
}
func stringArrayValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	case string:
		return splitCSV(typed)
	default:
		return nil
	}
}
func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

// searchPrincipals returns the ACL principals used for catalog and source
// searches. Platform, source and search administrators operate the catalog, so
// they search every registered repository even without a Bitbucket or GitLab
// account; all other callers stay fail-closed on the repository ACL.
func searchPrincipals(p auth.Principal) []string {
	return search.WithUnrestricted(aclPrincipals(p.ACLPrincipal, p.ACLPrincipals), p.Roles)
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
func sourceACLPrincipals(bitbucketSlug, gitlabID string, groups []string) []string {
	var out []string
	if bitbucketSlug != "" {
		out = append(out, bitbucketSlug, "bitbucket:licensed")
	}
	if gitlabID != "" {
		out = append(out, "gitlab:"+gitlabID, "gitlab:authenticated")
	}
	for _, group := range groups {
		if group != "" && !stringContains(out, group) {
			out = append(out, group)
		}
	}
	return out
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
	jsonOut(w, 200, map[string]any{"status": "ok", "version": version.Version, "build": version.Full(), "database": "ok", "repositories": repositories, "chunks": chunks, "indexJobs": map[string]int64{"pending": pending, "failed": failed}, "notificationDeliveries": map[string]int64{"pending": notificationPending, "failed": notificationFailed, "dead": notificationDead}, "activeApiKeys": activeKeys, "activeManagedSecrets": activeSecrets, "observability": map[string]bool{"tracingEnabled": a.traces.Enabled()}, "go": map[string]any{"goroutines": runtime.NumGoroutine(), "allocatedBytes": memory.Alloc}})
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

// vectorStatus reports whether the configured vector database is reachable and
// how many vectors it holds compared with the metadata database.
func (a *App) vectorStatus(w http.ResponseWriter, r *http.Request) {
	var stored int64
	_ = a.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM document_chunks WHERE embedding IS NOT NULL`).Scan(&stored)
	store, err := a.vectorStore(r.Context())
	if errors.Is(err, vectorstore.ErrNotConfigured) {
		jsonOut(w, http.StatusOK, map[string]any{
			"configured": false, "provider": "none", "storedVectors": stored,
			"detail": "벡터 DB를 사용하지 않습니다. 임베딩은 메타 DB에 저장되어 애플리케이션에서 직접 채점합니다.",
		})
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
	response := map[string]any{
		"configured": true, "provider": status.Provider, "target": status.Target, "collection": status.Collection,
		"dimensions": status.Dimensions, "vectors": status.Vectors, "ready": status.Ready,
		"detail": status.Detail, "storedVectors": stored,
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

// jobStallWarning is when a running job starts looking stuck to an operator.
// The worker reclaims it slightly later, so the screen warns before that.
const jobStallWarning = 10 * time.Minute

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
	if r.Header.Get("X-Restore-Confirmation") != "RESTORE "+id {
		problem(w, 400, "restore_confirmation_required", "Exact restore confirmation is required")
		return
	}
	a.requestGate.Lock()
	a.stopBackground()
	err := a.backup.Restore(r.Context(), id)
	a.startBackground()
	a.requestGate.Unlock()
	if err != nil {
		a.audit(r, p, "backup.restore", "backup", id, "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 400, "backup_restore_failed", err.Error())
		return
	}
	a.audit(r, p, "backup.restore", "backup", id, "success", nil)
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
	settings, err := a.loadSettingMap(ctx, "keycloak")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return auth.OIDCConfig{}, errors.New("Keycloak is not configured")
		}
		return auth.OIDCConfig{}, err
	}
	// Auto-mode values are derived again at load time so a later ui.publicUrl,
	// Keycloak base URL or realm change is reflected by login immediately rather
	// than leaving a stale redirect or issuer in the active runtime config.
	if err = a.normalizeSetting(ctx, "keycloak", settings); err != nil {
		return auth.OIDCConfig{}, err
	}
	raw, err := json.Marshal(settings)
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
	_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO users(id,subject,username,email,status) VALUES(?,?,?,?,'active') ON CONFLICT(subject) DO UPDATE SET username=excluded.username,email=excluded.email`), userID, identity.Subject, identity.Username, identity.Email)
	if err != nil {
		return "", err
	}
	var status string
	var rolesManaged int
	if err = tx.QueryRowContext(ctx, a.store.Rebind(`SELECT id,status,roles_managed FROM users WHERE subject=?`), identity.Subject).Scan(&userID, &status, &rolesManaged); err != nil {
		return "", err
	}
	if status != "active" {
		return "", errUserDisabled
	}
	_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_identities(user_id,bitbucket_user_slug,gitlab_user_id,bitbucket_groups,mapping_source,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET bitbucket_user_slug=excluded.bitbucket_user_slug,gitlab_user_id=excluded.gitlab_user_id,bitbucket_groups=excluded.bitbucket_groups,mapping_source=excluded.mapping_source,updated_at=excluded.updated_at`), userID, identity.BitbucketUserSlug, identity.GitLabUserID, strings.Join(identity.ACLGroups, ","), "keycloak-claims", time.Now().UTC())
	if err != nil {
		return "", err
	}
	if rolesManaged == 0 {
		if _, err = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM user_roles WHERE user_id=?`), userID); err != nil {
			return "", err
		}
		for _, role := range identity.Roles {
			if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_roles(user_id,role_code) VALUES(?,?) ON CONFLICT(user_id,role_code) DO NOTHING`), userID, role); err != nil {
				return "", err
			}
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
		// API responses carry ACL-filtered source content and administrative
		// state. Keeping them out of the browser and proxy caches stops that data
		// from being replayed to the next user of a shared machine.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

// Unwrap lets net/http.ResponseController reach optional interfaces exposed by
// the original writer. Flush is kept explicitly for handlers (including MCP
// Streamable HTTP) that use the conventional http.Flusher type assertion.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
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

// revalidate prevents browsers and internal proxies from retaining UI files
// across an upgrade. The assets are small and the administration UI must never
// mix index.html from one release with app.js or roles.js from another.
// webRoot returns the UI file system. The assets are embedded in the binary so
// the screen can never be an older release than the program serving it, and a
// volume mounted over the application directory cannot hide it. Setting
// GIT_CTX_WEB_DIR points at a directory instead, which is how the UI is edited
// without rebuilding.
func webRoot() http.FileSystem {
	if directory := strings.TrimSpace(os.Getenv(webfs.Directory)); directory != "" {
		slog.Info("serving the web UI from disk", "directory", directory)
		return http.Dir(directory)
	}
	return http.FS(webfs.Assets)
}

func revalidate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
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

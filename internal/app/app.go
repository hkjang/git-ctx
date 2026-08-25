package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git-ctx/internal/apikey"
	"git-ctx/internal/auth"
	"git-ctx/internal/backup"
	"git-ctx/internal/config"
	"git-ctx/internal/embedding"
	"git-ctx/internal/indexer"
	runtimelogging "git-ctx/internal/logging"
	"git-ctx/internal/mcp"
	outboundnotification "git-ctx/internal/notification"
	"git-ctx/internal/observability"
	"git-ctx/internal/quality"
	"git-ctx/internal/rerank"
	"git-ctx/internal/scheduler"
	"git-ctx/internal/search"
	secretstore "git-ctx/internal/secret"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/webhook"
	"git-ctx/internal/worker"
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
	embeddingRuntime   *embedding.Runtime
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
	c.RecoveryKey = strings.TrimSpace(c.RecoveryKey)
	if c.RecoveryKey == "" {
		// Configs assembled by an embedding program (rather than FromEnv) get
		// an unguessable process-local key. Operational recovery intentionally
		// requires the explicit, stable key enforced by config.FromEnv.
		var err error
		c.RecoveryKey, err = randomToken(32)
		if err != nil {
			return nil, fmt.Errorf("generate recovery signing key: %w", err)
		}
	} else if len(c.RecoveryKey) < 32 {
		return nil, errors.New("recovery signing key must contain at least 32 characters")
	}
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
	a.embeddingRuntime = embedding.NewRuntime()
	a.search = search.New(s)
	a.search.SetConfigLoader(a.searchConfig)
	a.search.SetEmbeddingLoader(a.semanticEmbeddingProvider)
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
	a.search.SetFallbackReporter(a.embeddingRuntime.RecordFallback)
	a.search.SetKeywordLoader(a.openSearchCandidates)
	a.search.SetVectorLoader(a.vectorCandidates)
	a.search.SetGlobalVectorLoader(a.globalVectorCandidates)
	a.quality = quality.New(s, a.search)
	a.mcp = mcp.New(a.search, s)
	a.mcp.SetHealthLoader(func() []source.BreakerState { return a.breakers.States() })
	a.mcp.SetEmbeddingHealthLoader(a.embeddingHealthMarkdown)
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
	backgroundWorker.SetEmbeddingFactory(a.semanticEmbeddingProvider)
	backgroundWorker.SetRetrievalModeLoader(a.retrievalMode)
	backgroundWorker.SetProjection(a.projectSearchStores)
	backgroundWorker.SetSourceHealth(a)
	backgroundScheduler := scheduler.New(a.store, a.pollingInterval)
	backgroundScheduler.SetRetentionLoader(a.retentionPolicy)
	backgroundScheduler.SetNotificationLoader(a.notificationPolicy)
	if revision := a.searchConfig(workerCtx).EmbeddingRevision; revision != "" {
		if queued, err := a.enqueueEmbeddingRevisionReindex(workerCtx, revision); err != nil {
			slog.Warn("incompatible embedding refs could not be queued for repair", "error", err)
		} else if queued > 0 {
			slog.Info("queued incompatible embedding refs for automatic repair", "jobs", queued)
		}
	}
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
	a.mux.Handle("POST /api/v1/tools/dependency-usage/test", a.authenticate(http.HandlerFunc(a.testDependencyUsage)))
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
	a.mux.Handle("GET /api/v1/admin/webhook-events", a.authorize(http.HandlerFunc(a.webhookEvents), "source-admin", "readonly-operator"))
	a.mux.Handle("GET /api/v1/admin/dependency-inventory", a.authorize(http.HandlerFunc(a.dependencyInventory), "source-admin", "search-admin", "readonly-operator"))
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

// browserSessionLifetime is how long a browser session stays valid. It is
// deliberately independent from the Keycloak access token lifetime, which is
// often five minutes: binding the cookie to that value logged administrators
// out again on the next page refresh.
const browserSessionLifetime = 12 * time.Hour

// sessionRenewalThreshold slides the session forward while the user is active
// so long admin work is never interrupted by a hard expiry.
const sessionRenewalThreshold = 2 * time.Hour

var platformRoles = []string{
	"platform-admin", "security-admin", "mcp-admin", "source-admin",
	"search-admin", "auditor", "developer", "service-account", "readonly-operator",
}
var errUserDisabled = errors.New("user is disabled")

// jobStallWarning is when a running job starts looking stuck to an operator.
// The worker reclaims it slightly later, so the screen warns before that.
const jobStallWarning = 10 * time.Minute

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

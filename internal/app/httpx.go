package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"git-ctx/internal/auth"
	webfs "git-ctx/web"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTP plumbing shared by every route: middleware, response helpers and the
// static console assets.

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
func (a *App) serveWebApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// /admin is a client-side route. Serve the same embedded index as / instead
	// of reaching back into the process working directory, which could contain
	// an older web tree (or no web tree at all).
	request := r.Clone(r.Context())
	request.URL.Path = "/"
	http.FileServer(webRoot()).ServeHTTP(w, request)
}

func (a *App) audit(r *http.Request, p auth.Principal, action, rt, rid, outcome string, metadata any) {
	raw, _ := json.Marshal(metadata)
	_, _ = a.store.DB.ExecContext(r.Context(), a.store.Rebind(`INSERT INTO audit_logs(id,actor_id,action,resource_type,resource_id,outcome,ip_address,metadata) VALUES(?,?,?,?,?,?,?,?)`), time.Now().Format("20060102150405.000000000"), p.UserID, action, rt, rid, outcome, r.RemoteAddr, string(raw))
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

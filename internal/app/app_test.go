package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/config"
	runtimelogging "git-ctx/internal/logging"
	"git-ctx/internal/observability"
	"git-ctx/internal/recovery"
	"git-ctx/internal/search"
	"git-ctx/internal/store"
	"git-ctx/internal/version"
)

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (r *flushRecorder) Flush() { r.flushed = true }

func TestStatusWriterPreservesStreaming(t *testing.T) {
	recorder := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := &statusWriter{ResponseWriter: recorder}
	flusher, ok := any(w).(http.Flusher)
	if !ok {
		t.Fatal("status writer must expose http.Flusher")
	}
	flusher.Flush()
	if !recorder.flushed || w.status != http.StatusOK {
		t.Fatalf("flush was not forwarded: flushed=%v status=%d", recorder.flushed, w.status)
	}
}

func TestOperationalSettingsValidation(t *testing.T) {
	a := &App{}
	valid := []struct {
		category string
		value    map[string]any
	}{
		{"mcp", map[string]any{"allowedOrigins": []any{"https://git-ctx.company"}, "maxRequestBytes": float64(1 << 20)}},
		{"index", map[string]any{"pollingMinutes": float64(30)}},
		{"security", map[string]any{"trustedProxyCidrs": []any{"10.20.0.0/16"}}},
		{"logging", map[string]any{"level": "debug"}},
		{"operations", map[string]any{"listenAddress": ":4747", "readTimeoutSeconds": float64(30), "maintenanceMode": true}},
		{"search", map[string]any{"retrievalMode": "hybrid-fallback", "minimumEmbeddingCoveragePercent": float64(80), "embeddingFailureThreshold": float64(2), "embeddingCooldownSeconds": float64(60), "embeddingCacheSeconds": float64(120)}},
	}
	for _, test := range valid {
		if err := a.validateSetting(context.Background(), test.category, test.value); err != nil {
			t.Fatalf("valid %s setting rejected: %v", test.category, err)
		}
	}
	invalid := []struct {
		category string
		value    map[string]any
	}{
		{"mcp", map[string]any{"allowedOrigins": []any{"http://git-ctx.company/path"}}},
		{"mcp", map[string]any{"maxRequestBytes": float64(17 << 20)}},
		{"index", map[string]any{"pollingMinutes": float64(0)}},
		{"security", map[string]any{"trustedProxyCidrs": []any{"not-a-cidr"}}},
		{"logging", map[string]any{"level": "verbose"}},
		{"operations", map[string]any{"listenAddress": "all-interfaces", "readTimeoutSeconds": float64(0)}},
		{"search", map[string]any{"retrievalMode": "hybrid-fallback", "minimumEmbeddingCoveragePercent": float64(101)}},
		{"search", map[string]any{"retrievalMode": "hybrid-fallback", "embeddingFailureThreshold": float64(0)}},
		{"search", map[string]any{"retrievalMode": "hybrid-fallback", "embeddingCooldownSeconds": float64(4)}},
		{"search", map[string]any{"retrievalMode": "hybrid-fallback", "embeddingCacheSeconds": float64(3601)}},
	}
	for _, test := range invalid {
		if err := a.validateSetting(context.Background(), test.category, test.value); err == nil {
			t.Fatalf("invalid %s setting was accepted: %#v", test.category, test.value)
		}
	}
}

func TestRetrievalPolicyChangeQueuesEachRefOnce(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:retrieval-reindex?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','p','repo','Repo','gitlab','1','/p/repo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,embedding_revision) VALUES('r','main','abc','old-model')`)
	a := &App{store: db}
	queued, err := a.enqueueRetrievalReindex(ctx)
	if err != nil || queued != 1 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}
	queued, err = a.enqueueRetrievalReindex(ctx)
	if err != nil || queued != 0 {
		t.Fatalf("duplicate queued=%d err=%v", queued, err)
	}
	var kind, status string
	if err = db.DB.QueryRow(`SELECT kind,status FROM index_jobs WHERE repository_id='r' AND ref_name='main'`).Scan(&kind, &status); err != nil {
		t.Fatal(err)
	}
	if kind != "retrieval-policy" || status != "pending" {
		t.Fatalf("kind=%s status=%s", kind, status)
	}
	_, _ = db.DB.Exec(`UPDATE index_jobs SET status='completed'`)
	queued, err = a.enqueueEmbeddingRevisionReindex(ctx, "sha256:new-model")
	if err != nil || queued != 1 {
		t.Fatalf("embedding repair queued=%d err=%v", queued, err)
	}
	if err = db.DB.QueryRow(`SELECT kind,status FROM index_jobs WHERE kind='embedding-revision'`).Scan(&kind, &status); err != nil {
		t.Fatal(err)
	}
	if kind != "embedding-revision" || status != "pending" {
		t.Fatalf("embedding kind=%s status=%s", kind, status)
	}
	if queued, err = a.enqueueEmbeddingRevisionReindex(ctx, "sha256:new-model"); err != nil || queued != 0 {
		t.Fatalf("duplicate embedding repair queued=%d err=%v", queued, err)
	}
}

func TestNotificationWebhookConnectionTestAndSecretMasking(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer notification-secret" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:notification-setting?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	body := fmt.Sprintf(`{"inAppEnabled":true,"externalEnabled":true,"webhookUrl":%q,"webhookAuthorization":"Bearer notification-secret","timeoutSeconds":5,"maxAttempts":3}`, server.URL)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer bootstrap")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	tested := request(http.MethodPost, "/api/v1/admin/settings/notifications/test", body)
	if tested.Code != http.StatusOK || calls != 1 {
		t.Fatalf("test status=%d calls=%d body=%s", tested.Code, calls, tested.Body.String())
	}
	saved := request(http.MethodPut, "/api/v1/admin/settings/notifications", body)
	if saved.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	loaded := request(http.MethodGet, "/api/v1/admin/settings/notifications", "")
	if loaded.Code != http.StatusOK || !strings.Contains(loaded.Body.String(), `"webhookAuthorization":"********"`) || strings.Contains(loaded.Body.String(), "notification-secret") {
		t.Fatalf("loaded status=%d body=%s", loaded.Code, loaded.Body.String())
	}
}

func TestAdminCanInspectAndRetryNotificationDelivery(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:notification-delivery-admin?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, err = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email,status) VALUES('notify-user','kc-notify','notify-user','notify@example.test','active')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.store.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,title,message) VALUES('notify-1','notify-user','test','Test notification','safe message')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.store.DB.Exec(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,status,attempts,last_error) VALUES('delivery-1','notify-1','webhook','hash-only','dead',3,'connection refused')`)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer bootstrap")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	listed := request(http.MethodGet, "/api/v1/admin/notification-deliveries")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"delivery-1"`) || strings.Contains(listed.Body.String(), "hash-only") {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	retried := request(http.MethodPost, "/api/v1/admin/notification-deliveries/delivery-1/retry")
	if retried.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", retried.Code, retried.Body.String())
	}
	var status, lastError string
	if err = a.store.DB.QueryRow(`SELECT status,last_error FROM notification_deliveries WHERE id='delivery-1'`).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || lastError != "" {
		t.Fatalf("status=%q lastError=%q", status, lastError)
	}
}

func TestOperationsAndLoggingSettingsApply(t *testing.T) {
	runtimelogging.Reset()
	t.Cleanup(runtimelogging.Reset)
	a, err := New(context.Background(), config.Config{
		ListenAddress: ":4747", DatabaseDriver: "sqlite", DatabaseDSN: "file:runtime-operations?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	request := func(method, path, body string, admin bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if admin {
			req.Header.Set("Authorization", "Bearer bootstrap")
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	operations := `{"listenAddress":"127.0.0.1:54747","readHeaderTimeoutSeconds":5,"readTimeoutSeconds":20,"writeTimeoutSeconds":40,"idleTimeoutSeconds":50,"shutdownTimeoutSeconds":8,"maintenanceMode":true,"maintenanceMessage":"관리자 점검 중"}`
	saved := request(http.MethodPut, "/api/v1/admin/settings/operations", operations, true)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"restartRequired":true`) {
		t.Fatalf("operations save status=%d body=%s", saved.Code, saved.Body.String())
	}
	server := a.HTTPServerConfig(context.Background())
	if server.ListenAddress != "127.0.0.1:54747" || server.ReadTimeout != 20*time.Second || server.ShutdownTimeout != 8*time.Second {
		t.Fatalf("server config=%+v", server)
	}
	blocked := request(http.MethodGet, "/api/v1/me", "", false)
	if blocked.Code != http.StatusServiceUnavailable || !strings.Contains(blocked.Body.String(), "관리자 점검 중") {
		t.Fatalf("maintenance status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	allowed := request(http.MethodGet, "/api/v1/admin/settings/operations", "", true)
	if allowed.Code != http.StatusOK {
		t.Fatalf("admin unavailable during maintenance status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	logging := request(http.MethodPut, "/api/v1/admin/settings/logging", `{"level":"debug"}`, true)
	if logging.Code != http.StatusOK || runtimelogging.Level.Level() != -4 {
		t.Fatalf("logging status=%d level=%v body=%s", logging.Code, runtimelogging.Level.Level(), logging.Body.String())
	}
	deleted := request(http.MethodDelete, "/api/v1/admin/settings/logging", "", true)
	if deleted.Code != http.StatusNoContent || runtimelogging.Level.Level() != 0 {
		t.Fatalf("logging delete status=%d level=%v", deleted.Code, runtimelogging.Level.Level())
	}
}

func TestHTTPTraceContextPropagation(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:http-trace?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	cfg := observability.Config{Enabled: true, Endpoint: collector.URL + "/v1/traces", ServiceName: "git-ctx-test", SampleRatio: 1, Timeout: time.Second, AllowInsecureLocalhost: true}
	if err = a.traces.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	const traceID = "0af7651916cd43dd8448eb211c80319c"
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Traceparent", "00-"+traceID+"-b7ad6b7169203331-01")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Trace-Id") != traceID {
		t.Fatalf("status=%d trace=%q", rec.Code, rec.Header().Get("X-Trace-Id"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = a.traces.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOneTimeBootstrapTokenGeneratedAndRemoved(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "backups")
	baseConfig := config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "bootstrap.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), PublicURL: "http://localhost:4747", BackupDirectory: directory,
	}
	a, err := New(context.Background(), baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	required, path := a.bootstrapStatus()
	if !required || path != filepath.Join(directory, "bootstrap-admin.token") {
		t.Fatalf("required=%v path=%q", required, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) != a.bootstrapAdminToken() {
		t.Fatalf("token file mismatch err=%v", err)
	}
	b, err := New(context.Background(), baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.bootstrapAdminToken() != a.bootstrapAdminToken() || !b.validBootstrapToken(context.Background(), strings.TrimSpace(string(raw))) {
		t.Fatal("multiple instances did not share the bootstrap token")
	}
	a.disableBootstrapAdmin()
	if b.validBootstrapToken(context.Background(), strings.TrimSpace(string(raw))) {
		t.Fatal("bootstrap revocation did not propagate to another instance")
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file remains: %v", err)
	}
}

func TestPlatformAdminUserCRUDAndDisabledOIDCUser(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite",
		DatabaseDSN:    "file:" + filepath.Join(t.TempDir(), "users.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper:      strings.Repeat("p", 32),
		MasterKey:      strings.Repeat("m", 32),
		BootstrapAdmin: "bootstrap",
		PublicURL:      "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer bootstrap")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	created := request(http.MethodPost, "/api/v1/admin/users", `{"subject":"kc-user-1","username":"alice","email":"alice@example.test","status":"active","roles":["developer","source-admin"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s", created.Code, created.Body.String())
	}
	var createdBody map[string]string
	if err = json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	id := createdBody["id"]
	listed := request(http.MethodGet, "/api/v1/admin/users", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"source-admin"`) {
		t.Fatalf("list=%d body=%s", listed.Code, listed.Body.String())
	}
	updated := request(http.MethodPut, "/api/v1/admin/users/"+id, `{"username":"alice","email":"alice@example.test","status":"disabled","roles":["developer"]}`)
	if updated.Code != http.StatusNoContent {
		t.Fatalf("update=%d body=%s", updated.Code, updated.Body.String())
	}
	if _, err = a.upsertIdentity(context.Background(), auth.Identity{Subject: "kc-user-1", Username: "alice", Roles: []string{"developer"}}); !errors.Is(err, errUserDisabled) {
		t.Fatalf("disabled OIDC login err=%v", err)
	}
	reactivated := request(http.MethodPut, "/api/v1/admin/users/"+id, `{"username":"alice","email":"alice@example.test","status":"active","roles":["platform-admin"]}`)
	if reactivated.Code != http.StatusNoContent {
		t.Fatalf("reactivate=%d body=%s", reactivated.Code, reactivated.Body.String())
	}
	if _, err = a.upsertIdentity(context.Background(), auth.Identity{Subject: "kc-user-1", Username: "alice", Roles: []string{"developer"}}); err != nil {
		t.Fatal(err)
	}
	roles, err := a.userRoles(context.Background(), id)
	if err != nil || !slices.Contains(roles, "platform-admin") || slices.Contains(roles, "developer") {
		t.Fatalf("admin-managed roles=%v err=%v", roles, err)
	}
	deleted := request(http.MethodDelete, "/api/v1/admin/users/"+id, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestSourceACLPrincipalsKeepBothIdentityMappings(t *testing.T) {
	got := sourceACLPrincipals("alice.bb", "42", []string{"group:engineering"})
	for _, want := range []string{"alice.bb", "bitbucket:licensed", "gitlab:42", "gitlab:authenticated", "group:engineering"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	if got := sourceACLPrincipals("", "", []string{"group:engineering"}); slices.Contains(got, "bitbucket:licensed") || slices.Contains(got, "gitlab:authenticated") {
		t.Fatalf("unmapped user received source-wide principal: %v", got)
	}
}

func TestAdministrativeBackupRestoreFlow(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "app-backup.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.store.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r1','KCB','demo','Demo','bitbucket','1','/kcb/demo','main')`)
	_, _ = a.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','r1','main','abc','README.md',1,1,'Demo','document','original','hash')`)
	request := func(method, path string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer bootstrap")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	created := request(http.MethodPost, "/api/v1/admin/backups", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var record struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &record); err != nil || record.ID == "" {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	_, _ = a.store.DB.Exec(`UPDATE document_chunks SET content='mutated' WHERE id='c1'`)
	denied := request(http.MethodPost, "/api/v1/admin/backups/"+record.ID+"/restore", nil)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("restore without confirmation status=%d", denied.Code)
	}
	restored := request(http.MethodPost, "/api/v1/admin/backups/"+record.ID+"/restore", map[string]string{"X-Restore-Confirmation": "RESTORE " + record.ID})
	if restored.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", restored.Code, restored.Body.String())
	}
	var content string
	if err = a.store.DB.QueryRow(`SELECT content FROM document_chunks WHERE id='c1'`).Scan(&content); err != nil || content != "original" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	download := request(http.MethodGet, "/api/v1/admin/backups/"+record.ID+"/download", nil)
	if download.Code != http.StatusOK || download.Header().Get("Cache-Control") != "no-store" || download.Header().Get("X-Content-SHA256") == "" || !strings.HasPrefix(download.Body.String(), magicForTest()) {
		t.Fatalf("download status=%d headers=%v", download.Code, download.Header())
	}
	var restoreAudits int
	if err = a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='backup.restore' AND outcome='success'`).Scan(&restoreAudits); err != nil || restoreAudits != 1 {
		t.Fatalf("restore audits=%d err=%v", restoreAudits, err)
	}
}

func magicForTest() string { return "GCTXBACKUP1\n" }

func TestBootstrapLoginCreatesHttpOnlySessionForMeAndRevokesIt(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "bootstrap-login.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	request := func(token, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/login", strings.NewReader(`{"token":"`+token+`"}`))
		req.Host = "localhost:4747"
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := request("bootstrap", "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request("wrong", "http://localhost:4747"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status=%d body=%s", rec.Code, rec.Body.String())
	}
	login := request("bootstrap", "http://localhost:4747")
	if login.Code != http.StatusOK || login.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("login status=%d headers=%v body=%s", login.Code, login.Header(), login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "git_ctx_session" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected bootstrap cookie: %#v", cookies)
	}
	meRequest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meRequest.AddCookie(cookies[0])
	me := httptest.NewRecorder()
	a.Handler().ServeHTTP(me, meRequest)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"Roles":["platform-admin"]`) || !strings.Contains(me.Body.String(), `"Version":"`+version.Version+`"`) {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}

	a.disableBootstrapAdmin()
	revokedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	revokedRequest.AddCookie(cookies[0])
	revoked := httptest.NewRecorder()
	a.Handler().ServeHTTP(revoked, revokedRequest)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bootstrap session status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestOneTimeRecoveryTokenCreatesRestrictedAdminSession(t *testing.T) {
	pepper := strings.Repeat("p", 32)
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:recovery-login?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: pepper, MasterKey: strings.Repeat("m", 32), RecoveryKey: pepper, BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	token, _, err := recovery.Generate(pepper, 15*time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	login := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/recovery/login", strings.NewReader(`{"token":"`+token+`"}`))
		req.Host = "localhost:4747"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	if crossOrigin := login("https://evil.example"); crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", crossOrigin.Code, crossOrigin.Body.String())
	}
	authenticated := login("http://localhost:4747")
	if authenticated.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", authenticated.Code, authenticated.Body.String())
	}
	cookies := authenticated.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected recovery cookie: %#v", cookies)
	}
	meRequest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meRequest.AddCookie(cookies[0])
	me := httptest.NewRecorder()
	a.Handler().ServeHTTP(me, meRequest)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"UserID":"break-glass-admin"`) || !strings.Contains(me.Body.String(), `"Roles":["platform-admin"]`) {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}
	if replay := login("http://localhost:4747"); replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	keyRequest := httptest.NewRequest(http.MethodPost, "/api/v1/me/api-keys", strings.NewReader(`{"name":"persistent-recovery-key","scopes":["query-docs"]}`))
	keyRequest.Host = "localhost:4747"
	keyRequest.Header.Set("Content-Type", "application/json")
	keyRequest.Header.Set("Origin", "http://localhost:4747")
	keyRequest.AddCookie(cookies[0])
	key := httptest.NewRecorder()
	a.Handler().ServeHTTP(key, keyRequest)
	if key.Code != http.StatusForbidden || !strings.Contains(key.Body.String(), "recovery_session_restricted") {
		t.Fatalf("key status=%d body=%s", key.Code, key.Body.String())
	}
	var consumed int
	if err = a.store.DB.QueryRow(`SELECT COUNT(*) FROM admin_recovery_tokens`).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("consumed=%d err=%v", consumed, err)
	}
}

func TestCookieMutationOriginPolicy(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		origin     string
		fetchSite  string
		forwardTLS bool
		want       bool
	}{
		{name: "safe read", method: http.MethodGet, origin: "https://evil.example", want: true},
		{name: "same origin", method: http.MethodPost, origin: "https://git-ctx.company", forwardTLS: true, want: true},
		{name: "sibling subdomain", method: http.MethodPost, origin: "https://evil.company", forwardTLS: true, want: false},
		{name: "scheme mismatch", method: http.MethodPost, origin: "http://git-ctx.company", forwardTLS: true, want: false},
		{name: "null origin", method: http.MethodDelete, origin: "null", want: false},
		{name: "fetch metadata alone", method: http.MethodPatch, fetchSite: "same-origin", want: false},
		{name: "fetch same site", method: http.MethodPatch, fetchSite: "same-site", want: false},
		{name: "fetch cross site", method: http.MethodPatch, fetchSite: "cross-site", want: false},
		{name: "missing origin", method: http.MethodPut, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, "http://git-ctx.company/api/v1/admin/settings", nil)
			request.Host = "git-ctx.company"
			request.Header.Set("Origin", tt.origin)
			request.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			if tt.forwardTLS {
				request.Header.Set("X-Forwarded-Proto", "https")
			}
			if got := cookieMutationAllowed(request); got != tt.want {
				t.Fatalf("allowed=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestProgrammaticRecoveryKeyIsNormalizedAndNeverWeak(t *testing.T) {
	base := config.Config{
		DatabaseDriver:  "sqlite",
		DatabaseDSN:     "file:programmatic-recovery-key?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper:       strings.Repeat("p", 32),
		MasterKey:       strings.Repeat("m", 32),
		BootstrapAdmin:  "bootstrap",
		BackupDirectory: t.TempDir(),
	}
	normalized := strings.Repeat("r", 32)
	cfg := base
	cfg.RecoveryKey = " \t" + normalized + "\r\n"
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.cfg.RecoveryKey != normalized {
		t.Fatalf("recovery key was not normalized: %q", a.cfg.RecoveryKey)
	}
	a.Close()

	cfg = base
	cfg.DatabaseDSN = "file:programmatic-random-recovery-key?mode=memory&cache=shared&_foreign_keys=on"
	cfg.RecoveryKey = strings.Repeat(" ", 32)
	a, err = New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.cfg.RecoveryKey) < 32 || strings.TrimSpace(a.cfg.RecoveryKey) == "" {
		t.Fatalf("blank recovery key was not replaced: %q", a.cfg.RecoveryKey)
	}
	a.Close()

	cfg = base
	cfg.RecoveryKey = strings.Repeat("w", 31)
	if _, err = New(context.Background(), cfg); err == nil {
		t.Fatal("short programmatic recovery key was accepted")
	}
}

func TestManagedSecretAdminAPIAndSettingReference(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "managed-secret-api.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer bootstrap")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	created := request(http.MethodPost, "/api/v1/admin/secrets", `{"name":"bitbucket-pat","backend":"database","value":"pat-secret","reason":"initial"}`)
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), "pat-secret") {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	listed := request(http.MethodGet, "/api/v1/admin/secrets", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"bitbucket-pat"`) || strings.Contains(listed.Body.String(), "pat-secret") {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	raw, _ := json.Marshal(map[string]any{"baseUrl": "https://bitbucket.company", "pat": "secret://bitbucket-pat"})
	sealed, err := a.seal(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.store.DB.Exec(a.store.Rebind(`INSERT INTO system_settings(category,version,value_encrypted,updated_by) VALUES('bitbucket',1,?,'admin')`), sealed)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := a.loadSettingMap(context.Background(), "bitbucket")
	if err != nil || resolved["pat"] != "pat-secret" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	disabled := request(http.MethodPost, "/api/v1/admin/secrets/bitbucket-pat/disable", "")
	if disabled.Code != http.StatusNoContent {
		t.Fatalf("disable status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	if _, err = a.loadSettingMap(context.Background(), "bitbucket"); err == nil {
		t.Fatal("disabled secret reference was not fail-closed")
	}
}

func TestVaultAdminConnectionTest(t *testing.T) {
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/token/lookup-self" || r.Header.Get("X-Vault-Token") != "vault-token" {
			http.Error(w, "unexpected Vault request", http.StatusForbidden)
			return
		}
		io.WriteString(w, `{"data":{"policies":["git-ctx"]}}`)
	}))
	defer vault.Close()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "vault-admin-test.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	body := fmt.Sprintf(`{"enabled":true,"baseUrl":%q,"token":"vault-token","mount":"secret","prefix":"git-ctx","tlsVerify":true}`, vault.URL)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/vault/test", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bootstrap")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "vault-token") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var audits int
	if err = a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='settings.test' AND resource_id='vault' AND outcome='success'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}

func TestBitbucketConnectionTestValidatesACLAndBranches(t *testing.T) {
	denyACL := false
	denySearch := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/1.0/projects":
			io.WriteString(w, `{"values":[{"key":"PRJ"}],"isLastPage":true}`)
		case "/rest/api/1.0/projects/PRJ/repos":
			io.WriteString(w, `{"values":[{"id":1,"slug":"repo","project":{"key":"PRJ"}}],"isLastPage":true}`)
		case "/rest/api/1.0/projects/PRJ/repos/repo/permissions/users":
			if denyACL {
				http.Error(w, "repository admin required", http.StatusForbidden)
				return
			}
			io.WriteString(w, `{"values":[],"isLastPage":true}`)
		case "/rest/api/1.0/projects/PRJ/repos/repo/permissions/groups",
			"/rest/api/1.0/projects/PRJ/permissions/users",
			"/rest/api/1.0/projects/PRJ/permissions/groups":
			io.WriteString(w, `{"values":[],"isLastPage":true}`)
		case "/rest/api/1.0/admin/permissions/users", "/rest/api/1.0/admin/permissions/groups":
			http.Error(w, "global admin required", http.StatusForbidden)
		case "/rest/api/1.0/projects/PRJ/permissions/PROJECT_READ/all",
			"/rest/api/1.0/projects/PRJ/permissions/PROJECT_WRITE/all",
			"/rest/api/1.0/projects/PRJ/permissions/PROJECT_ADMIN/all":
			io.WriteString(w, `{"permitted":false}`)
		case "/rest/api/1.0/projects/PRJ/repos/repo/branches":
			io.WriteString(w, `{"values":[{"displayId":"main","latestCommit":"abc","isDefault":true}],"isLastPage":true}`)
		case "/rest/search/latest/search":
			if denySearch {
				http.Error(w, "code search unavailable", http.StatusServiceUnavailable)
				return
			}
			io.WriteString(w, `{"code":{"values":[]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:bitbucket-setting-test?mode=memory&cache=shared&_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	test := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/bitbucket/test", strings.NewReader(fmt.Sprintf(`{"baseUrl":%q,"apiPrefix":"/rest/api/1.0","pat":"token"}`, server.URL)))
		req.Header.Set("Authorization", "Bearer bootstrap")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := test(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"querySearch"`) || !strings.Contains(rec.Body.String(), `"status":"verified"`) {
		t.Fatalf("complete connection test=%d body=%s", rec.Code, rec.Body.String())
	}
	denySearch = true
	if rec := test(); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "query search API test") {
		t.Fatalf("query search failure status=%d body=%s", rec.Code, rec.Body.String())
	}
	denySearch = false
	denyACL = true
	if rec := test(); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ACL synchronization") {
		t.Fatalf("insufficient ACL permission status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestKeycloakBaseRealmSaveNormalizesAndKeepsBootstrapUntilAdminLogin(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const suffix = "/.well-known/openid-configuration"
		if !strings.HasPrefix(r.URL.Path, "/realms/") || !strings.HasSuffix(r.URL.Path, suffix) {
			http.NotFound(w, r)
			return
		}
		issuer := server.URL + strings.TrimSuffix(r.URL.Path, suffix)
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/protocol/openid-connect/auth",
			"token_endpoint": issuer + "/protocol/openid-connect/token", "jwks_uri": issuer + "/protocol/openid-connect/certs",
		})
	}))
	defer server.Close()
	issuer := server.URL + "/realms/company"
	directory := filepath.Join(t.TempDir(), "backups")
	cfg := config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "keycloak-save.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), PublicURL: "https://git-ctx.internal", BackupDirectory: directory,
	}
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	token := a.bootstrapAdminToken()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/keycloak", strings.NewReader(fmt.Sprintf(`{"baseUrl":%q,"realm":"company","clientId":"git-ctx","clientSecret":"client-secret","realmRoleMappings":{"ctx-admin":"platform-admin"},"tlsVerify":false}`, server.URL)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := a.loadSettingMapRaw(context.Background(), "keycloak")
	if err != nil || stored["issuerUrl"] != issuer || stored["redirectUrl"] != "https://git-ctx.internal/auth/callback" || stored["tlsVerify"] != true {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/keycloak", nil)
	getRequest.Header.Set("Authorization", "Bearer "+token)
	getResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(getResult, getRequest)
	body := getResult.Body.String()
	if getResult.Code != http.StatusOK || !strings.Contains(body, server.URL) || !strings.Contains(body, `"realm":"company"`) || !strings.Contains(body, `"clientId":"git-ctx"`) || !strings.Contains(body, `"clientSecret":"********"`) || strings.Contains(body, "client-secret") {
		t.Fatalf("reloaded Keycloak setting status=%d body=%s", getResult.Code, body)
	}
	if !a.validBootstrapToken(context.Background(), token) {
		t.Fatal("bootstrap was revoked before a successful Keycloak platform-admin login")
	}
	publicRequest := httptest.NewRequest(http.MethodGet, "/api/v1/public/config", nil)
	publicResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(publicResult, publicRequest)
	if publicResult.Code != http.StatusOK || !strings.Contains(publicResult.Body.String(), `"ssoConfigured":true`) || !strings.Contains(publicResult.Body.String(), `"bootstrapRequired":true`) {
		t.Fatalf("public login choices status=%d body=%s", publicResult.Code, publicResult.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/keycloak", strings.NewReader(fmt.Sprintf(`{"baseUrl":%q,"realm":"engineering","issuerMode":"auto","issuerUrl":%q,"clientId":"git-ctx","clientSecret":"********","redirectMode":"auto","realmRoleMappings":{"ctx-admin":"platform-admin"},"tlsVerify":false}`, server.URL, issuer)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("realm update status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err = a.loadSettingMapRaw(context.Background(), "keycloak")
	if err != nil || stored["issuerUrl"] != server.URL+"/realms/engineering" || stored["clientSecret"] != "client-secret" {
		t.Fatalf("updated=%#v err=%v", stored, err)
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/keycloak/status", nil)
	statusRequest.Header.Set("Authorization", "Bearer "+token)
	status := httptest.NewRecorder()
	a.Handler().ServeHTTP(status, statusRequest)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"active"`) || !strings.Contains(status.Body.String(), `/realms/engineering`) {
		t.Fatalf("OIDC status=%d body=%s", status.Code, status.Body.String())
	}
	uiRequest := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/ui", strings.NewReader(`{"publicUrl":"https://new-git-ctx.internal"}`))
	uiRequest.Header.Set("Authorization", "Bearer "+token)
	uiRequest.Header.Set("Content-Type", "application/json")
	uiResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(uiResult, uiRequest)
	if uiResult.Code != http.StatusOK {
		t.Fatalf("UI URL update=%d body=%s", uiResult.Code, uiResult.Body.String())
	}
	activeOIDC, err := a.loadOIDCConfig(context.Background())
	if err != nil || activeOIDC.RedirectURL != "https://new-git-ctx.internal/auth/callback" || activeOIDC.PostLogoutRedirectURL != "https://new-git-ctx.internal/" {
		t.Fatalf("active OIDC=%#v err=%v", activeOIDC, err)
	}
	a.Close()
	b, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.validBootstrapToken(context.Background(), token) {
		t.Fatal("bootstrap recovery did not survive restart before activation")
	}
	b.disableBootstrapAdmin()
}

func TestPublicAndAdminDatabaseStatus(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "db-status.db") + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	public := httptest.NewRecorder()
	a.Handler().ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil))
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), `"status":"connected"`) || strings.Contains(public.Body.String(), "db-status.db") {
		t.Fatalf("public status=%d body=%s", public.Code, public.Body.String())
	}
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/database/status", nil)
	adminRequest.Header.Set("Authorization", "Bearer bootstrap")
	admin := httptest.NewRecorder()
	a.Handler().ServeHTTP(admin, adminRequest)
	// Derive the expectation from the migration directory rather than naming a
	// file: the contract is that the status reports the newest migration that
	// ran, not that any particular one is newest.
	entries, readErr := os.ReadDir(filepath.Join("..", "store", "migrations"))
	if readErr != nil {
		t.Fatalf("read migrations: %v", readErr)
	}
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), `"latest":"`+newest+`"`) || !strings.Contains(admin.Body.String(), `"pool"`) {
		t.Fatalf("admin status=%d newest=%s body=%s", admin.Code, newest, admin.Body.String())
	}
}

func TestAdministrativeEmbeddingHealthReportsRevisionMismatch(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer modelServer.Close()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "embedding-health.db") + "?_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	modelRequest := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/model", strings.NewReader(fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":%q,"model":"model-v2","apiKey":"secret"}`, modelServer.URL)))
	modelRequest.Header.Set("Authorization", "Bearer bootstrap")
	modelRequest.Header.Set("Content-Type", "application/json")
	modelResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(modelResult, modelRequest)
	if modelResult.Code != http.StatusOK {
		t.Fatalf("model setting=%d body=%s", modelResult.Code, modelResult.Body.String())
	}
	_, _ = a.store.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','dify','Dify','gitlab','1','/core/dify','main')`)
	_, _ = a.store.DB.Exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,embedding_revision,total_chunks,embedded_chunks,embedding_status) VALUES('r','main','abc','model-v1',10,2,'partial')`)

	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer bootstrap")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	vector := request("/api/v1/admin/vector/status")
	for _, expected := range []string{`"requestedRetrievalMode":"hybrid-fallback"`, `"operationalMode":"keyword-only"`, `"embeddingCoveragePercent":0`, `"storedVectors":2`, `"compatibleVectors":0`, `"incompatibleRefs":1`, `"partialRefs":1`, `"minimumEmbeddingCoveragePercent":80`} {
		if vector.Code != http.StatusOK || !strings.Contains(vector.Body.String(), expected) {
			t.Fatalf("vector status=%d body=%s missing=%s", vector.Code, vector.Body.String(), expected)
		}
	}
	health := request("/api/v1/admin/health")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"operationalMode":"keyword-only"`) || !strings.Contains(health.Body.String(), `"coveragePercent":0`) || !strings.Contains(health.Body.String(), `"incompatibleRefs":1`) {
		t.Fatalf("health=%d body=%s", health.Code, health.Body.String())
	}
	metrics := request("/metrics")
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "git_ctx_embedding_coverage_percent 0.000") || !strings.Contains(metrics.Body.String(), "git_ctx_embedding_incompatible_refs 1") || !strings.Contains(metrics.Body.String(), "git_ctx_embedding_circuit_open 0") {
		t.Fatalf("metrics=%d body=%s", metrics.Code, metrics.Body.String())
	}
}

func TestPostgresStartupFailureFallsBackToSQLiteRecovery(t *testing.T) {
	directory := t.TempDir()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "postgres",
		DatabaseDSN:    "postgres://gitctx:invalid@127.0.0.1:1/gitctx?sslmode=disable&connect_timeout=1",
		KeyPepper:      strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32),
		BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: directory,
	})
	if err != nil {
		t.Fatalf("recovery startup failed: %v", err)
	}
	defer a.Close()
	if !a.recoveryMode || a.store.Driver() != "sqlite" {
		t.Fatalf("expected sqlite recovery mode, mode=%v driver=%s", a.recoveryMode, a.store.Driver())
	}
	info, err := os.Stat(filepath.Join(directory, "recovery.db"))
	if err != nil {
		t.Fatalf("recovery database missing: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("recovery database mode=%o", info.Mode().Perm())
	}
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"recoveryMode":true`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdministrativeSearchQualityBenchmarkFlow(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "quality-api.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.store.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r1','KCB','demo','Demo','bitbucket','1','/kcb/demo','main')`)
	_, _ = a.store.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','alice','read')`)
	_, _ = a.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c1','r1','main','abc','docs/gpu.md',1,5,'GPU API','document','Pod GPU usage endpoint','hash',x'')`)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer bootstrap")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	created := request(http.MethodPost, "/api/v1/admin/quality/cases", `{"name":"GPU","libraryId":"/kcb/demo/main","query":"Pod GPU usage","principals":["alice"],"relevantSources":["docs/gpu.md"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("case status=%d body=%s", created.Code, created.Body.String())
	}
	run := request(http.MethodPost, "/api/v1/admin/quality/runs", `{"topK":8,"minimumRecall":0.8,"minimumMrr":0.8,"minimumNdcg":0.8}`)
	if run.Code != http.StatusCreated {
		t.Fatalf("run status=%d body=%s", run.Code, run.Body.String())
	}
	var result struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		Recall float64 `json:"recallAtK"`
	}
	if err = json.Unmarshal(run.Body.Bytes(), &result); err != nil || result.ID == "" || result.Status != "passed" || result.Recall != 1 {
		t.Fatalf("run=%#v err=%v", result, err)
	}
	details := request(http.MethodGet, "/api/v1/admin/quality/runs/"+result.ID+"/results", "")
	if details.Code != http.StatusOK || !strings.Contains(details.Body.String(), "docs/gpu.md") {
		t.Fatalf("details status=%d body=%s", details.Code, details.Body.String())
	}
	var audits int
	if err = a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='quality.run' AND outcome='passed'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}

func TestAdministrativeModelConnectionTestCallsEmbeddingAndReranker(t *testing.T) {
	var embeddingCalls, rerankCalls int
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer model-secret" {
			t.Errorf("missing model authorization")
		}
		switch r.URL.Path {
		case "/v1/embeddings":
			embeddingCalls++
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
		case "/v1/rerank":
			rerankCalls++
			_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.1}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer modelServer.Close()
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:model-test-api?mode=memory&cache=shared&_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	body := fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":%q,"model":"embed","apiKey":"model-secret","rerankerEnabled":true,"rerankerProvider":"openai-compatible","rerankerBaseUrl":%q,"rerankerModel":"rerank","rerankerApiKey":"model-secret","tlsVerify":true}`, modelServer.URL, modelServer.URL)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/model/test", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bootstrap")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || embeddingCalls != 1 || rerankCalls != 1 || !strings.Contains(rec.Body.String(), `"status":"verified"`) {
		t.Fatalf("status=%d body=%s embedding=%d rerank=%d", rec.Code, rec.Body.String(), embeddingCalls, rerankCalls)
	}
	var audits int
	if err = a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='settings.test' AND resource_type='model' AND outcome='success'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}

func TestAdministrativeRoleMatrix(t *testing.T) {
	principal := func(role string) auth.Principal { return auth.Principal{Roles: []string{role}} }
	for role, endpointRole := range map[string]string{
		"source-admin": "source-admin", "mcp-admin": "mcp-admin", "search-admin": "search-admin",
		"security-admin": "security-admin", "auditor": "auditor", "readonly-operator": "readonly-operator",
	} {
		if !roleAllowed(principal(role), endpointRole) {
			t.Errorf("%s was denied its endpoint role", role)
		}
	}
	if roleAllowed(principal("developer"), "source-admin", "readonly-operator") {
		t.Fatal("developer gained administrative endpoint access")
	}
	if !roleAllowed(principal("platform-admin"), "source-admin") {
		t.Fatal("platform-admin did not inherit endpoint access")
	}
	for category, role := range map[string]string{
		"bitbucket": "source-admin", "gitlab": "source-admin", "index": "source-admin",
		"mcp": "mcp-admin", "search": "search-admin", "model": "search-admin",
		"security": "security-admin",
	} {
		if !settingRoleAllowed(principal(role), category) {
			t.Errorf("%s denied setting %s", role, category)
		}
		if settingRoleAllowed(principal("developer"), category) {
			t.Errorf("developer gained setting %s", category)
		}
	}
	if settingRoleAllowed(principal("source-admin"), "keycloak") {
		t.Fatal("source-admin gained platform-only Keycloak settings")
	}
}

// A denied settings change must name the missing role and the roles the caller
// actually has, and the diagnostics endpoint must report the same decision, so
// an administrator can fix the Keycloak mapping without reading server logs.
func TestSettingDenialAndAccessDiagnosticsExplainMissingRole(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:access-diagnostics?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email,status) VALUES('u1','kc-1','alice','','active')`)
	_, _ = a.store.DB.Exec(`INSERT INTO user_roles(user_id,role_code) VALUES('u1','mcp-admin')`)
	const rawSession = "session-token-for-mcp-admin"
	if _, err = a.store.DB.Exec(a.store.Rebind(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(?,?,?,?)`),
		sessionHash(rawSession), "u1", time.Now().UTC().Add(time.Hour), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	withSession := func(method, path, payload string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		if method != http.MethodGet && method != http.MethodHead {
			request.Header.Set("Origin", "http://example.com")
		}
		request.AddCookie(&http.Cookie{Name: "git_ctx_session", Value: rawSession})
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	deniedResult := withSession(http.MethodPut, "/api/v1/admin/settings/gitlab", `{}`)
	body := deniedResult.Body.String()
	if deniedResult.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", deniedResult.Code, body)
	}
	for _, expected := range []string{"insufficient_role", "source-admin", "platform-admin", "mcp-admin"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("denial message is missing %q: %s", expected, body)
		}
	}
	diagnosticsResult := withSession(http.MethodGet, "/api/v1/me/access", "")
	var access struct {
		AclReady bool `json:"aclReady"`
		Settings []struct {
			Category string   `json:"category"`
			Allowed  bool     `json:"allowed"`
			Roles    []string `json:"roles"`
		} `json:"settings"`
	}
	if err = json.Unmarshal(diagnosticsResult.Body.Bytes(), &access); err != nil {
		t.Fatalf("diagnostics body=%s err=%v", diagnosticsResult.Body.String(), err)
	}
	if access.AclReady {
		t.Fatal("an identity without a source claim must not report an ACL principal")
	}
	found := map[string]bool{}
	for _, item := range access.Settings {
		found[item.Category] = item.Allowed
	}
	if found["gitlab"] || !found["mcp"] {
		t.Fatalf("unexpected per-category decisions: %#v", access.Settings)
	}
}

// The setup dashboard must report the real blockers of a fresh install and the
// search diagnostics must explain another user's empty result set without
// exposing snippets or file paths to an administrator who lacks source access.
func TestSetupStatusAndSearchDiagnostics(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:setup-and-diagnostics?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	call := func(method, path, payload string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	var setup struct {
		Completed int `json:"completed"`
		Total     int `json:"total"`
		Steps     []struct {
			Key, Status, Target string
		} `json:"steps"`
	}
	if err = json.Unmarshal(call(http.MethodGet, "/api/v1/admin/setup-status", "").Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, step := range setup.Steps {
		status[step.Key] = step.Status
	}
	if setup.Total != 6 || status["keycloak"] != "todo" || status["source"] != "todo" || status["repositories"] != "todo" {
		t.Fatalf("a fresh install must report its blockers: %#v", setup)
	}

	// A configured source flips the matching step without touching the others.
	if saved := call(http.MethodPut, "/api/v1/admin/settings/ui", `{"serviceName":"git-ctx","publicUrl":"http://localhost:4747"}`); saved.Code != http.StatusOK {
		t.Fatalf("ui setting status=%d body=%s", saved.Code, saved.Body.String())
	}
	if _, err = a.store.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('r','core','demo','Demo','','gitlab','1','/core/demo','main',1)`); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(call(http.MethodGet, "/api/v1/admin/setup-status", "").Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	for _, step := range setup.Steps {
		if step.Key == "repositories" && step.Status != "done" {
			t.Fatalf("registered repository was not detected: %#v", step)
		}
	}

	_, _ = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email,status) VALUES('u1','kc-1','alice','','active')`)
	_, _ = a.store.DB.Exec(`INSERT INTO user_roles(user_id,role_code) VALUES('u1','developer')`)
	diagnostics := call(http.MethodPost, "/api/v1/admin/search-diagnostics", `{"username":"alice","query":"verify_token"}`)
	if diagnostics.Code != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%s", diagnostics.Code, diagnostics.Body.String())
	}
	body := diagnostics.Body.String()
	for _, expected := range []string{`"aclReady":false`, `"hitCount":0`, "bitbucket_user_slug"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("diagnostics is missing %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{`"snippet":`, `"Snippet":`, `"filePath":`, `"path":`, `"Path":`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("diagnostics leaked source content (%q): %s", forbidden, body)
		}
	}
	if missing := call(http.MethodPost, "/api/v1/admin/search-diagnostics", `{"username":"ghost","query":"x"}`); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown user status=%d", missing.Code)
	}
	var audited int
	if err = a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='search.diagnostics'`).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("diagnostics must be audited: count=%d err=%v", audited, err)
	}

	// Setting history exposes who changed which version, never the value.
	history := call(http.MethodGet, "/api/v1/admin/settings/ui/versions", "")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"version":1`) {
		t.Fatalf("history status=%d body=%s", history.Code, history.Body.String())
	}
	if strings.Contains(history.Body.String(), "publicUrl") {
		t.Fatalf("history leaked setting values: %s", history.Body.String())
	}
}

// Credential endpoints must throttle, API responses must never be cached, and
// administrators must search the catalog without a source ACL principal.
func TestCredentialThrottlingCacheHeadersAndAdminSearchAccess(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:hardening?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	attempt := func() int {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap/login", strings.NewReader(`{"token":"wrong"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	for count := 0; count < credentialAttemptLimit; count++ {
		if code := attempt(); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", count, code)
		}
	}
	if code := attempt(); code != http.StatusTooManyRequests {
		t.Fatalf("guessing was not throttled: status=%d", code)
	}

	status := httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil)
	statusResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(statusResult, status)
	if statusResult.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("API responses must not be cached: %v", statusResult.Header())
	}

	// A source administrator with no Bitbucket or GitLab identity still searches.
	_, _ = a.store.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','dify','Dify','AI platform','gitlab','1','/core/dify','main',1)`)
	_, _ = a.store.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','someone-else','read')`)
	admin := auth.Principal{UserID: "u1", Username: "ops", Roles: []string{"source-admin"}}
	if principals := searchPrincipals(admin); !search.Unrestricted(principals) {
		t.Fatalf("source-admin did not receive catalog-wide search: %v", principals)
	}
	repositories, err := a.search.SearchRepositories(context.Background(), searchPrincipals(admin), "dify", "", 10)
	if err != nil || len(repositories) != 1 {
		t.Fatalf("administrator search=%#v err=%v", repositories, err)
	}
	developer := auth.Principal{UserID: "u2", Username: "dev", Roles: []string{"developer"}, ACLPrincipal: "alice"}
	hidden, err := a.search.SearchRepositories(context.Background(), searchPrincipals(developer), "dify", "", 10)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("developer ACL leak=%#v err=%v", hidden, err)
	}
}

// A session close to expiry is extended while the user is active, so long
// administrative work is never interrupted by a forced re-login.
func TestActiveSessionIsRenewedBeforeExpiry(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:session-renewal?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "https://git-ctx.company",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email,status) VALUES('u1','kc-1','alice','','active')`)
	const raw = "expiring-session-token"
	if _, err = a.store.DB.Exec(a.store.Rebind(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(?,?,?,?)`),
		sessionHash(raw), "u1", time.Now().UTC().Add(20*time.Minute), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	request.AddCookie(&http.Cookie{Name: "git_ctx_session", Value: raw})
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var renewed *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "git_ctx_session" {
			renewed = cookie
		}
	}
	if renewed == nil || time.Until(renewed.Expires) < 8*time.Hour {
		t.Fatalf("session was not renewed: %#v", renewed)
	}
	// A plain HTTP request must not receive a Secure cookie even when the public
	// URL is HTTPS, otherwise the browser drops the session on refresh.
	if renewed.Secure {
		t.Fatalf("Secure must follow the request scheme: %#v", renewed)
	}
	var stored time.Time
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT expires_at FROM user_sessions WHERE id_hash=?`), sessionHash(raw)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if time.Until(stored) < 8*time.Hour {
		t.Fatalf("stored expiry was not extended: %v", stored)
	}
}

func TestAdministratorMCPKeyScopesRequireCurrentRole(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:admin-mcp-scopes?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('developer','developer','developer','')`)
	request := func(principal auth.Principal, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/me/api-keys", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
		rec := httptest.NewRecorder()
		a.createKey(rec, req)
		return rec
	}
	developer := request(auth.Principal{UserID: "developer", Roles: []string{"developer"}}, `{"name":"bad-admin","scopes":["get-platform-status"]}`)
	if developer.Code != http.StatusForbidden {
		t.Fatalf("developer status=%d body=%s", developer.Code, developer.Body.String())
	}
	sourceAdmin := request(auth.Principal{UserID: "developer", Roles: []string{"source-admin"}}, `{"name":"source-ops","scopes":["search-repositories","search-source","get-platform-status","list-index-jobs","reindex-repository"]}`)
	if sourceAdmin.Code != http.StatusCreated || !strings.Contains(sourceAdmin.Body.String(), "reindex-repository") {
		t.Fatalf("source admin status=%d body=%s", sourceAdmin.Code, sourceAdmin.Body.String())
	}
}

func TestAuthorizationHeadersAreMaskedAndPreserved(t *testing.T) {
	value := map[string]any{"headers": map[string]any{"Authorization": "Bearer secret", "X-API-Key": "key-secret", "X-Tenant": "tenant-a"}}
	maskSecrets(value)
	headers := value["headers"].(map[string]any)
	if headers["Authorization"] != "********" || headers["X-API-Key"] != "********" || headers["X-Tenant"] != "tenant-a" {
		t.Fatalf("unexpected masked headers: %#v", headers)
	}
	previous := map[string]any{"headers": map[string]any{"Authorization": "Bearer old", "X-API-Key": "old-key"}}
	preserveMasked(previous, value)
	if headers["Authorization"] != "Bearer old" || headers["X-API-Key"] != "old-key" {
		t.Fatalf("masked headers were not preserved: %#v", headers)
	}
}

func TestPublicUIConfigExposesOnlyBranding(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:public-ui?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	put := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/ui", bytes.NewBufferString(`{"serviceName":"Internal Context","tagline":"개발 지식","logoUrl":"/logo.svg","faviconUrl":"https://assets.company/favicon.svg","notice":"점검 공지","apiToken":"must-not-leak"}`))
	put.Header.Set("Authorization", "Bearer bootstrap")
	put.Header.Set("Content-Type", "application/json")
	putResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(putResult, put)
	if putResult.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", putResult.Code, putResult.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/public/config", nil)
	getResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(getResult, get)
	if getResult.Code != http.StatusOK || strings.Contains(getResult.Body.String(), "must-not-leak") || strings.Contains(getResult.Body.String(), "apiToken") {
		t.Fatalf("unsafe public config status=%d body=%s", getResult.Code, getResult.Body.String())
	}
	var got map[string]any
	if err = json.Unmarshal(getResult.Body.Bytes(), &got); err != nil || got["serviceName"] != "Internal Context" || got["notice"] != "점검 공지" || got["version"] != version.Version {
		t.Fatalf("public config=%#v err=%v", got, err)
	}
	if validatePublicAssetURL("//evil.example/logo.svg") == nil || validatePublicAssetURL("http://evil.example/logo.svg") == nil {
		t.Fatal("unsafe public asset URL accepted")
	}
}

func TestAdminAndStaticUIAreNeverCachedAcrossUpgrades(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:ui-cache?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, path := range []string{"/admin", "/app.js", "/roles.js", "/app.css"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		a.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control=%q, want no-store", path, got)
		}
	}
}

func TestSettingCRUDAndMaskPreservation(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		ListenAddress: ":0", DatabaseDriver: "sqlite", DatabaseDSN: "file:app-settings?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer bootstrap")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		return rec
	}
	rec := request(http.MethodPut, "/api/v1/admin/settings/ui", `{"serviceName":"A","apiToken":"secret-one"}`)
	if rec.Code != 200 {
		t.Fatalf("v1 status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(http.MethodGet, "/api/v1/admin/settings/ui", "")
	var got struct {
		Value        map[string]any `json:"value"`
		Version      int            `json:"version"`
		UpdatedBy    string         `json:"updatedBy"`
		UpdatedAt    time.Time      `json:"updatedAt"`
		MaskedFields []string       `json:"maskedFields"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Value["serviceName"] != "A" || got.Value["apiToken"] != "********" || got.Version != 1 || got.UpdatedBy != "bootstrap-admin" || got.UpdatedAt.IsZero() || !slices.Contains(got.MaskedFields, "apiToken") {
		t.Fatalf("stored setting was not fully represented: %#v", got)
	}
	rec = request(http.MethodPut, "/api/v1/admin/settings/ui", `{"serviceName":"B","apiToken":"********"}`)
	if rec.Code != 200 {
		t.Fatalf("v2 status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := a.loadSettingMap(context.Background(), "ui")
	if err != nil {
		t.Fatal(err)
	}
	if stored["apiToken"] != "secret-one" {
		t.Fatalf("masked update replaced secret: %#v", stored)
	}
	rec = request(http.MethodDelete, "/api/v1/admin/settings/ui", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(http.MethodGet, "/api/v1/admin/settings/ui", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted setting remained status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSourceSettingsReturnAllNonSecretValuesAndSecretPresence(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:source-setting-display?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	configs := map[string]map[string]any{
		"bitbucket": {
			"baseUrl": "https://bitbucket.internal", "apiPrefix": "/rest/api/1.0",
			"pat": "bitbucket-secret", "tlsVerify": true, "timeoutSeconds": float64(27),
		},
		"gitlab": {
			"baseUrl": "https://gitlab.internal", "token": "gitlab-secret",
			"tlsVerify": false, "timeoutSeconds": float64(19),
		},
	}
	for category, value := range configs {
		raw, _ := json.Marshal(value)
		sealed, sealErr := a.seal(raw)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		_, err = a.store.DB.Exec(a.store.Rebind(`INSERT INTO system_settings(category,version,value_encrypted,updated_by,updated_at) VALUES(?,?,?,?,?)`), category, 3, sealed, "source-admin-user", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/"+category, nil)
		req.Header.Set("Authorization", "Bearer bootstrap")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", category, rec.Code, rec.Body.String())
		}
		var response struct {
			Value        map[string]any `json:"value"`
			Version      int            `json:"version"`
			UpdatedBy    string         `json:"updatedBy"`
			MaskedFields []string       `json:"maskedFields"`
		}
		if err = json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		secretField := map[string]string{"bitbucket": "pat", "gitlab": "token"}[category]
		if response.Value["baseUrl"] != value["baseUrl"] || response.Value["timeoutSeconds"] != value["timeoutSeconds"] ||
			response.Value[secretField] != "********" || response.Version != 3 || response.UpdatedBy != "source-admin-user" ||
			!slices.Contains(response.MaskedFields, secretField) {
			t.Fatalf("%s response=%#v", category, response)
		}
	}
}

func TestForwardedIPRequiresConfiguredTrustedProxy(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:trusted-proxy?mode=memory&cache=shared&_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "https://git-ctx.company"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "10.20.30.40")
	if got := a.requestIP(req); got != "192.0.2.10" {
		t.Fatalf("untrusted forwarded IP accepted: %s", got)
	}
	raw := []byte(`{"trustedProxyCidrs":["192.0.2.0/24"]}`)
	sealed, err := a.seal(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.store.DB.Exec(`INSERT INTO system_settings(category,version,value_encrypted,updated_by) VALUES('security',1,?,'test')`, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.requestIP(req); got != "10.20.30.40" {
		t.Fatalf("trusted forwarded IP rejected: %s", got)
	}
}

// The index diagnostics must name a cause and an action for every state an
// operator can land in, because "not indexed" alone is not actionable.
func TestIndexStateExplainsEveryBlockedCase(t *testing.T) {
	now := time.Now().UTC()
	stalled := sql.NullTime{Time: now.Add(-30 * time.Minute), Valid: true}
	fresh := sql.NullTime{Time: now.Add(-time.Minute), Valid: true}
	for _, testCase := range []struct {
		name              string
		chunks            int
		status, message   string
		files             int
		startedAt         sql.NullTime
		wantState         string
		wantDetailKeyword string
		wantAction        bool
	}{
		{"no job", 0, "", "", 0, sql.NullTime{}, "never-run", "생성되지", true},
		{"failed", 0, "failed", "embedding endpoint rejected a probe request", 0, fresh, "failed", "probe", true},
		{"stalled", 0, "running", "", 3, stalled, "stalled", "분째", true},
		{"running", 0, "running", "", 3, fresh, "indexing", "진행", true},
		{"queued", 0, "pending", "", 0, sql.NullTime{}, "queued", "대기", true},
		{"policy matched nothing", 0, "completed", "12 file(s) listed but none matched the index policy", 0, fresh, "empty", "none matched the index policy", true},
		{"partial", 7, "completed", "1 file(s) skipped: gone.md", 7, fresh, "partial", "건너뛰", true},
		{"indexed", 7, "completed", "", 7, fresh, "indexed", "검색 가능", false},
	} {
		state, detail, action := indexState(testCase.chunks, testCase.status, testCase.message, testCase.files, testCase.startedAt, now)
		if state != testCase.wantState {
			t.Errorf("%s: state=%s want %s", testCase.name, state, testCase.wantState)
		}
		if !strings.Contains(detail, testCase.wantDetailKeyword) {
			t.Errorf("%s: detail=%q must mention %q", testCase.name, detail, testCase.wantDetailKeyword)
		}
		if testCase.wantAction != (action != "") {
			t.Errorf("%s: action=%q", testCase.name, action)
		}
	}
}

// MCP analytics has to answer an operator's real questions, and the audit view
// has to reconstruct one credential's calls. Both are built from the same rows,
// so this covers the aggregate, the recommendation it produces, and the export.
func TestMCPAnalyticsAndCallAudit(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "mcp-analytics.db") + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Now().UTC()
	insert := func(id, tool, outcome string, latency, bytes int, truncated int, preview, mode, code string, age time.Duration) {
		t.Helper()
		if _, err := a.store.DB.Exec(`INSERT INTO mcp_calls(id,occurred_at,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,arguments_hash,result_count,cache_hit,error_code,retrieval_mode)
VALUES(?,?,'u1','KEY123',?,'/kcb/clustara',?,?,'10.0.0.1',?,?,'sess01234567','req-1','claude-code','1.2',?,?,3,0,?,?)`,
			id, now.Add(-age), tool, outcome, latency, bytes, truncated, preview, "h-"+preview, code, mode); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 10; index++ {
		insert(fmt.Sprintf("c%d", index), "search-code", "success", 100*(index+1), 40000, 1, "query=gpu", "index", "", time.Hour)
	}
	for index := 0; index < 6; index++ {
		insert(fmt.Sprintf("e%d", index), "find-file", "empty", 50, 200, 0, "name=Jenkinsfile", "indexing", "", time.Hour)
	}
	insert("x1", "find-file", "error", 30000, 0, 0, "name=broken", "", "timeout", time.Hour)
	// Outside the 24h window, so it must not be counted there.
	insert("old", "search-code", "success", 10, 100, 0, "query=old", "index", "", 72*time.Hour)

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer bootstrap")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	response := get("/api/v1/admin/mcp/analytics?window=24h")
	if response.Code != http.StatusOK {
		t.Fatalf("analytics=%d body=%s", response.Code, response.Body.String())
	}
	var analytics struct {
		Summary struct {
			Calls, Empty, Errors, Truncated int64
		} `json:"summary"`
		Tools []struct {
			Tool                       string `json:"tool"`
			Calls, P95LatencyMS, Empty int64
			BudgetBytes                int `json:"responseBudgetBytes"`
		} `json:"tools"`
		Recommendations []struct {
			Tool, Severity, Message, Field string
			Value                          int
		} `json:"recommendations"`
		Unanswered []struct {
			Tool, Arguments string
			Calls           int64
		} `json:"unanswered"`
		Retrieval, Errors, Clients []struct {
			Label string
			Calls int64
		}
		Timeline []struct {
			Bucket string
			Calls  int64
		} `json:"timeline"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &analytics); err != nil {
		t.Fatal(err)
	}
	if analytics.Summary.Calls != 17 || analytics.Summary.Empty != 6 || analytics.Summary.Errors != 1 || analytics.Summary.Truncated != 10 {
		t.Fatalf("the window must exclude the older call: %#v", analytics.Summary)
	}
	var searchCode struct {
		p95, calls int64
	}
	for _, tool := range analytics.Tools {
		if tool.Tool == "search-code" {
			searchCode.p95, searchCode.calls = tool.P95LatencyMS, tool.Calls
		}
	}
	if searchCode.calls != 10 || searchCode.p95 != 900 {
		t.Fatalf("per-tool percentiles are wrong: %#v", analytics.Tools)
	}
	// Every answer of search-code was truncated and most find-file calls were
	// empty, so both must be reported with an actionable field.
	var budgetAdvice, emptyAdvice bool
	for _, item := range analytics.Recommendations {
		if item.Tool == "search-code" && item.Field == "maxResponseBytes" && item.Value > 0 {
			budgetAdvice = true
		}
		if item.Tool == "find-file" && item.Severity == "critical" {
			emptyAdvice = true
		}
	}
	if !budgetAdvice || !emptyAdvice {
		t.Fatalf("recommendations=%#v", analytics.Recommendations)
	}
	if len(analytics.Unanswered) == 0 || analytics.Unanswered[0].Arguments != "name=Jenkinsfile" || analytics.Unanswered[0].Calls != 6 {
		t.Fatalf("the unanswered list must rank the repeated empty question: %#v", analytics.Unanswered)
	}
	if len(analytics.Timeline) == 0 || len(analytics.Clients) == 0 || analytics.Clients[0].Label != "claude-code 1.2" {
		t.Fatalf("timeline=%#v clients=%#v", analytics.Timeline, analytics.Clients)
	}

	filtered := get("/api/v1/admin/mcp/calls?window=24h&tool=find-file&outcome=error")
	if filtered.Code != http.StatusOK {
		t.Fatalf("calls=%d body=%s", filtered.Code, filtered.Body.String())
	}
	var audit struct {
		Total int64
		Items []struct {
			Tool, Outcome, ErrorCode, SessionID, Client, Arguments string
			Truncated                                              bool
		}
	}
	if err := json.Unmarshal(filtered.Body.Bytes(), &audit); err != nil {
		t.Fatal(err)
	}
	if audit.Total != 1 || len(audit.Items) != 1 || audit.Items[0].ErrorCode != "timeout" || audit.Items[0].SessionID != "sess01234567" {
		t.Fatalf("audit filter=%#v", audit)
	}

	export := get("/api/v1/admin/mcp/calls?window=24h&tool=search-code&format=csv")
	if export.Code != http.StatusOK || !strings.HasPrefix(export.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv export=%d type=%s", export.Code, export.Header().Get("Content-Type"))
	}
	if lines := strings.Count(strings.TrimSpace(export.Body.String()), "\n"); lines != 10 {
		t.Fatalf("csv rows=%d body=%s", lines, export.Body.String())
	}
	if !strings.HasPrefix(export.Body.String(), "call_id,occurred_at") || !strings.Contains(export.Body.String(), "query=gpu") {
		t.Fatalf("csv body=%s", export.Body.String())
	}
}

// The X-ray view has to show the stages of one call and the sequence the agent
// followed around it, and must never show another user their neighbour's calls.
func TestMCPCallTraceAndSessionSequence(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "mcp-trace.db") + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Now().UTC()
	call := func(id, tool, outcome string, results int, age time.Duration, summary string) {
		t.Helper()
		if _, err := a.store.DB.Exec(`INSERT INTO mcp_calls(id,occurred_at,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,arguments_hash,result_count,cache_hit,error_code,retrieval_mode,trace_summary)
VALUES(?,?,'u1','KEY123',?,'',?,120,'10.0.0.1',900,0,'sessAAAAAAAA','req-1','claude-code','1.2','query=webhook','h1',?,0,'','index',?)`, id, now.Add(-age), tool, outcome, results, summary); err != nil {
			t.Fatal(err)
		}
	}
	call("call-1", "search-code", "empty", 0, 2*time.Minute, "source-query gitlab: 12 candidates, none passed")
	call("call-2", "search-semantic", "success", 3, time.Minute, "")
	// Another user's call in another session must stay invisible below.
	if _, err := a.store.DB.Exec(`INSERT INTO mcp_calls(id,occurred_at,user_id,tool,outcome,duration_ms,client_ip,session_id) VALUES('call-x',?, 'u2','search-code','success',10,'10.0.0.9','sessBBBBBBBB')`, now); err != nil {
		t.Fatal(err)
	}
	steps := [][]any{
		{1, "acl", "restricted", "ok", "2 principals", 0, 0, 1, 0},
		{2, "index-repositories", "gitlab", "empty", "", 0, 0, 8, 1},
		{3, "source-query", "gitlab", "empty", "indexed and remote per-repository search", 12, 0, 90, 9},
	}
	for _, step := range steps {
		if _, err := a.store.DB.Exec(`INSERT INTO mcp_call_steps(call_id,sequence,stage,target,status,detail,candidates,results,duration_ms,offset_ms) VALUES('call-1',?,?,?,?,?,?,?,?,?)`, step...); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/mcp/calls/call-1", nil)
	request.Header.Set("Authorization", "Bearer bootstrap")
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("trace=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var trace struct {
		Call struct {
			Tool, Outcome, TraceSummary string
			DurationMs, TracedMs        int64
			UntracedMs                  int64
		}
		Steps []struct {
			Sequence              int64
			Stage, Status, Target string
			Candidates, Results   int64
			DurationMs, OffsetMs  int64
		}
		SessionSequence []struct {
			ID, Tool, Outcome string
			Current           bool
		}
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if len(trace.Steps) != 3 || trace.Steps[2].Stage != "source-query" || trace.Steps[2].Candidates != 12 || trace.Steps[2].Results != 0 {
		t.Fatalf("steps=%#v", trace.Steps)
	}
	if trace.Call.TracedMs != 99 || trace.Call.UntracedMs != 21 {
		t.Fatalf("the untraced remainder must be reported: traced=%d untraced=%d", trace.Call.TracedMs, trace.Call.UntracedMs)
	}
	if trace.Call.TraceSummary == "" {
		t.Fatal("the summary must state where the results were lost")
	}
	if len(trace.SessionSequence) != 2 || !trace.SessionSequence[0].Current || trace.SessionSequence[1].Tool != "search-semantic" {
		t.Fatalf("the session sequence must show what the agent did next: %#v", trace.SessionSequence)
	}

	// The personal route only opens the caller's own calls.
	own := httptest.NewRequest(http.MethodGet, "/api/v1/me/calls/call-x", nil)
	own.Header.Set("Authorization", "Bearer bootstrap")
	ownRecorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(ownRecorder, own)
	if ownRecorder.Code != http.StatusNotFound {
		t.Fatalf("another user's call must not be readable: %d %s", ownRecorder.Code, ownRecorder.Body.String())
	}
}

// A conversation is the unit that succeeds or fails, not a single call, and the
// self-check has to exercise the real retrieval path rather than the settings.
func TestMCPSessionsAndSelfCheck(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "mcp-sessions.db") + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Now().UTC()
	insert := func(id, session, tool, outcome string, age time.Duration) {
		t.Helper()
		if _, err := a.store.DB.Exec(`INSERT INTO mcp_calls(id,occurred_at,user_id,tool,outcome,duration_ms,client_ip,response_bytes,session_id,client_name,client_version,arguments_preview,result_count)
VALUES(?,?,'u1',?,?,40,'10.0.0.1',800,?,'claude-code','2.1','query=결제 재시도',1)`, id, now.Add(-age), tool, outcome, session); err != nil {
			t.Fatal(err)
		}
	}
	// One conversation that found its answer, one that gave up.
	insert("s1-a", "resolved0001", "search-code", "empty", 10*time.Minute)
	insert("s1-b", "resolved0001", "search-semantic", "success", 9*time.Minute)
	insert("s2-a", "givenup00001", "search-code", "empty", 5*time.Minute)
	insert("s2-b", "givenup00001", "find-file", "empty", 4*time.Minute)

	get := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	response := get(http.MethodGet, "/api/v1/admin/mcp/sessions?window=24h", "")
	if response.Code != http.StatusOK {
		t.Fatalf("sessions=%d body=%s", response.Code, response.Body.String())
	}
	var sessions struct {
		Sessions []struct {
			SessionID, Client, LastOutcome, LastCallID string
			ToolChain                                  []string
			Calls, Empty                               int64
			Unresolved                                 bool
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Sessions) != 2 {
		t.Fatalf("sessions=%#v", sessions.Sessions)
	}
	// The conversation that ended without an answer is ranked first.
	first := sessions.Sessions[0]
	if first.SessionID != "givenup00001" || !first.Unresolved || first.LastCallID != "s2-b" {
		t.Fatalf("unresolved sessions must rank first: %#v", sessions.Sessions)
	}
	if strings.Join(first.ToolChain, "→") != "search-code→find-file" {
		t.Fatalf("tool chain=%v", first.ToolChain)
	}
	if sessions.Sessions[1].Unresolved {
		t.Fatalf("the resolved conversation must not be flagged: %#v", sessions.Sessions[1])
	}

	check := get(http.MethodPost, "/api/v1/admin/mcp/selfcheck", `{"query":"결제 재시도"}`)
	if check.Code != http.StatusOK {
		t.Fatalf("selfcheck=%d body=%s", check.Code, check.Body.String())
	}
	var result struct {
		Verdict string
		Checks  []struct {
			Name, Status, Detail, Action string
			Steps                        []struct{ Stage, Status string }
		}
	}
	if err := json.Unmarshal(check.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	// An empty deployment must fail loudly with an action, not report success.
	if result.Verdict != "fail" {
		t.Fatalf("verdict=%s checks=%#v", result.Verdict, result.Checks)
	}
	names := map[string]string{}
	for _, item := range result.Checks {
		names[item.Name] = item.Status
	}
	for _, expected := range []string{"저장소 카탈로그", "ACL 주체", "코드 검색 (search-code)", "파일명 검색 (find-file)", "의미 검색 (search-semantic)"} {
		if _, ok := names[expected]; !ok {
			t.Fatalf("check %q missing: %#v", expected, names)
		}
	}
	var traced bool
	for _, item := range result.Checks {
		if len(item.Steps) > 0 {
			traced = true
		}
		if item.Status != "ok" && item.Action == "" {
			t.Fatalf("a failing check must say what to do: %#v", item)
		}
	}
	if !traced {
		t.Fatal("the self-check must return the stage trace of the retrieval it ran")
	}
}

// Deleting a setting leaves its version history behind, so the next save has to
// continue the numbering. Restarting at 1 collides with the history row that is
// still there and made a perfectly normal "delete, then configure again" fail.
func TestSettingSaveAfterDeleteContinuesVersionHistory(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "settings-version.db") + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/logging", `{"level":"debug"}`); saved.Code != http.StatusOK {
		t.Fatalf("first save=%d body=%s", saved.Code, saved.Body.String())
	}
	if deleted := call(http.MethodDelete, "/api/v1/admin/settings/logging", ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d body=%s", deleted.Code, deleted.Body.String())
	}
	again := call(http.MethodPut, "/api/v1/admin/settings/logging", `{"level":"info"}`)
	if again.Code != http.StatusOK {
		t.Fatalf("save after delete=%d body=%s", again.Code, again.Body.String())
	}
	if !strings.Contains(again.Body.String(), `"version":2`) {
		t.Fatalf("the version must continue the history: %s", again.Body.String())
	}
	versions := call(http.MethodGet, "/api/v1/admin/settings/logging/versions", "")
	if !strings.Contains(versions.Body.String(), `"version":2`) || !strings.Contains(versions.Body.String(), `"version":1`) {
		t.Fatalf("history=%s", versions.Body.String())
	}
}

// Settings history is only useful if a previous configuration can be read and
// put back, and two administrators must not silently overwrite each other.
func TestSettingVersionDiffRestoreAndConflict(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "settings-restore.db") + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/search", `{"keywordWeight":1,"vectorWeight":0.35,"finalDocuments":8}`); saved.Code != http.StatusOK {
		t.Fatalf("v1=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/search", `{"keywordWeight":2,"vectorWeight":0.9,"finalDocuments":20}`); saved.Code != http.StatusOK {
		t.Fatalf("v2=%d body=%s", saved.Code, saved.Body.String())
	}

	// Reading v1 shows what restoring it would change.
	detail := call(http.MethodGet, "/api/v1/admin/settings/search/versions/1", "")
	if detail.Code != http.StatusOK {
		t.Fatalf("version detail=%d body=%s", detail.Code, detail.Body.String())
	}
	var version struct {
		Version, CurrentVersion int
		Value                   map[string]any
		Changes                 []struct {
			Field, Kind, Before, After string
			Secret                     bool
		}
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version.CurrentVersion != 2 || len(version.Changes) == 0 {
		t.Fatalf("diff=%#v", version)
	}
	var weight bool
	for _, change := range version.Changes {
		if change.Field == "vectorWeight" && change.Kind == "changed" && strings.HasPrefix(change.After, "0.35") {
			weight = true
		}
	}
	if !weight {
		t.Fatalf("the diff must state the target value: %#v", version.Changes)
	}

	// Restoring appends a new version rather than rewriting history.
	restored := call(http.MethodPost, "/api/v1/admin/settings/search/versions/1/restore", "")
	if restored.Code != http.StatusOK || !strings.Contains(restored.Body.String(), `"version":3`) {
		t.Fatalf("restore=%d body=%s", restored.Code, restored.Body.String())
	}
	current := call(http.MethodGet, "/api/v1/admin/settings/search", "")
	if !strings.Contains(current.Body.String(), `"vectorWeight":0.35`) {
		t.Fatalf("restored value=%s", current.Body.String())
	}

	// A save that carries a stale version is refused instead of clobbering.
	stale := call(http.MethodPut, "/api/v1/admin/settings/search", `{"keywordWeight":5,"expectedVersion":2}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale save=%d body=%s", stale.Code, stale.Body.String())
	}
	fresh := call(http.MethodPut, "/api/v1/admin/settings/search", `{"keywordWeight":5,"vectorWeight":0.35,"finalDocuments":8,"expectedVersion":3}`)
	if fresh.Code != http.StatusOK {
		t.Fatalf("fresh save=%d body=%s", fresh.Code, fresh.Body.String())
	}
	if strings.Contains(call(http.MethodGet, "/api/v1/admin/settings/search", "").Body.String(), "expectedVersion") {
		t.Fatal("expectedVersion must not be stored as part of the setting")
	}
}

// A source server in a maintenance window must not stop an administrator from
// saving a configuration, but the skipped validation has to be visible rather
// than silently accepted.
func TestSettingForceSaveRecordsSkippedValidation(t *testing.T) {
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "settings-force.db") + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	// Nothing listens on this port, so validation fails.
	setting := `{"provider":"milvus","baseUrl":"http://127.0.0.1:9","collection":"git_ctx_chunk_vectors","dimensions":256,"timeoutSeconds":1}`
	refused := call("/api/v1/admin/settings/vector", setting)
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "setting_validation_failed") {
		t.Fatalf("unreachable target must be refused by default: %d %s", refused.Code, refused.Body.String())
	}
	forced := call("/api/v1/admin/settings/vector?force=true", setting)
	if forced.Code != http.StatusOK {
		t.Fatalf("forced save=%d body=%s", forced.Code, forced.Body.String())
	}
	if !strings.Contains(forced.Body.String(), "validationSkipped") || !strings.Contains(forced.Body.String(), "warning") {
		t.Fatalf("a forced save must report what it skipped: %s", forced.Body.String())
	}
	var audited int
	if err := a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='settings.update' AND metadata LIKE '%validationSkipped%'`).Scan(&audited); err != nil || audited == 0 {
		t.Fatalf("the skip must be auditable: %d err=%v", audited, err)
	}
}

// An export has to be safe to carry between environments: no credentials in the
// file, a dry run that says what would change, and an import that keeps the
// target's own secrets.
func TestSettingsExportImportRoundTrip(t *testing.T) {
	newApp := func(name string) *App {
		t.Helper()
		instance, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), name) + "?_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(t.TempDir(), "backups")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(instance.Close)
		return instance
	}
	call := func(instance *App, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		instance.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	source := newApp("export-source.db")
	if saved := call(source, http.MethodPut, "/api/v1/admin/settings/search", `{"keywordWeight":3,"vectorWeight":0.5,"finalDocuments":12}`); saved.Code != http.StatusOK {
		t.Fatalf("search=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(source, http.MethodPut, "/api/v1/admin/settings/notifications", `{"enabled":true,"webhookUrl":"https://hooks.internal/x","apiToken":"super-secret-token"}`); saved.Code != http.StatusOK {
		t.Fatalf("notifications=%d body=%s", saved.Code, saved.Body.String())
	}

	exported := call(source, http.MethodGet, "/api/v1/admin/settings-export", "")
	if exported.Code != http.StatusOK {
		t.Fatalf("export=%d body=%s", exported.Code, exported.Body.String())
	}
	document := exported.Body.String()
	if strings.Contains(document, "super-secret-token") {
		t.Fatal("an export must never contain a credential")
	}
	if !strings.Contains(document, `"keywordWeight":3`) || !strings.Contains(document, "********") {
		t.Fatalf("export=%s", document)
	}

	target := newApp("export-target.db")
	preview := call(target, http.MethodPost, "/api/v1/admin/settings-import", document)
	if preview.Code != http.StatusOK {
		t.Fatalf("dry run=%d body=%s", preview.Code, preview.Body.String())
	}
	var result struct {
		DryRun  bool
		Applied int
		Results []struct {
			Category, Status, Detail string
			Changes                  []struct{ Field, Kind string }
		}
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Applied != 0 {
		t.Fatalf("a dry run must not change anything: %#v", result)
	}
	status := map[string]string{}
	for _, item := range result.Results {
		status[item.Category] = item.Status
	}
	if status["search"] != "ready" {
		t.Fatalf("dry run=%#v", result.Results)
	}
	// Nothing was written yet.
	if call(target, http.MethodGet, "/api/v1/admin/settings/search", "").Code != http.StatusNotFound {
		t.Fatal("the dry run wrote a setting")
	}

	applied := call(target, http.MethodPost, "/api/v1/admin/settings-import?apply=true", document)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply=%d body=%s", applied.Code, applied.Body.String())
	}
	loaded := call(target, http.MethodGet, "/api/v1/admin/settings/search", "")
	if !strings.Contains(loaded.Body.String(), `"keywordWeight":3`) || !strings.Contains(loaded.Body.String(), `"version":1`) {
		t.Fatalf("imported value=%s", loaded.Body.String())
	}
	// The masked secret must not have been stored literally.
	notifications, err := target.loadSettingMapRaw(context.Background(), "notifications")
	if err == nil {
		if token, _ := notifications["apiToken"].(string); token == "********" {
			t.Fatal("the mask was imported as the secret itself")
		}
	}
	// Importing the same document twice reports no change rather than churning
	// the version history.
	second := call(target, http.MethodPost, "/api/v1/admin/settings-import?apply=true", document)
	if !strings.Contains(second.Body.String(), "unchanged") {
		t.Fatalf("second import=%s", second.Body.String())
	}
}

// The administrator picks the vector database in settings, and the platform has
// to actually use the one they picked. The existing coverage only proved an
// unreachable target is refused; this walks the path an operator takes when the
// target is real.
func TestAdminSelectsMilvusAsTheVectorDatabase(t *testing.T) {
	var listed, statted bool
	milvus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/collections/list"):
			listed = true
			_, _ = w.Write([]byte(`{"code":0,"data":["git_ctx_chunk_vectors"]}`))
		case strings.HasSuffix(r.URL.Path, "/collections/get_stats"):
			statted = true
			_, _ = w.Write([]byte(`{"code":0,"data":{"rowCount":"42"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer milvus.Close()

	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "vector.db") + "?_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32),
		BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
		BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	// Saving is what runs the connection test, so a 200 here means the platform
	// reached the Milvus the administrator named.
	setting := `{"provider":"milvus","baseUrl":"` + milvus.URL + `","collection":"git_ctx_chunk_vectors","dimensions":256,"timeoutSeconds":5}`
	saved := call(http.MethodPut, "/api/v1/admin/settings/vector", setting)
	if saved.Code != http.StatusOK {
		t.Fatalf("save=%d body=%s", saved.Code, saved.Body.String())
	}
	if !listed {
		t.Fatal("saving the setting did not test the connection")
	}

	// The status screen must now report Milvus rather than the built-in store.
	status := call(http.MethodGet, "/api/v1/admin/vector/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
	var view struct {
		Provider   string `json:"provider"`
		Collection string `json:"collection"`
		Ready      bool   `json:"ready"`
		Vectors    int64  `json:"vectors"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status: %v body=%s", err, status.Body.String())
	}
	if view.Provider != "milvus" || !view.Ready {
		t.Fatalf("status did not report the selected provider: %#v body=%s", view, status.Body.String())
	}
	if view.Vectors != 42 || !statted {
		t.Errorf("status did not read the collection size: %#v", view)
	}

	// Switching back must be equally available, or an operator cannot undo it.
	off := call(http.MethodPut, "/api/v1/admin/settings/vector", `{"provider":"none"}`)
	if off.Code != http.StatusOK {
		t.Fatalf("switching back=%d body=%s", off.Code, off.Body.String())
	}
	after := call(http.MethodGet, "/api/v1/admin/vector/status", "")
	if strings.Contains(after.Body.String(), `"provider":"milvus"`) {
		t.Errorf("still reporting milvus after switching off: %s", after.Body.String())
	}
}

// When semantic search stops working an operator has to know which half broke.
// The vector database was already probed live on every status read; the model
// endpoint was only ever reached when the setting was saved, so a model that
// died afterwards looked identical to a healthy one.
func TestVectorStatusProbeSeparatesModelFailureFromVectorFailure(t *testing.T) {
	modelUp := true
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !modelUp {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"model is loading"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3,0.4]}]}`))
	}))
	defer model.Close()

	milvus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/collections/list"):
			_, _ = w.Write([]byte(`{"code":0,"data":["git_ctx_chunk_vectors"]}`))
		case strings.HasSuffix(r.URL.Path, "/collections/get_stats"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"rowCount":"7"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer milvus.Close()

	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "probe.db") + "?_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32),
		BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
		BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	if saved := call(http.MethodPut, "/api/v1/admin/settings/model",
		`{"provider":"openai-compatible","baseUrl":"`+model.URL+`","model":"m","dimensions":4,"timeoutSeconds":5}`); saved.Code != http.StatusOK {
		t.Fatalf("model save=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/vector",
		`{"provider":"milvus","baseUrl":"`+milvus.URL+`","collection":"git_ctx_chunk_vectors","dimensions":4,"timeoutSeconds":5}`); saved.Code != http.StatusOK {
		t.Fatalf("vector save=%d body=%s", saved.Code, saved.Body.String())
	}

	probe := func() (probeOK, vectorReady bool, body string) {
		t.Helper()
		recorder := call(http.MethodGet, "/api/v1/admin/vector/status?probe=true", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var view struct {
			Ready          bool `json:"ready"`
			EmbeddingProbe struct {
				OK    bool   `json:"ok"`
				Stage string `json:"stage"`
			} `json:"embeddingProbe"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode: %v body=%s", err, recorder.Body.String())
		}
		return view.EmbeddingProbe.OK, view.Ready, recorder.Body.String()
	}

	if modelOK, vectorOK, body := probe(); !modelOK || !vectorOK {
		t.Fatalf("both healthy but reported model=%v vector=%v: %s", modelOK, vectorOK, body)
	}

	// The model dies. The vector database is untouched, and the two must now
	// read differently.
	modelUp = false
	modelOK, vectorOK, body := probe()
	if modelOK {
		t.Errorf("a dead model endpoint still probed healthy: %s", body)
	}
	if !vectorOK {
		t.Errorf("the vector database was reported broken when only the model died: %s", body)
	}

	// Without the flag the probe is skipped, so routine polling stays cheap.
	plain := call(http.MethodGet, "/api/v1/admin/vector/status", "")
	if strings.Contains(plain.Body.String(), "embeddingProbe") {
		t.Errorf("the probe ran without being asked for: %s", plain.Body.String())
	}
}

// A pack is now more than a list of repositories: it carries what it is for, a
// budget, the symbols worth anchoring on, and whether to pull in the files that
// say how the project is worked in. All of it has to survive a round trip, or
// the console silently drops half of what an operator entered.
func TestContextPackCarriesPurposeBudgetAndEntrypoints(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(t.TempDir(), "packs.db") + "?_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32),
		BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
		BackupDirectory: filepath.Join(t.TempDir(), "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	created := call(http.MethodPost, "/api/v1/admin/context-packs", `{
		"slug":"credit-api","name":"Credit API","description":"여신 도메인",
		"purpose":"onboarding","tokenBudget":30000,"includeConventions":false,
		"items":[{"libraryId":"/kcb/api","ref":"main","queryHint":"API"}],
		"entrypoints":[{"symbol":"CreditService","libraryId":"/kcb/api"},{"symbol":"CreditController"}]}`)
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("create=%d body=%s", created.Code, created.Body.String())
	}

	listed := call(http.MethodGet, "/api/v1/admin/context-packs", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list=%d body=%s", listed.Code, listed.Body.String())
	}
	var packs []struct {
		ID                 string `json:"id"`
		Purpose            string `json:"purpose"`
		TokenBudget        int    `json:"tokenBudget"`
		IncludeConventions bool   `json:"includeConventions"`
		Entrypoints        []struct {
			Symbol    string `json:"symbol"`
			LibraryID string `json:"libraryId"`
		} `json:"entrypoints"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &packs); err != nil || len(packs) != 1 {
		t.Fatalf("decode list: %v body=%s", err, listed.Body.String())
	}
	pack := packs[0]
	if pack.Purpose != "onboarding" || pack.TokenBudget != 30000 || pack.IncludeConventions {
		t.Errorf("pack lost its new fields: %#v", pack)
	}
	if len(pack.Entrypoints) != 2 || pack.Entrypoints[0].Symbol != "CreditService" || pack.Entrypoints[0].LibraryID != "/kcb/api" {
		t.Errorf("entrypoints = %#v", pack.Entrypoints)
	}

	// An out-of-range budget is refused rather than silently clamped: an
	// operator who typed 40 meant something, and it was not 40 bytes.
	rejected := call(http.MethodPut, "/api/v1/admin/context-packs/"+pack.ID, `{
		"slug":"credit-api","name":"Credit API","tokenBudget":40,
		"items":[{"libraryId":"/kcb/api"}]}`)
	if rejected.Code != http.StatusBadRequest {
		t.Errorf("a 40 byte budget was accepted: %d %s", rejected.Code, rejected.Body.String())
	}

	// Updating replaces the entrypoints rather than accumulating them.
	updated := call(http.MethodPut, "/api/v1/admin/context-packs/"+pack.ID, `{
		"slug":"credit-api","name":"Credit API","purpose":"feature-development",
		"items":[{"libraryId":"/kcb/api"}],"entrypoints":[{"symbol":"CreditRepository"}]}`)
	if updated.Code != http.StatusNoContent && updated.Code != http.StatusOK {
		t.Fatalf("update=%d body=%s", updated.Code, updated.Body.String())
	}
	listed = call(http.MethodGet, "/api/v1/admin/context-packs", "")
	if err := json.Unmarshal(listed.Body.Bytes(), &packs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(packs[0].Entrypoints) != 1 || packs[0].Entrypoints[0].Symbol != "CreditRepository" {
		t.Errorf("entrypoints were not replaced: %#v", packs[0].Entrypoints)
	}
	if packs[0].Purpose != "feature-development" || !packs[0].IncludeConventions {
		t.Errorf("update lost fields: %#v", packs[0])
	}
}

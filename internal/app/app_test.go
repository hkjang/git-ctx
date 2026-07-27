package app

import (
	"bytes"
	"context"
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
	}
	for _, test := range invalid {
		if err := a.validateSetting(context.Background(), test.category, test.value); err == nil {
			t.Fatalf("invalid %s setting was accepted: %#v", test.category, test.value)
		}
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
		KeyPepper: pepper, MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
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
	keyRequest.Header.Set("Content-Type", "application/json")
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
	if rec := test(); rec.Code != http.StatusOK {
		t.Fatalf("complete connection test=%d body=%s", rec.Code, rec.Body.String())
	}
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
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), `"latest":"017_admin_recovery_tokens.sql"`) || !strings.Contains(admin.Body.String(), `"pool"`) {
		t.Fatalf("admin status=%d body=%s", admin.Code, admin.Body.String())
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
		Value map[string]any `json:"value"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Value["apiToken"] != "********" {
		t.Fatalf("secret not masked: %#v", got.Value)
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

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
	"strings"
	"testing"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/config"
	"git-ctx/internal/observability"
)

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
	restored := request(http.MethodPost, "/api/v1/admin/backups/"+record.ID+"/restore", map[string]string{"X-Restore-Confirmation": "RESTORE " + record.ID, "X-Change-Reason": "integration test"})
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
		"security": "security-admin", "permissions": "security-admin",
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
	put.Header.Set("X-Change-Reason", "branding test")
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
	if err = json.Unmarshal(getResult.Body.Bytes(), &got); err != nil || got["serviceName"] != "Internal Context" || got["notice"] != "점검 공지" {
		t.Fatalf("public config=%#v err=%v", got, err)
	}
	if validatePublicAssetURL("//evil.example/logo.svg") == nil || validatePublicAssetURL("http://evil.example/logo.svg") == nil {
		t.Fatal("unsafe public asset URL accepted")
	}
}

func TestSettingMaskPreservationAndRollback(t *testing.T) {
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
		req.Header.Set("X-Change-Reason", "test")
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
	rec = request(http.MethodPost, "/api/v1/admin/settings/ui/rollback", `{"targetVersion":1,"reason":"restore test"}`)
	if rec.Code != 200 {
		t.Fatalf("rollback status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err = a.loadSettingMap(context.Background(), "ui")
	if err != nil {
		t.Fatal(err)
	}
	if stored["serviceName"] != "A" || stored["apiToken"] != "secret-one" {
		t.Fatalf("rollback value=%#v", stored)
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

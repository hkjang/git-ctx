package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git-ctx/internal/auth"
	"git-ctx/internal/config"
)

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
	var got map[string]string
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
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:trusted-proxy?mode=memory&cache=shared&_foreign_keys=on", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), PublicURL: "https://git-ctx.company"})
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

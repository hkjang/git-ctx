package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestKeycloakOIDCEndToEndSavePKCECallbackAndSession(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, &jose.SignerOptions{ExtraHeaders: map[jose.HeaderKey]any{"kid": "e2e"}})
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	var sawVerifier bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token",
				"jwks_uri": issuer + "/jwks", "end_session_endpoint": issuer + "/logout",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "e2e", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code") != "valid-code" || r.Form.Get("code_verifier") == "" {
				http.Error(w, "invalid token request", http.StatusBadRequest)
				return
			}
			sawVerifier = true
			now := time.Now()
			raw, signErr := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "kc-e2e", Audience: jwt.Audience{"git-ctx"}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now)}).Claims(map[string]any{
				"preferred_username": "oidc-admin", "email": "admin@example.test", "bitbucket_user_slug": "oidc.bb",
				"realm_access": map[string]any{"roles": []string{"git-ctx-admin"}},
			}).Serialize()
			if signErr != nil {
				http.Error(w, signErr.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL

	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:oidc-e2e?mode=memory&cache=shared&_foreign_keys=on&_busy_timeout=5000", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	setting := `{"issuerMode":"custom","issuerUrl":` + quoteJSON(issuer) + `,"clientId":"git-ctx","clientSecret":"client-secret","redirectMode":"custom","redirectUrl":"http://localhost:4747/auth/callback","postLogoutRedirectUrl":"http://localhost:4747/","realmRoleMappings":{"git-ctx-admin":"platform-admin"},"tlsVerify":true}`
	put := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/keycloak", strings.NewReader(setting))
	put.Header.Set("Authorization", "Bearer bootstrap")
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("X-Change-Reason", "OIDC e2e")
	putResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(putResult, put)
	if putResult.Code != http.StatusOK || !strings.Contains(putResult.Body.String(), `"applied":true`) {
		t.Fatalf("save=%d body=%s", putResult.Code, putResult.Body.String())
	}

	login := httptest.NewRequest(http.MethodGet, "/auth/login?return_to=/%23admin/keycloak", nil)
	loginResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(loginResult, login)
	if loginResult.Code != http.StatusFound {
		t.Fatalf("login=%d body=%s", loginResult.Code, loginResult.Body.String())
	}
	authorizeURL, err := url.Parse(loginResult.Header().Get("Location"))
	if err != nil || authorizeURL.Query().Get("code_challenge_method") != "S256" || authorizeURL.Query().Get("code_challenge") == "" {
		t.Fatalf("authorize URL=%s err=%v", authorizeURL, err)
	}
	state := authorizeURL.Query().Get("state")
	callback := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+url.QueryEscape(state)+"&code=valid-code", nil)
	callbackResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(callbackResult, callback)
	if callbackResult.Code != http.StatusFound || callbackResult.Header().Get("Location") != "/#admin/keycloak" || !sawVerifier {
		t.Fatalf("callback=%d verifier=%v body=%s", callbackResult.Code, sawVerifier, callbackResult.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range callbackResult.Result().Cookies() {
		if cookie.Name == "git_ctx_session" {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session=%#v", session)
	}
	me := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	me.AddCookie(session)
	meResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(meResult, me)
	if meResult.Code != http.StatusOK || !strings.Contains(meResult.Body.String(), "oidc-admin") || !strings.Contains(meResult.Body.String(), "platform-admin") {
		t.Fatalf("me=%d body=%s", meResult.Code, meResult.Body.String())
	}
	if a.bootstrapAdminToken() != "" {
		t.Fatal("bootstrap token remained active after verified platform-admin OIDC login")
	}
}

func quoteJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

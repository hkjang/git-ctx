package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestOIDCVerifierMapsKeycloakClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/auth",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
				"end_session_endpoint":                  issuer + "/logout",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, &jose.SignerOptions{ExtraHeaders: map[jose.HeaderKey]any{"kid": "test"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "kc-1", Audience: jwt.Audience{"git-ctx"}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now)}).Claims(map[string]any{
		"preferred_username": "alice", "email": "alice@example.test", "groups": []string{"/engineering", "/readonly"},
		"bitbucket_user_slug": "alice.bb", "gitlab_user_id": "42",
		"realm_access":    map[string]any{"roles": []string{"ctx-admin", "readonly-operator"}},
		"resource_access": map[string]any{"git-ctx": map[string]any{"roles": []string{"ctx-audit"}}},
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewOIDCVerifier(func(context.Context) (OIDCConfig, error) {
		return OIDCConfig{
			IssuerURL: issuer, ClientID: "git-ctx",
			RealmRoleMappings:      map[string]string{"ctx-admin": "platform-admin"},
			ClientRoleMappings:     map[string]string{"ctx-audit": "auditor"},
			BitbucketGroupMappings: map[string]string{"/engineering": "engineering"},
		}, nil
	})
	id, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "kc-1" || id.BitbucketUserSlug != "alice.bb" || id.GitLabUserID != "42" {
		t.Fatalf("identity=%#v", id)
	}
	if !contains(id.Roles, "platform-admin") || !contains(id.Roles, "auditor") || !contains(id.Roles, "readonly-operator") {
		t.Fatalf("roles=%v", id.Roles)
	}
	if !contains(id.ACLGroups, "group:engineering") {
		t.Fatalf("ACL groups=%v", id.ACLGroups)
	}
	if !contains(id.ACLGroups, "group:readonly") {
		t.Fatalf("automatic ACL groups=%v", id.ACLGroups)
	}
	accessRaw, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "kc-1", Audience: jwt.Audience{"account"}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now)}).Claims(map[string]any{
		"azp": "git-ctx", "preferred_username": "alice", "email": "alice@example.test",
		"realm_access": map[string]any{"roles": []string{"ctx-admin"}},
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	// Verify the ID token first to ensure its audience-checking verifier cannot
	// leak through the cache into the access-token path.
	accessID, err := verifier.VerifyAccessToken(context.Background(), accessRaw)
	if err != nil || !contains(accessID.Roles, "platform-admin") {
		t.Fatalf("access token identity=%#v err=%v", accessID, err)
	}
	logoutURL, err := EndSessionURL(context.Background(), OIDCConfig{IssuerURL: issuer, ClientID: "git-ctx", PostLogoutRedirectURL: "https://git-ctx.example/"})
	if err != nil || !strings.Contains(logoutURL, "client_id=git-ctx") || !strings.Contains(logoutURL, "post_logout_redirect_uri=") {
		t.Fatalf("logoutURL=%s err=%v", logoutURL, err)
	}
}

func TestOIDCDefaultsUseOnlyBuiltInKeycloakScopes(t *testing.T) {
	cfg := OIDCConfig{}
	cfg.defaults()
	if got := strings.Join(cfg.Scopes, " "); got != "openid profile email" {
		t.Fatalf("default scopes=%q", got)
	}
}

func TestExchangeCodeUsesConfiguredOIDCHTTPClient(t *testing.T) {
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks"})
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code") != "login-code" || r.Form.Get("code_verifier") != "pkce-verifier" {
				http.Error(w, "invalid exchange", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 300, "id_token": "signed-id-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	verify := false
	token, err := ExchangeCode(context.Background(), OIDCConfig{IssuerURL: issuer, ClientID: "git-ctx", ClientSecret: "secret", RedirectURL: "http://localhost:4747/auth/callback", TLSVerify: &verify}, "login-code", "pkce-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access" || token.Extra("id_token") != "signed-id-token" {
		t.Fatalf("token=%#v id_token=%v", token, token.Extra("id_token"))
	}
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

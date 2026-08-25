package app

import (
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"git-ctx/internal/apikey"
	"git-ctx/internal/auth"
	"git-ctx/internal/recovery"
	"git-ctx/internal/search"
	"git-ctx/internal/store"
	"golang.org/x/oauth2"
)

// Authentication and authorisation: OIDC login, sessions, bootstrap and recovery
// access, and the role checks every admin route runs through.

// attemptLimiter throttles the credential endpoints per client address. The
// bootstrap and recovery tokens are high entropy, but an unthrottled endpoint
// still lets an attacker probe continuously and floods the audit log.
type attemptLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("CONTEXT7_API_KEY")
		if raw == "" {
			raw = r.Header.Get("X-API-Key")
		}
		// Several MCP clients and gateways can only send an Authorization
		// header, and a key pasted there used to be checked against Keycloak
		// and rejected with an error about a login system the caller never
		// used. The key format identifies itself, so it is accepted wherever
		// it arrives — and nothing else is treated as a key.
		if raw == "" {
			if bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); apikey.Looks(bearer) {
				raw = bearer
			}
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
				if !cookieMutationAllowed(r) {
					problem(w, http.StatusForbidden, "csrf_check_failed", "Session-authenticated state changes require a same-origin request")
					return
				}
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
			identity, err := a.oidc.VerifyAccessToken(r.Context(), token)
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

func cookieMutationAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Session cookies are a browser credential. Non-browser clients use an
		// API key or bearer token, so an unsafe cookie request without Origin is
		// rejected instead of creating an ambiguous CSRF bypass.
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	expectedScheme := "http"
	if requestIsSecure(r) {
		expectedScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, expectedScheme) && strings.EqualFold(parsed.Host, r.Host)
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
	tokenHash, tokenExpires, err := recovery.Verify(strings.TrimSpace(in.Token), a.cfg.RecoveryKey, time.Now().UTC())
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
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
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
	// Checked again on the way out: the value was stored before this redirect
	// and a row written by an older build has not been through safeReturnTo.
	http.Redirect(w, r, safeReturnTo(returnTo), http.StatusFound)
}

// safeReturnTo keeps the post-login redirect on this site.
//
// Requiring a leading "/" and rejecting "//" is not enough. Browsers fold a
// backslash into a slash for http and https URLs, so "/\evil.example" resolves
// to //evil.example and lands the user on another origin -- moments after they
// authenticated, which is exactly when they trust the flow. Anything that is
// not plainly a path on this host becomes "/".
func safeReturnTo(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	if strings.ContainsAny(raw, "\\") {
		return "/"
	}
	for _, char := range raw {
		if char < 0x20 || char == 0x7f {
			return "/"
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Opaque != "" {
		return "/"
	}
	return raw
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("git_ctx_session"); err == nil {
		if !cookieMutationAllowed(r) {
			problem(w, http.StatusForbidden, "csrf_check_failed", "Session-authenticated state changes require a same-origin request")
			return
		}
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

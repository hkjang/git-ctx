package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"git-ctx/internal/auth"
	"git-ctx/internal/config"
	"git-ctx/internal/toolcatalog"
)

// MCP over SSO: a Keycloak access token opens /mcp without a personal key.
//
// The MCP authorization specification (2025-06-18 and later) is OAuth 2.1. An
// MCP client that is refused with 401 reads the resource_metadata URL in the
// challenge, finds the authorization server there (RFC 9728), sends the
// person through Keycloak with PKCE and comes back with an access token whose
// audience is this server. This file is the resource-server half only: the
// metadata document, the challenge, the token check and the account lookup.
// Nothing issues tokens here, nothing registers clients, and no token is
// stored — every request is checked on its own.
//
// The key stays. It is what an automation with no person behind it uses, and
// what a deployment without Keycloak uses. An SSO token is a second door into
// the same room: it authenticates an account that already exists, carries the
// scopes the administrator chose for it, and passes every gate a key passes.
// It never creates an account, never revives a disabled one, and never turns a
// role claim into a platform role — signing in to the web is where all of
// that is decided.

const mcpPath = "/mcp"

// mcpOAuthSettings is the "mcp" setting category's oauth* keys. The names are
// this platform's spelling of the standard's mcp.oauth.enabled / .resource /
// .audience / .scopes, the way keycloak.issuerUrl spells oidc.issuer_url.
type mcpOAuthSettings struct {
	Enabled bool
	// Resource is the identifier this server claims (RFC 8707): the setting
	// when the administrator gave one, otherwise ui.publicUrl plus /mcp, which
	// mcpOAuthActive fills in. Never derived from the request.
	Resource string
	// Audiences are values an administrator accepts in aud or azp besides the
	// resource identifier — in practice the MCP client's Keycloak client ID.
	Audiences []string
	// Scopes is the ceiling for an SSO caller. Keycloak is not taught this
	// platform's tool vocabulary; the administrator states it once here.
	Scopes []string
}

// mcpOAuthReadScopes is the default ceiling: every tool a developer's key may
// hold, without the three administrative ones.
func mcpOAuthReadScopes() []string {
	var out []string
	for _, name := range toolcatalog.Names() {
		switch name {
		case toolcatalog.GetPlatformStatus, toolcatalog.ListIndexJobs, toolcatalog.ReindexRepository:
			continue
		}
		out = append(out, name)
	}
	return out
}

func (a *App) mcpOAuthSettings(ctx context.Context) mcpOAuthSettings {
	settings, _ := a.loadSettingMap(ctx, "mcp")
	enabled, _ := settings["oauthEnabled"].(bool)
	out := mcpOAuthSettings{
		Enabled:   enabled,
		Resource:  strings.TrimSpace(stringValue(settings, "oauthResource")),
		Audiences: settingWords(settings["oauthAudience"]),
		Scopes:    settingWords(settings["oauthScopes"]),
	}
	if len(out.Scopes) == 0 {
		out.Scopes = mcpOAuthReadScopes()
	}
	return out
}

// settingWords reads a list the console stores as an array and an operator
// may have imported as one space- or comma-separated string.
func settingWords(raw any) []string {
	var out []string
	add := func(value string) {
		for _, word := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r' }) {
			if !slices.Contains(out, word) {
				out = append(out, word)
			}
		}
	}
	switch value := raw.(type) {
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				add(text)
			}
		}
	case []string:
		for _, item := range value {
			add(item)
		}
	case string:
		add(value)
	}
	return out
}

// validateMCPOAuthSetting is the save-time half of the switch: a resource
// that is not a public HTTPS URL, an audience that is not a client ID, or a
// scope that names no tool would each only be discovered when the first
// token is refused, and the refusal would blame the token.
func validateMCPOAuthSetting(value map[string]any) error {
	if raw, present := value["oauthEnabled"]; present {
		if _, ok := raw.(bool); !ok {
			return errors.New("mcp.oauthEnabled must be true or false")
		}
	}
	if resource := strings.TrimSpace(stringValue(value, "oauthResource")); resource != "" {
		parsed, err := url.Parse(resource)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("mcp.oauthResource must be an absolute URL without credentials, query or fragment")
		}
		if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
			return errors.New("mcp.oauthResource must use HTTPS outside localhost")
		}
	}
	for _, key := range []string{"oauthAudience", "oauthScopes"} {
		if raw, present := value[key]; present {
			switch raw.(type) {
			case []any, string:
			default:
				return fmt.Errorf("mcp.%s must be a list of strings", key)
			}
		}
	}
	for _, audience := range settingWords(value["oauthAudience"]) {
		if len(audience) > 256 || strings.ContainsAny(audience, "\\\"'<>") {
			return fmt.Errorf("mcp.oauthAudience contains an invalid value %q", audience)
		}
	}
	for _, scope := range settingWords(value["oauthScopes"]) {
		if !toolcatalog.Supports(scope) {
			return fmt.Errorf("mcp.oauthScopes names no MCP tool: %q", scope)
		}
	}
	return nil
}

// mcpOAuthActive reports whether SSO tokens are accepted at /mcp right now,
// and if not, why. Enabled is not enough: without a Keycloak issuer there is
// nothing to verify against, a metadata document that names no
// authorization server sends the client in a loop, and without a resource
// identifier there is nothing a token's aud could be held to. When active,
// the returned settings carry the resolved Resource.
func (a *App) mcpOAuthActive(ctx context.Context) (mcpOAuthSettings, string, string) {
	settings := a.mcpOAuthSettings(ctx)
	if !settings.Enabled {
		return settings, "", "mcp.oauthEnabled is off"
	}
	cfg, err := a.loadOIDCConfig(ctx)
	if err != nil || strings.TrimSpace(cfg.IssuerURL) == "" {
		return settings, "", "Keycloak is not configured (keycloak.issuerUrl is empty)"
	}
	if settings.Resource == "" {
		settings.Resource = a.mcpResource(ctx)
	}
	if settings.Resource == "" {
		return settings, "", "no resource identifier (set mcp.oauthResource or ui.publicUrl)"
	}
	return settings, strings.TrimSuffix(cfg.IssuerURL, "/"), ""
}

// mcpResource is the identifier this deployment claims when mcp.oauthResource
// is empty: the public address the client actually connects to, plus /mcp.
// Only a configured public URL counts. The compiled-in default would make
// every installation claim the same identifier, and the request's Host
// header is whatever the caller chose to send — neither is an identity a
// token's aud can be checked against, so with nothing configured there is
// no resource at all and SSO stays inactive.
func (a *App) mcpResource(ctx context.Context) string {
	if settings, err := a.loadSettingMap(ctx, "ui"); err == nil {
		if value, ok := settings["publicUrl"].(string); ok {
			if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
				return strings.TrimSuffix(value, "/") + mcpPath
			}
		}
	}
	if a.cfg.PublicURL == "" || a.cfg.PublicURL == config.DefaultPublicURL {
		return ""
	}
	return strings.TrimSuffix(a.cfg.PublicURL, "/") + mcpPath
}

// mcpMetadataURL is where a refused client is sent to learn the above.
func mcpMetadataURL(resource string) string {
	return strings.TrimSuffix(resource, mcpPath) + "/.well-known/oauth-protected-resource" + mcpPath
}

// protectedResourceMetadata is RFC 9728: the document a refused MCP client
// reads to find the authorization server. Public by design — it says where to
// sign in, not who is signed in — and bare JSON rather than this API's
// problem envelope, because the reader is an OAuth client library.
func (a *App) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	settings, issuer, inactive := a.mcpOAuthActive(r.Context())
	if inactive != "" {
		if settings.Enabled {
			slog.Warn("mcp sso is enabled but not active", "reason", inactive)
		}
		problem(w, http.StatusNotFound, "mcp_oauth_disabled", "This server's MCP endpoint does not accept SSO tokens; use a personal API key")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	// Browser-hosted MCP clients read this document cross-origin. Only this
	// document is opened; /mcp itself keeps its Origin allow-list.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 settings.Resource,
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         settings.Scopes,
		"resource_name":            "git-ctx MCP",
	})
}

// mcpChallenge turns a 401 from /mcp into an invitation: the client reads
// resource_metadata and starts the OAuth flow from there. Only the MCP path
// gets it — a browser or REST client that sees this header on an API 401
// would be sent somewhere it cannot use.
func (a *App) mcpChallenge(w http.ResponseWriter, r *http.Request, presented bool) {
	if r.URL.Path != mcpPath {
		return
	}
	settings, _, inactive := a.mcpOAuthActive(r.Context())
	if inactive != "" {
		return
	}
	value := fmt.Sprintf(`Bearer realm="git-ctx", resource_metadata=%q`, mcpMetadataURL(settings.Resource))
	if presented {
		value += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", value)
}

// looksLikeJWT is the shape test that separates "not a key" from "an SSO
// token to check": three non-empty dot-separated segments.
func looksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// oauthRefusal carries two texts: the reason, which names the failed check
// and goes to the log, and the message, which tells the caller what to do.
// The caller's text never repeats the reason — a bad signature and an
// unknown issuer look the same from outside, and should.
type oauthRefusal struct {
	reason  error
	message string
}

// oauthPrincipal turns an SSO access token presented at /mcp into a
// principal, or says why it will not. Called only once the request has been
// found to carry a JWT-shaped bearer and SSO acceptance is active, so
// settings.Resource is resolved.
func (a *App) oauthPrincipal(ctx context.Context, token string, settings mcpOAuthSettings) (auth.Principal, *oauthRefusal) {
	resource := settings.Resource
	verified, err := a.oidc.VerifyResourceToken(ctx, token, resource, settings.Audiences)
	if err != nil {
		var audience *auth.AudienceError
		if errors.As(err, &audience) {
			return auth.Principal{}, &oauthRefusal{reason: err, message: fmt.Sprintf(
				"SSO token was not issued for this server (aud=%v, azp=%q). Add %q to the allowed audiences (mcp.oauthAudience) or give the Keycloak client an Audience mapper for %q",
				audience.Audience, audience.ClientID, audience.ClientID, resource)}
		}
		return auth.Principal{}, &oauthRefusal{reason: err, message: "SSO access token was rejected (signature, issuer, validity period or token type); sign in again from the MCP client"}
	}
	// The administrator's list is the ceiling. A token that carries this
	// platform's tool names in its scope claim narrows itself to the
	// intersection; one that carries none (Keycloak's default "openid profile
	// email") gets the whole ceiling. An intersection that comes out empty is a
	// refusal, never an empty list: no gate here reads an empty list as "no
	// tools", and handing one on would be a guess about what every gate does.
	scopes := settings.Scopes
	if named := slices.DeleteFunc(slices.Clone(verified.Scopes), func(s string) bool { return !toolcatalog.Supports(s) }); len(named) > 0 {
		scopes = slices.DeleteFunc(slices.Clone(settings.Scopes), func(s string) bool { return !slices.Contains(named, s) })
		if len(scopes) == 0 {
			return auth.Principal{}, &oauthRefusal{
				reason:  fmt.Errorf("token scopes %v share nothing with mcp.oauthScopes %v", named, settings.Scopes),
				message: fmt.Sprintf("SSO token scopes %v include none of the MCP scopes this server grants to SSO callers %v", named, settings.Scopes),
			}
		}
	}
	// The same lookup the key path does, and only that: an account that is
	// not there is not created, and one that is disabled is not reopened.
	var uid, subject, username, bitbucketSlug, gitlabID, aclGroupText string
	err = a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT u.id,u.subject,u.username,COALESCE(i.bitbucket_user_slug,''),COALESCE(i.gitlab_user_id,''),COALESCE(i.bitbucket_groups,'')
		FROM users u LEFT JOIN user_identities i ON i.user_id=u.id
		WHERE u.status='active' AND (u.subject=? OR (?<>'' AND u.username=?))
		ORDER BY CASE WHEN u.subject=? THEN 0 ELSE 1 END LIMIT 1`), verified.Subject, verified.Username, verified.Username, verified.Subject).
		Scan(&uid, &subject, &username, &bitbucketSlug, &gitlabID, &aclGroupText)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Principal{}, &oauthRefusal{
			reason:  fmt.Errorf("no active account for subject %q / username %q", verified.Subject, verified.Username),
			message: "This SSO account is not registered here or is disabled; sign in to the web console once first",
		}
	}
	if err != nil {
		return auth.Principal{}, &oauthRefusal{reason: fmt.Errorf("account lookup: %w", err), message: "Unable to look up the SSO account"}
	}
	// Roles come from this platform's table, not from the token.
	roles, _ := a.userRoles(ctx, uid)
	aclPrincipal := bitbucketSlug
	if aclPrincipal == "" && gitlabID != "" {
		aclPrincipal = "gitlab:" + gitlabID
	}
	return auth.Principal{
		UserID: uid, Subject: subject, Username: username,
		ACLPrincipal: aclPrincipal, ACLPrincipals: sourceACLPrincipals(bitbucketSlug, gitlabID, splitCSV(aclGroupText)),
		Roles: roles, Scopes: scopes,
		// The call log's key-prefix column says how the call came in; a key
		// shows its prefix, an SSO token the client it was issued to.
		KeyPrefix:     "sso:" + verified.ClientID,
		OAuthClientID: verified.ClientID,
	}, nil
}

// logOAuthRefusal records which check failed. The caller only ever learns
// the message; without this line an operator cannot tell a bad signature
// from a wrong issuer from an expired token. The request id is the one the
// logging middleware stamped on the response, so the line joins the
// http_request line and the audit row for the same call.
func (a *App) logOAuthRefusal(w http.ResponseWriter, r *http.Request, refusal *oauthRefusal) {
	slog.Warn("mcp sso token refused", "request_id", w.Header().Get("X-Request-ID"), "reason", refusal.reason, "client_ip", auth.ClientIP(r.Context()))
}

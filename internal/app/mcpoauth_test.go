package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/config"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// A fake Keycloak: a real key pair, discovery and JWKS, and tokens it signs
// on request. Nothing about token verification is stubbed — the server under
// test reads the discovery document and the key set over HTTP exactly as it
// would from Keycloak.
type fakeIdP struct {
	t      *testing.T
	server *httptest.Server
	issuer string
	key    *rsa.PrivateKey
	signer jose.Signer
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, &jose.SignerOptions{ExtraHeaders: map[jose.HeaderKey]any{"kid": "sso"}})
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{t: t, key: key, signer: signer}
	idp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": idp.issuer, "authorization_endpoint": idp.issuer + "/auth", "token_endpoint": idp.issuer + "/token",
				"jwks_uri": idp.issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "sso", Algorithm: "RS256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.server.Close)
	idp.issuer = idp.server.URL
	return idp
}

// token signs a Keycloak-shaped access token: aud "account" and the client
// in azp unless the extra claims say otherwise, which is what Keycloak 26
// issues without an Audience mapper.
func (idp *fakeIdP) token(subject string, azp string, audience []string, extra map[string]any) string {
	idp.t.Helper()
	now := time.Now()
	claims := jwt.Claims{Issuer: idp.issuer, Subject: subject, Audience: jwt.Audience(audience), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)), IssuedAt: jwt.NewNumericDate(now)}
	if raw, ok := extra["iss"].(string); ok {
		claims.Issuer = raw
	}
	if raw, ok := extra["exp"].(time.Time); ok {
		claims.Expiry = jwt.NewNumericDate(raw)
	}
	if raw, ok := extra["nbf"].(time.Time); ok {
		claims.NotBefore = jwt.NewNumericDate(raw)
	}
	private := map[string]any{"azp": azp, "typ": "Bearer", "preferred_username": subject + "-name", "scope": "openid profile email"}
	for key, value := range extra {
		switch key {
		case "iss", "exp", "nbf":
			continue
		}
		private[key] = value
	}
	raw, err := jwt.Signed(idp.signer).Claims(claims).Claims(private).Serialize()
	if err != nil {
		idp.t.Fatal(err)
	}
	return raw
}

const ssoResource = "https://git-ctx.company/mcp"

// ssoFixture is a server with Keycloak configured for the web sign-in, one
// registered active account, one disabled one, and SSO for MCP still off.
func ssoFixture(t *testing.T, name string) (*App, *fakeIdP) {
	t.Helper()
	return ssoFixtureAt(t, name, "https://git-ctx.company")
}

// ssoFixtureAt is ssoFixture with the public URL the process was started
// with; config.DefaultPublicURL is an installation nobody configured.
func ssoFixtureAt(t *testing.T, name, publicURL string) (*App, *fakeIdP) {
	t.Helper()
	idp := newFakeIdP(t)
	a, err := New(context.Background(), config.Config{DatabaseDriver: "sqlite", DatabaseDSN: "file:" + name + "?mode=memory&cache=shared&_foreign_keys=on&_busy_timeout=5000", KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: publicURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := a.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO users(id,subject,username,email) VALUES('u1','kc-alice','alice','alice@example.test')`)
	must(`INSERT INTO user_identities(user_id,bitbucket_user_slug,mapping_source) VALUES('u1','alice.bb','test')`)
	must(`INSERT INTO users(id,subject,username,email,status) VALUES('u2','kc-bob','bob','bob@example.test','disabled')`)
	saveSetting(t, a, "keycloak", `{"issuerUrl":`+quoteJSON(idp.issuer)+`,"clientId":"git-ctx","clientSecret":"client-secret"}`)
	return a, idp
}

func saveSetting(t *testing.T, a *App, category, body string) {
	t.Helper()
	put := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/"+category, strings.NewReader(body))
	put.Header.Set("Authorization", "Bearer bootstrap")
	put.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("save %s=%d body=%s", category, rec.Code, rec.Body.String())
	}
}

func ssoRequest(a *App, path, bearer, body string) *httptest.ResponseRecorder {
	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"sso","version":"1"}}}`

// listTools initialises a session with the bearer and returns the tool names
// it is shown.
func listTools(t *testing.T, a *App, bearer string) []string {
	t.Helper()
	init := ssoRequest(a, "/mcp", bearer, mcpInitialize)
	if init.Code != http.StatusOK {
		t.Fatalf("initialize=%d body=%s", init.Code, init.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	req.Header.Set("Mcp-Session-Id", init.Header().Get("Mcp-Session-Id"))
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("tools/list=%d body=%s", rec.Code, rec.Body.String())
	}
	var names []string
	for _, tool := range listed.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestSSOForMCPIsOffByDefault(t *testing.T) {
	a, idp := ssoFixture(t, "sso-off")
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		if rec := ssoRequest(a, path, "", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s while off=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	// A token for this server is refused the way every non-key bearer was
	// refused before this feature existed, and the refusal carries no
	// challenge: an install that never turned this on says nothing new.
	token := idp.token("kc-alice", "claude-mcp", []string{ssoResource}, nil)
	rec := ssoRequest(a, "/mcp", token, mcpInitialize)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Keycloak access token validation failed") {
		t.Fatalf("token while off=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("challenge while off: %q", rec.Header().Get("WWW-Authenticate"))
	}
	if rec := ssoRequest(a, "/mcp", "", mcpInitialize); rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("anonymous challenge while off: %q", rec.Header().Get("WWW-Authenticate"))
	}
}

// A resource identifier is never made up from the request. With neither
// mcp.oauthResource nor ui.publicUrl set, an installation still running on
// the compiled-in public URL has no identity a token's aud could be held to,
// so SSO for MCP stays inactive until one is configured — not "any Host the
// caller sends", and not the same http://localhost:4747/mcp on every install.
func TestSSOStaysInactiveWithoutAResourceIdentifier(t *testing.T) {
	a, idp := ssoFixtureAt(t, "sso-no-resource", config.DefaultPublicURL)
	saveSetting(t, a, "mcp", `{"oauthEnabled":true,"oauthAudience":["claude-mcp"]}`)

	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, _, inactive := a.mcpOAuthActive(context.Background()); !strings.Contains(inactive, "ui.publicUrl") {
		t.Fatalf("inactive reason=%q", inactive)
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		if rec := ssoRequest(a, path, "", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s without a resource=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if !strings.Contains(log.String(), "mcp sso is enabled but not active") {
		t.Fatalf("no warning about the inactive switch: %s", log.String())
	}
	// A token whose aud is exactly what the Host header would have produced
	// is refused all the same, and no challenge invites the client to retry.
	for _, aud := range []string{"http://localhost:4747/mcp", "http://example.com/mcp"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(mcpInitialize))
		req.Host = strings.TrimSuffix(strings.TrimPrefix(aud, "http://"), "/mcp")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		req.Header.Set("Authorization", "Bearer "+idp.token("kc-alice", "claude-mcp", []string{aud}, nil))
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Keycloak access token validation failed") {
			t.Fatalf("aud=%s: code=%d body=%s", aud, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("aud=%s: challenge=%q", aud, rec.Header().Get("WWW-Authenticate"))
		}
	}
	// A token via the allowed-audience list is refused too: the switch is
	// inactive as a whole, not merely missing one comparison value.
	if rec := ssoRequest(a, "/mcp", idp.token("kc-alice", "claude-mcp", []string{"account"}, nil), mcpInitialize); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("azp token without a resource=%d challenge=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	// ui.publicUrl is what the documentation says fills the gap.
	saveSetting(t, a, "ui", `{"publicUrl":"https://console.company/"}`)
	rec := ssoRequest(a, "/.well-known/oauth-protected-resource/mcp", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"resource":"https://console.company/mcp"`) {
		t.Fatalf("metadata after ui.publicUrl=%d body=%s", rec.Code, rec.Body.String())
	}
	if names := listTools(t, a, idp.token("kc-alice", "claude-mcp", []string{"https://console.company/mcp"}, nil)); len(names) == 0 {
		t.Fatalf("token for the configured resource opened nothing")
	}
}

func TestSSOMetadataAndChallengePointAtKeycloak(t *testing.T) {
	a, idp := ssoFixture(t, "sso-metadata")
	saveSetting(t, a, "mcp", `{"oauthEnabled":true,"oauthAudience":["claude-mcp"],"oauthScopes":["resolve-library-id","query-docs"]}`)

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		rec := ssoRequest(a, path, "", "")
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s=%d cors=%q body=%s", path, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"), rec.Body.String())
		}
		var document map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		// Bare RFC 9728 JSON, not this API's problem envelope.
		if _, enveloped := document["status"]; enveloped {
			t.Fatalf("metadata is wrapped: %s", rec.Body.String())
		}
		if document["resource"] != ssoResource {
			t.Fatalf("resource=%v", document["resource"])
		}
		if servers, _ := document["authorization_servers"].([]any); len(servers) != 1 || servers[0] != idp.issuer {
			t.Fatalf("authorization_servers=%v", document["authorization_servers"])
		}
		if methods, _ := document["bearer_methods_supported"].([]any); len(methods) != 1 || methods[0] != "header" {
			t.Fatalf("bearer_methods_supported=%v", document["bearer_methods_supported"])
		}
		if scopes, _ := document["scopes_supported"].([]any); len(scopes) != 2 || scopes[0] != "resolve-library-id" {
			t.Fatalf("scopes_supported=%v", document["scopes_supported"])
		}
	}

	anonymous := ssoRequest(a, "/mcp", "", mcpInitialize)
	challenge := anonymous.Header().Get("WWW-Authenticate")
	want := `Bearer realm="git-ctx", resource_metadata="https://git-ctx.company/.well-known/oauth-protected-resource/mcp"`
	if anonymous.Code != http.StatusUnauthorized || challenge != want {
		t.Fatalf("anonymous /mcp=%d challenge=%q", anonymous.Code, challenge)
	}
	// A refused credential adds error="invalid_token" so the client knows to
	// obtain a new token rather than to start from nothing.
	refused := ssoRequest(a, "/mcp", "bctx_live_nope_nope", mcpInitialize)
	if refused.Code != http.StatusUnauthorized || refused.Header().Get("WWW-Authenticate") != want+`, error="invalid_token"` {
		t.Fatalf("refused key /mcp=%d challenge=%q", refused.Code, refused.Header().Get("WWW-Authenticate"))
	}
	// REST 401s are what they always were: a browser or API client shown
	// this header would be sent somewhere it cannot use.
	rest := ssoRequest(a, "/api/v1/me", "", "")
	if rest.Code != http.StatusUnauthorized || rest.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("REST 401=%d challenge=%q", rest.Code, rest.Header().Get("WWW-Authenticate"))
	}
}

func TestSSOTokenOpensMCPForARegisteredAccountOnly(t *testing.T) {
	a, idp := ssoFixture(t, "sso-account")
	saveSetting(t, a, "mcp", `{"oauthEnabled":true,"oauthScopes":["resolve-library-id","query-docs","get-platform-status"]}`)

	// The formal path: an Audience mapper put the resource identifier in aud.
	// The token also claims platform-admin, which this server must not believe.
	token := idp.token("kc-alice", "claude-mcp", []string{ssoResource, "account"}, map[string]any{"realm_access": map[string]any{"roles": []string{"platform-admin"}}})
	names := listTools(t, a, token)
	if strings.Join(names, ",") != "resolve-library-id,query-docs" {
		t.Fatalf("tools for SSO caller=%v (get-platform-status needs a platform role this account does not hold)", names)
	}
	var count int
	if err := a.store.DB.QueryRow(`SELECT COUNT(*) FROM user_roles WHERE user_id='u1'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("token roles were written to the account: count=%d err=%v", count, err)
	}
	// Once the platform itself grants the role, the same token opens the
	// administrative tool — the role comes from the table, not the token.
	if _, err := a.store.DB.Exec(`INSERT INTO user_roles(user_id,role_code) VALUES('u1','readonly-operator')`); err != nil {
		t.Fatal(err)
	}
	if names = listTools(t, a, token); !stringContains(names, "get-platform-status") {
		t.Fatalf("tools after role grant=%v", names)
	}
	// A call is recorded against the account and says it came in over SSO
	// from which client, where a key call shows its prefix.
	init := ssoRequest(a, "/mcp", token, mcpInitialize)
	call := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"resolve-library-id","arguments":{"libraryName":"demo"}}}`))
	call.Header.Set("Content-Type", "application/json")
	call.Header.Set("Accept", "application/json, text/event-stream")
	call.Header.Set("MCP-Protocol-Version", "2025-06-18")
	call.Header.Set("Mcp-Session-Id", init.Header().Get("Mcp-Session-Id"))
	call.Header.Set("Authorization", "Bearer "+token)
	called := httptest.NewRecorder()
	a.Handler().ServeHTTP(called, call)
	if called.Code != http.StatusOK {
		t.Fatalf("tools/call=%d body=%s", called.Code, called.Body.String())
	}
	var prefix string
	if err := a.store.DB.QueryRow(`SELECT api_key_prefix FROM mcp_calls WHERE user_id='u1' AND tool='resolve-library-id'`).Scan(&prefix); err != nil || prefix != "sso:claude-mcp" {
		t.Fatalf("call attributed to %q err=%v", prefix, err)
	}

	// Somebody Keycloak knows but this platform does not: refused, and no
	// account appears. Signing in to the web is what registers a person.
	stranger := ssoRequest(a, "/mcp", idp.token("kc-carol", "claude-mcp", []string{ssoResource}, nil), mcpInitialize)
	if stranger.Code != http.StatusUnauthorized || !strings.Contains(stranger.Body.String(), "sign in to the web console once first") {
		t.Fatalf("unregistered=%d body=%s", stranger.Code, stranger.Body.String())
	}
	if err := a.store.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE subject='kc-carol'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("an account was created for an SSO stranger: count=%d err=%v", count, err)
	}
	disabled := ssoRequest(a, "/mcp", idp.token("kc-bob", "claude-mcp", []string{ssoResource}, nil), mcpInitialize)
	if disabled.Code != http.StatusUnauthorized || !strings.Contains(disabled.Body.String(), "sign in to the web console once first") {
		t.Fatalf("disabled=%d body=%s", disabled.Code, disabled.Body.String())
	}
	var status string
	if err := a.store.DB.QueryRow(`SELECT status FROM users WHERE id='u2'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("disabled account after SSO attempt: status=%q err=%v", status, err)
	}

	// The token is an MCP credential and nothing else.
	rest := ssoRequest(a, "/api/v1/me", token, "")
	if rest.Code != http.StatusUnauthorized {
		t.Fatalf("SSO token on REST=%d body=%s", rest.Code, rest.Body.String())
	}
}

func TestSSOTokenForAnotherApplicationIsRefusedWithTheFix(t *testing.T) {
	a, idp := ssoFixture(t, "sso-audience")
	saveSetting(t, a, "mcp", `{"oauthEnabled":true}`)

	// What Keycloak 26 issues without a mapper: aud is "account", the client
	// is in azp. Nothing names this server, so the token could be for any
	// application in the realm.
	other := ssoRequest(a, "/mcp", idp.token("kc-alice", "other-app", []string{"account"}, nil), mcpInitialize)
	body := other.Body.String()
	if other.Code != http.StatusUnauthorized || !strings.Contains(body, `aud=[account]`) || !strings.Contains(body, `azp=\"other-app\"`) || !strings.Contains(body, `Add \"other-app\" to the allowed audiences`) || !strings.Contains(body, ssoResource) {
		t.Fatalf("other application=%d body=%s", other.Code, body)
	}
	if !strings.HasSuffix(other.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("challenge=%q", other.Header().Get("WWW-Authenticate"))
	}

	// The compatibility path: the administrator names the client instead of
	// adding a mapper, and the same token passes on azp.
	saveSetting(t, a, "mcp", `{"oauthEnabled":true,"oauthAudience":["other-app"],"oauthScopes":["query-docs"]}`)
	if names := listTools(t, a, idp.token("kc-alice", "other-app", []string{"account"}, nil)); strings.Join(names, ",") != "query-docs" {
		t.Fatalf("tools via azp=%v", names)
	}
	// The web sign-in's client is not implicitly trusted at /mcp: an SSO
	// caller is held to the administrator's list, whichever client it used.
	web := ssoRequest(a, "/mcp", idp.token("kc-alice", "git-ctx", []string{"account"}, nil), mcpInitialize)
	if web.Code != http.StatusUnauthorized || !strings.Contains(web.Body.String(), `azp=\"git-ctx\"`) {
		t.Fatalf("web client token=%d body=%s", web.Code, web.Body.String())
	}
}

func TestSSOTokenChecksEveryClaimAndLogsWhichOneFailed(t *testing.T) {
	a, idp := ssoFixture(t, "sso-claims")
	saveSetting(t, a, "mcp", `{"oauthEnabled":true,"oauthAudience":["claude-mcp"]}`)

	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	hmac := func() string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(strings.Repeat("k", 32))}, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: idp.issuer, Subject: "kc-alice", Audience: jwt.Audience{ssoResource}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}).Claims(map[string]any{"azp": "claude-mcp"}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	cases := []struct {
		name, token, logged string
	}{
		{"expired", idp.token("kc-alice", "claude-mcp", []string{"account"}, map[string]any{"exp": time.Now().Add(-time.Minute)}), "expired"},
		{"not yet valid", idp.token("kc-alice", "claude-mcp", []string{"account"}, map[string]any{"nbf": time.Now().Add(time.Hour)}), "nbf"},
		{"another issuer", idp.token("kc-alice", "claude-mcp", []string{"account"}, map[string]any{"iss": "https://elsewhere.example/realms/x"}), "different provider"},
		{"ID token", idp.token("kc-alice", "claude-mcp", []string{"account"}, map[string]any{"typ": "ID"}), "typ=ID"},
		{"HS256", hmac(), "unexpected signature algorithm"},
		{"proof of possession", idp.token("kc-alice", "claude-mcp", []string{"account"}, map[string]any{"cnf": map[string]any{"jkt": "x"}}), "cnf"},
		{"no subject", idp.token("", "claude-mcp", []string{"account"}, nil), "subject"},
		{"scopes outside the ceiling", idp.token("kc-alice", "claude-mcp", []string{"account"}, map[string]any{"scope": "openid reindex-repository"}), "share nothing"},
	}
	for _, tc := range cases {
		log.Reset()
		rec := ssoRequest(a, "/mcp", tc.token, mcpInitialize)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: code=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		refusedLine := ""
		for _, line := range strings.Split(log.String(), "\n") {
			if strings.Contains(line, "mcp sso token refused") {
				refusedLine = line
			}
		}
		if refusedLine == "" || !strings.Contains(refusedLine, tc.logged) {
			t.Fatalf("%s: the log does not say which check failed (want %q): %s", tc.name, tc.logged, log.String())
		}
		// The refusal line itself carries the request id the middleware
		// stamped on the response, so an operator can join it to the
		// http_request line (which has its own request_id, hence the line
		// rather than the whole buffer).
		requestID := rec.Header().Get("X-Request-ID")
		if requestID == "" {
			t.Fatalf("%s: response carries no X-Request-ID", tc.name)
		}
		if !strings.Contains(refusedLine, "request_id="+requestID) {
			t.Fatalf("%s: the refusal line does not carry request_id=%s: %s", tc.name, requestID, refusedLine)
		}
	}
	var audited int
	if err := a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='mcp.oauth.auth' AND outcome='failure'`).Scan(&audited); err != nil || audited != len(cases) {
		t.Fatalf("audit rows=%d err=%v", audited, err)
	}
	// A token that narrows itself to part of the ceiling gets that part.
	if names := listTools(t, a, idp.token("kc-alice", "claude-mcp", []string{"account"}, map[string]any{"scope": "openid query-docs read-file"})); strings.Join(names, ",") != "query-docs,read-file" {
		t.Fatalf("intersected tools=%v", names)
	}
}

func TestSSOSettingsAreCheckedWhenSaved(t *testing.T) {
	a, _ := ssoFixture(t, "sso-settings")
	for _, body := range []string{
		`{"oauthEnabled":"yes"}`,
		`{"oauthResource":"http://git-ctx.company/mcp"}`,
		`{"oauthResource":"https://user:pw@git-ctx.company/mcp"}`,
		`{"oauthScopes":["not-a-tool"]}`,
		`{"oauthAudience":["<script>"]}`,
		`{"oauthAudience":42}`,
	} {
		put := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/mcp", strings.NewReader(body))
		put.Header.Set("Authorization", "Bearer bootstrap")
		put.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, put)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s saved with %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	// Enabled without a resource of its own: the public URL supplies one, and
	// the default ceiling is every tool a developer may hold, minus the
	// administrative three.
	saveSetting(t, a, "mcp", `{"oauthEnabled":true,"oauthAudience":"claude-mcp cursor"}`)
	settings := a.mcpOAuthSettings(context.Background())
	if strings.Join(settings.Audiences, ",") != "claude-mcp,cursor" {
		t.Fatalf("audiences=%v", settings.Audiences)
	}
	for _, admin := range []string{"get-platform-status", "list-index-jobs", "reindex-repository"} {
		if stringContains(settings.Scopes, admin) {
			t.Fatalf("default ceiling includes %s", admin)
		}
	}
	if !stringContains(settings.Scopes, "read-file") || len(settings.Scopes) < 20 {
		t.Fatalf("default ceiling=%v", settings.Scopes)
	}
	// An explicit resource identifier is what the metadata advertises.
	saveSetting(t, a, "mcp", `{"oauthEnabled":true,"oauthResource":"https://edge.company/git-ctx/mcp"}`)
	rec := ssoRequest(a, "/.well-known/oauth-protected-resource/mcp", "", "")
	if !strings.Contains(rec.Body.String(), `"resource":"https://edge.company/git-ctx/mcp"`) {
		t.Fatalf("metadata=%s", rec.Body.String())
	}
	if challenge := ssoRequest(a, "/mcp", "", mcpInitialize).Header().Get("WWW-Authenticate"); !strings.Contains(challenge, `resource_metadata="https://edge.company/git-ctx/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("challenge=%q", challenge)
	}
}

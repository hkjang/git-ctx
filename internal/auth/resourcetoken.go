package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// An SSO access token presented to /mcp. The MCP authorization specification
// makes this server a resource server: Keycloak issues the token, and the
// only questions here are whether Keycloak really signed it and whether it
// was issued for this server rather than for some other application in the
// realm. The web sign-in's verifier answers the first question but not the
// second — it accepts a token issued to the web client, and an MCP client is
// a different Keycloak client whose token names this server in aud or itself
// in azp.

// ResourceToken is what a verified SSO access token says about its holder.
type ResourceToken struct {
	Subject string
	// Username is the configured username claim, empty when the token lacks it.
	Username string
	// ClientID is azp: the Keycloak client that obtained the token.
	ClientID string
	Audience []string
	// Scopes is the token's scope claim split on whitespace.
	Scopes []string
	Expiry time.Time
}

// AudienceError says which audience the token carried and what would have
// been accepted, so the operator who reads the refusal can finish configuring
// either side without a second round trip.
type AudienceError struct {
	Audience []string
	ClientID string
	Resource string
}

func (e *AudienceError) Error() string {
	return fmt.Sprintf("token audience %v / azp %q names neither the resource %q nor an allowed audience", e.Audience, e.ClientID, e.Resource)
}

// resourceSigningAlgs is every asymmetric algorithm go-oidc knows. An HMAC
// token is one anybody holding the (public) verification input could have
// minted, and "none" is not a signature at all; both are refused before the
// key set is consulted.
var resourceSigningAlgs = []string{oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512}

// VerifyResourceToken checks an access token against the configured issuer's
// key set and against this server's identity. resource is what the token's
// aud must name (RFC 8707); audiences are values an administrator accepts in
// aud or azp instead, because a Keycloak 26 access token carries the client in
// azp and only "account" in aud unless a mapper says otherwise.
//
// Refused: a bad signature, another issuer, an expired or not-yet-valid
// token, an ID token (typ=ID — proof of login, not an API credential), a
// token bound to a proof of possession this server cannot check (cnf), an
// empty subject, and a token for somebody else (AudienceError).
func (v *OIDCVerifier) VerifyResourceToken(ctx context.Context, raw, resource string, audiences []string) (ResourceToken, error) {
	cfg, err := v.load(ctx)
	if err != nil {
		return ResourceToken{}, err
	}
	cfg.defaults()
	if cfg.IssuerURL == "" {
		return ResourceToken{}, errors.New("Keycloak OIDC is not configured")
	}
	verifier, err := v.resourceVerifier(ctx, cfg)
	if err != nil {
		return ResourceToken{}, err
	}
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		return ResourceToken{}, fmt.Errorf("verify access token: %w", err)
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return ResourceToken{}, fmt.Errorf("access token claims: %w", err)
	}
	skew := time.Duration(cfg.AllowedClockSkewSeconds) * time.Second
	if notBefore, ok := numberClaim(claims, "nbf"); ok && time.Now().Add(skew).Before(time.Unix(int64(notBefore), 0)) {
		return ResourceToken{}, errors.New("access token is not valid yet (nbf)")
	}
	if strings.EqualFold(stringClaim(claims, "typ"), "ID") {
		return ResourceToken{}, errors.New("an ID token is not an access token (typ=ID)")
	}
	if _, bound := claims["cnf"]; bound {
		return ResourceToken{}, errors.New("access token is bound to a proof of possession this server cannot verify (cnf)")
	}
	if strings.TrimSpace(token.Subject) == "" {
		return ResourceToken{}, errors.New("access token has no subject")
	}
	out := ResourceToken{
		Subject:  token.Subject,
		Username: stringClaim(claims, cfg.UsernameClaim),
		ClientID: stringClaim(claims, "azp"),
		Audience: slices.Clone(token.Audience),
		Scopes:   strings.Fields(stringClaim(claims, "scope")),
		Expiry:   token.Expiry,
	}
	forUs := slices.Contains(out.Audience, resource)
	for _, accepted := range audiences {
		if accepted == "" {
			continue
		}
		if slices.Contains(out.Audience, accepted) || out.ClientID == accepted {
			forUs = true
		}
	}
	if !forUs {
		return ResourceToken{}, &AudienceError{Audience: out.Audience, ClientID: out.ClientID, Resource: resource}
	}
	return out, nil
}

// resourceVerifier is cached like the sign-in verifiers, but built on a
// context that outlives the request: the provider keeps it for later key
// fetches, and a key rotation must not fail because the first caller hung up.
func (v *OIDCVerifier) resourceVerifier(ctx context.Context, cfg OIDCConfig) (*oidc.IDTokenVerifier, error) {
	key := verifierKey(cfg)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.resource != nil && v.resourceKey == key && time.Now().Before(v.resourceExpires) {
		return v.resource, nil
	}
	oidcCtx, err := oidcContext(context.WithoutCancel(ctx), cfg)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(oidcCtx, strings.TrimSuffix(cfg.IssuerURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	skew := time.Duration(cfg.AllowedClockSkewSeconds) * time.Second
	// The audience is compared above against more than one acceptable value;
	// the library compares against exactly one, so it is told to stand aside.
	v.resource = provider.VerifierContext(oidcCtx, &oidc.Config{
		SkipClientIDCheck:    true,
		SupportedSigningAlgs: resourceSigningAlgs,
		Now:                  func() time.Time { return time.Now().Add(-skew) },
	})
	v.resourceKey = key
	v.resourceExpires = time.Now().Add(10 * time.Minute)
	return v.resource, nil
}

func numberClaim(m map[string]any, key string) (float64, bool) {
	value, ok := m[key].(float64)
	return value, ok
}

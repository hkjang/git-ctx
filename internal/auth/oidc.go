package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"git-ctx/internal/netclient"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	IssuerURL               string            `json:"issuerUrl"`
	ClientID                string            `json:"clientId"`
	ClientSecret            string            `json:"clientSecret"`
	RedirectURL             string            `json:"redirectUrl"`
	PostLogoutRedirectURL   string            `json:"postLogoutRedirectUrl"`
	Scopes                  []string          `json:"scopes"`
	UsernameClaim           string            `json:"usernameClaim"`
	EmailClaim              string            `json:"emailClaim"`
	GroupsClaim             string            `json:"groupsClaim"`
	BitbucketUserSlugClaim  string            `json:"bitbucketUserSlugClaim"`
	GitLabUserIDClaim       string            `json:"gitlabUserIdClaim"`
	RealmRoleMappings       map[string]string `json:"realmRoleMappings"`
	ClientRoleMappings      map[string]string `json:"clientRoleMappings"`
	BitbucketGroupMappings  map[string]string `json:"bitbucketGroupMappings"`
	AllowedClockSkewSeconds int               `json:"allowedClockSkewSeconds"`
	TLSVerify               *bool             `json:"tlsVerify"`
	CACertificate           string            `json:"caCertificate"`
	ProxyURL                string            `json:"proxyUrl"`
	TimeoutSeconds          int               `json:"timeoutSeconds"`
}

func (c *OIDCConfig) defaults() {
	if c.UsernameClaim == "" {
		c.UsernameClaim = "preferred_username"
	}
	if c.EmailClaim == "" {
		c.EmailClaim = "email"
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = "groups"
	}
	if c.BitbucketUserSlugClaim == "" {
		c.BitbucketUserSlugClaim = "bitbucket_user_slug"
	}
	if c.GitLabUserIDClaim == "" {
		c.GitLabUserIDClaim = "gitlab_user_id"
	}
	if len(c.Scopes) == 0 {
		c.Scopes = []string{oidc.ScopeOpenID, "profile", "email", "groups"}
	}
}

type Identity struct {
	Subject, Username, Email, BitbucketUserSlug, GitLabUserID string
	Roles, Groups                                             []string
	ACLGroups                                                 []string
}

type OIDCVerifier struct {
	load     func(context.Context) (OIDCConfig, error)
	mu       sync.Mutex
	key      string
	verifier *oidc.IDTokenVerifier
	expires  time.Time
}

func NewOIDCVerifier(loader func(context.Context) (OIDCConfig, error)) *OIDCVerifier {
	return &OIDCVerifier{load: loader}
}

func ValidateOIDCConfig(ctx context.Context, cfg OIDCConfig) error {
	cfg.defaults()
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return errors.New("issuerUrl and clientId are required")
	}
	if cfg.AllowedClockSkewSeconds < 0 || cfg.AllowedClockSkewSeconds > 300 {
		return errors.New("allowedClockSkewSeconds must be between 0 and 300")
	}
	ctx, err := oidcContext(ctx, cfg)
	if err != nil {
		return err
	}
	_, err = oidc.NewProvider(ctx, strings.TrimSuffix(cfg.IssuerURL, "/"))
	if err != nil {
		return fmt.Errorf("OIDC discovery: %w", err)
	}
	validRoles := map[string]bool{
		"platform-admin": true, "security-admin": true, "mcp-admin": true,
		"source-admin": true, "search-admin": true, "auditor": true,
		"developer": true, "service-account": true, "readonly-operator": true,
	}
	for _, mappings := range []map[string]string{cfg.RealmRoleMappings, cfg.ClientRoleMappings} {
		for _, target := range mappings {
			if !validRoles[target] {
				return fmt.Errorf("unsupported platform role mapping %q", target)
			}
		}
	}
	return nil
}

func OAuthConfig(ctx context.Context, cfg OIDCConfig) (oauth2.Config, error) {
	cfg.defaults()
	if err := ValidateOIDCConfig(ctx, cfg); err != nil {
		return oauth2.Config{}, err
	}
	if cfg.RedirectURL == "" {
		return oauth2.Config{}, errors.New("redirectUrl is required for browser login")
	}
	ctx, err := oidcContext(ctx, cfg)
	if err != nil {
		return oauth2.Config{}, err
	}
	provider, err := oidc.NewProvider(ctx, strings.TrimSuffix(cfg.IssuerURL, "/"))
	if err != nil {
		return oauth2.Config{}, err
	}
	return oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		Endpoint: provider.Endpoint(), RedirectURL: cfg.RedirectURL, Scopes: cfg.Scopes,
	}, nil
}

func EndSessionURL(ctx context.Context, cfg OIDCConfig) (string, error) {
	cfg.defaults()
	ctx, err := oidcContext(ctx, cfg)
	if err != nil {
		return "", err
	}
	provider, err := oidc.NewProvider(ctx, strings.TrimSuffix(cfg.IssuerURL, "/"))
	if err != nil {
		return "", err
	}
	var metadata struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return "", err
	}
	if metadata.EndSessionEndpoint == "" {
		return "", errors.New("OIDC provider does not advertise end_session_endpoint")
	}
	target, err := url.Parse(metadata.EndSessionEndpoint)
	if err != nil {
		return "", err
	}
	q := target.Query()
	q.Set("client_id", cfg.ClientID)
	if cfg.PostLogoutRedirectURL != "" {
		q.Set("post_logout_redirect_uri", cfg.PostLogoutRedirectURL)
	}
	target.RawQuery = q.Encode()
	return target.String(), nil
}

func (v *OIDCVerifier) Verify(ctx context.Context, raw string) (Identity, error) {
	cfg, err := v.load(ctx)
	if err != nil {
		return Identity{}, err
	}
	cfg.defaults()
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return Identity{}, errors.New("Keycloak OIDC is not configured")
	}
	verifier, err := v.get(ctx, cfg)
	if err != nil {
		return Identity{}, err
	}
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("verify OIDC token: %w", err)
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return Identity{}, err
	}
	id := Identity{Subject: token.Subject, Username: stringClaim(claims, cfg.UsernameClaim), Email: stringClaim(claims, cfg.EmailClaim), Groups: stringSliceClaim(claims, cfg.GroupsClaim), BitbucketUserSlug: stringClaim(claims, cfg.BitbucketUserSlugClaim), GitLabUserID: stringClaim(claims, cfg.GitLabUserIDClaim)}
	if id.Subject == "" || id.Username == "" {
		return Identity{}, errors.New("required Keycloak subject or username claim is missing")
	}
	var realmRoles []string
	if access, ok := claims["realm_access"].(map[string]any); ok {
		realmRoles = stringSliceClaim(access, "roles")
	}
	for _, role := range realmRoles {
		if mapped := cfg.RealmRoleMappings[role]; mapped != "" && !slices.Contains(id.Roles, mapped) {
			id.Roles = append(id.Roles, mapped)
		}
	}
	if resources, ok := claims["resource_access"].(map[string]any); ok {
		if client, ok := resources[cfg.ClientID].(map[string]any); ok {
			for _, role := range stringSliceClaim(client, "roles") {
				if mapped := cfg.ClientRoleMappings[role]; mapped != "" && !slices.Contains(id.Roles, mapped) {
					id.Roles = append(id.Roles, mapped)
				}
			}
		}
	}
	if len(id.Roles) == 0 {
		id.Roles = []string{"developer"}
	}
	for _, group := range id.Groups {
		if mapped := cfg.BitbucketGroupMappings[group]; mapped != "" {
			id.ACLGroups = append(id.ACLGroups, "group:"+mapped)
		}
	}
	return id, nil
}

func (v *OIDCVerifier) get(ctx context.Context, cfg OIDCConfig) (*oidc.IDTokenVerifier, error) {
	transportKey := sha256.Sum256([]byte(fmt.Sprintf("%v|%s|%s", cfg.TLSVerify, cfg.CACertificate, cfg.ProxyURL)))
	key := strings.TrimSuffix(cfg.IssuerURL, "/") + "|" + cfg.ClientID + "|" + fmt.Sprintf("%x", transportKey)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verifier != nil && v.key == key && time.Now().Before(v.expires) {
		return v.verifier, nil
	}
	ctx, err := oidcContext(ctx, cfg)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(ctx, strings.TrimSuffix(cfg.IssuerURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	skew := time.Duration(cfg.AllowedClockSkewSeconds) * time.Second
	v.verifier = provider.VerifierContext(ctx, &oidc.Config{ClientID: cfg.ClientID, Now: func() time.Time { return time.Now().Add(-skew) }})
	v.key = key
	v.expires = time.Now().Add(10 * time.Minute)
	return v.verifier, nil
}
func oidcContext(ctx context.Context, cfg OIDCConfig) (context.Context, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client, err := netclient.New(netclient.Config{Timeout: timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	return oidc.ClientContext(ctx, client), nil
}
func stringClaim(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func stringSliceClaim(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		if s, ok := m[key].([]string); ok {
			return s
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

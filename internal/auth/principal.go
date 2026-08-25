package auth

import "context"

type Principal struct {
	UserID              string
	Subject             string
	Username            string
	ACLPrincipal        string
	ACLPrincipals       []string
	Roles               []string
	Groups              []string
	KeyID               string
	KeyPrefix           string
	Scopes              []string
	AllowedRepositories []string
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

type clientIPKey struct{}

// WithClientIP carries the caller's address as the HTTP layer resolved it,
// including its decision about whether a forwarding header could be trusted.
// Components further in must not re-derive it: a key restricted by CIDR and the
// audit row explaining the call have to agree on who called, and an address
// that still carries its ephemeral port cannot be matched against either.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIP returns the resolved caller address, or an empty string.
func ClientIP(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

func (p Principal) HasRole(role string) bool {
	for _, item := range p.Roles {
		if item == role || item == "platform-admin" {
			return true
		}
	}
	return false
}

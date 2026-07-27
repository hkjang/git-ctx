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

func (p Principal) HasRole(role string) bool {
	for _, item := range p.Roles {
		if item == role || item == "platform-admin" {
			return true
		}
	}
	return false
}

package mcp

import (
	"strings"

	"git-ctx/internal/auth"
	"git-ctx/internal/search"
	"net/http"
)

// Argument decoding and the small helpers shared across tools.

func stringArg(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func stringSliceArg(m map[string]any, k string) []string {
	values, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}
func intArg(m map[string]any, k string, fallback int) int {
	value, ok := m[k].(float64)
	if !ok || value != float64(int(value)) {
		return fallback
	}
	return int(value)
}
func baseLibraryID(id string) string {
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return "/" + parts[0] + "/" + parts[1]
}
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return runeSafeCut(value, limit) + "…"
}
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		host = strings.TrimSpace(strings.Split(x, ",")[0])
	}
	return host
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func libraryAllowed(libraryID string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(libraryID), "/"), "/")
	if len(parts) < 2 {
		return false
	}
	base := "/" + parts[0] + "/" + parts[1]
	for _, item := range allowed {
		if strings.EqualFold(item, base) {
			return true
		}
	}
	return false
}
func filterLibraries(items []search.Library, allowed []string) []search.Library {
	out := items[:0]
	for _, item := range items {
		if libraryAllowed(item.ID, allowed) {
			out = append(out, item)
		}
	}
	return out
}

// principalACLs resolves the principals used for every MCP search. Platform,
// source and search administrators receive the catalog-wide principal so their
// tools work without a Bitbucket or GitLab account.
func principalACLs(p auth.Principal) []string {
	var principals []string
	switch {
	case len(p.ACLPrincipals) > 0:
		principals = p.ACLPrincipals
	case p.ACLPrincipal != "":
		principals = []string{p.ACLPrincipal}
	}
	return search.WithUnrestricted(principals, p.Roles)
}

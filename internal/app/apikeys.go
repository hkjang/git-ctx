package app

import (
	"net/http"
	"time"

	"git-ctx/internal/apikey"
	"git-ctx/internal/auth"
	"git-ctx/internal/toolcatalog"
)

// MCP API key lifecycle for the signed-in user.

func (a *App) listKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	v, e := a.keys.List(r.Context(), p.UserID)
	if e != nil {
		problem(w, 500, "internal_error", "Unable to list keys")
		return
	}
	jsonOut(w, 200, v)
}
func (a *App) createKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.UserID == "break-glass-admin" {
		problem(w, http.StatusForbidden, "recovery_session_restricted", "Recovery sessions cannot create persistent MCP API keys")
		return
	}
	var in struct {
		Name         string              `json:"name"`
		Scopes       []string            `json:"scopes"`
		ExpiresAt    *time.Time          `json:"expiresAt"`
		Restrictions apikey.Restrictions `json:"restrictions"`
	}
	if decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "Invalid JSON")
		return
	}
	if !keyScopesAllowed(p, in.Scopes) {
		problem(w, http.StatusForbidden, "forbidden_scope", "The current administrator role cannot grant the requested MCP management tool")
		return
	}
	k, plain, e := a.keys.CreateWithRestrictions(r.Context(), p.UserID, in.Name, in.Scopes, in.ExpiresAt, in.Restrictions)
	if e != nil {
		problem(w, 400, "invalid_request", e.Error())
		return
	}
	a.audit(r, p, "api_key.create", "api_key", k.ID, "success", map[string]any{"prefix": k.Prefix})
	jsonOut(w, 201, map[string]any{"key": k, "secret": plain, "notice": "This value is shown once and cannot be recovered."})
}

func keyScopesAllowed(p auth.Principal, scopes []string) bool {
	for _, scope := range scopes {
		switch scope {
		case toolcatalog.GetPlatformStatus:
			if !roleAllowed(p, "readonly-operator", "source-admin", "mcp-admin", "search-admin", "security-admin", "auditor") {
				return false
			}
		case toolcatalog.ListIndexJobs:
			if !roleAllowed(p, "source-admin", "readonly-operator") {
				return false
			}
		case toolcatalog.ReindexRepository:
			if !roleAllowed(p, "source-admin") {
				return false
			}
		}
	}
	return true
}

func (a *App) updateKeyScopes(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Scopes []string `json:"scopes"`
	}
	if decode(r, &in) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "scopes are required")
		return
	}
	if !keyScopesAllowed(p, in.Scopes) {
		problem(w, http.StatusForbidden, "forbidden_scope", "The current administrator role cannot grant the requested MCP management tool")
		return
	}
	if err := a.keys.UpdateScopes(r.Context(), p.UserID, r.PathValue("id"), in.Scopes); err != nil {
		problem(w, http.StatusBadRequest, "scope_update_failed", err.Error())
		return
	}
	a.audit(r, p, "api_key.scopes_update", "api_key", r.PathValue("id"), "success", map[string]any{"scopes": in.Scopes})
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) disableKey(w http.ResponseWriter, r *http.Request) { a.keyStatus(w, r, "disabled") }
func (a *App) enableKey(w http.ResponseWriter, r *http.Request)  { a.keyStatus(w, r, "enabled") }
func (a *App) revokeKey(w http.ResponseWriter, r *http.Request)  { a.keyStatus(w, r, "revoked") }
func (a *App) rotateKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		OverlapMinutes int `json:"overlapMinutes"`
	}
	if r.ContentLength > 0 && decode(r, &in) != nil {
		problem(w, 400, "invalid_request", "Invalid rotation request")
		return
	}
	if in.OverlapMinutes < 0 || in.OverlapMinutes > 1440 {
		problem(w, 400, "invalid_overlap", "overlapMinutes must be between 0 and 1440")
		return
	}
	k, secret, err := a.keys.Rotate(r.Context(), p.UserID, r.PathValue("id"), time.Duration(in.OverlapMinutes)*time.Minute)
	if err != nil {
		problem(w, 400, "rotation_failed", err.Error())
		return
	}
	a.audit(r, p, "api_key.rotate", "api_key", r.PathValue("id"), "success", map[string]any{"replacementId": k.ID, "overlapMinutes": in.OverlapMinutes})
	jsonOut(w, 201, map[string]any{"key": k, "secret": secret, "notice": "This replacement key is shown once and cannot be recovered."})
}
func (a *App) keyStatus(w http.ResponseWriter, r *http.Request, status string) {
	p, _ := auth.FromContext(r.Context())
	if e := a.keys.SetStatus(r.Context(), p.UserID, r.PathValue("id"), status); e != nil {
		problem(w, 404, "not_found", "Key not found")
		return
	}
	a.audit(r, p, "api_key."+status, "api_key", r.PathValue("id"), "success", nil)
	w.WriteHeader(204)
}

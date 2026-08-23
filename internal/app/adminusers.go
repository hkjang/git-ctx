package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"git-ctx/internal/auth"
)

// Administrative user, key, managed-secret and audit endpoints.

func (a *App) auditLogs(w http.ResponseWriter, r *http.Request) {
	rows, e := a.store.DB.QueryContext(r.Context(), `SELECT occurred_at,actor_id,action,resource_type,resource_id,outcome,ip_address,metadata FROM audit_logs ORDER BY occurred_at DESC LIMIT 200`)
	if e != nil {
		problem(w, 500, "internal_error", e.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var t time.Time
		var actor, action, rt, rid, outcome, ip, metadata string
		_ = rows.Scan(&t, &actor, &action, &rt, &rid, &outcome, &ip, &metadata)
		out = append(out, map[string]any{"at": t, "actor": actor, "action": action, "resourceType": rt, "resourceId": rid, "outcome": outcome, "ip": ip})
	}
	jsonOut(w, 200, out)
}

type adminUserInput struct {
	Subject  string   `json:"subject"`
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Status   string   `json:"status"`
	Roles    []string `json:"roles"`
}

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT id,subject,username,email,status,created_at FROM users WHERE id NOT IN ('bootstrap-admin','break-glass-admin') ORDER BY username`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	type listedUser struct {
		id, subject, username, email, status string
		created                              time.Time
	}
	users := make([]listedUser, 0)
	for rows.Next() {
		var user listedUser
		if err = rows.Scan(&user.id, &user.subject, &user.username, &user.email, &user.status, &user.created); err != nil {
			rows.Close()
			problem(w, 500, "internal_error", err.Error())
			return
		}
		users = append(users, user)
	}
	if err = rows.Close(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		roles, roleErr := a.userRoles(r.Context(), user.id)
		if roleErr != nil {
			problem(w, 500, "internal_error", roleErr.Error())
			return
		}
		out = append(out, map[string]any{"id": user.id, "subject": user.subject, "username": user.username, "email": user.email, "status": user.status, "roles": roles, "createdAt": user.created})
	}
	jsonOut(w, 200, map[string]any{"users": out, "roles": platformRoles})
}

func validateAdminUserInput(in *adminUserInput, requireSubject bool) error {
	in.Subject, in.Username, in.Email, in.Status = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Username), strings.TrimSpace(in.Email), strings.TrimSpace(in.Status)
	if in.Username == "" || (requireSubject && in.Subject == "") {
		return errors.New("subject and username are required")
	}
	if len(in.Username) > 200 || len(in.Subject) > 500 || len(in.Email) > 320 {
		return errors.New("user field is too long")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.Status != "active" && in.Status != "disabled" {
		return errors.New("status must be active or disabled")
	}
	uniqueRoles := make([]string, 0, len(in.Roles))
	for _, role := range in.Roles {
		if !slices.Contains(platformRoles, role) {
			return fmt.Errorf("unsupported role: %s", role)
		}
		if !slices.Contains(uniqueRoles, role) {
			uniqueRoles = append(uniqueRoles, role)
		}
	}
	in.Roles = uniqueRoles
	if len(in.Roles) == 0 {
		in.Roles = []string{"developer"}
	}
	return nil
}

func (a *App) createAdminUser(w http.ResponseWriter, r *http.Request) {
	var in adminUserInput
	if decode(r, &in) != nil || validateAdminUserInput(&in, true) != nil {
		problem(w, 400, "invalid_user", "Subject, username, status, and roles must be valid")
		return
	}
	id, err := randomToken(18)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	if err = a.writeAdminUser(r.Context(), id, in, true); err != nil {
		problem(w, 409, "user_create_failed", "Subject must be unique and roles must be valid")
		return
	}
	p, _ := auth.FromContext(r.Context())
	a.audit(r, p, "user.create", "user", id, "success", map[string]any{"roles": in.Roles, "status": in.Status})
	jsonOut(w, 201, map[string]any{"id": id})
}

func (a *App) updateAdminUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in adminUserInput
	if id == "" || decode(r, &in) != nil || validateAdminUserInput(&in, false) != nil {
		problem(w, 400, "invalid_user", "Username, status, and roles must be valid")
		return
	}
	p, _ := auth.FromContext(r.Context())
	if id == p.UserID && (in.Status != "active" || !slices.Contains(in.Roles, "platform-admin")) {
		problem(w, 409, "self_lockout", "You cannot disable yourself or remove your own platform-admin role")
		return
	}
	if err := a.writeAdminUser(r.Context(), id, in, false); err != nil {
		problem(w, 404, "user_not_found", "User was not found")
		return
	}
	a.audit(r, p, "user.update", "user", id, "success", map[string]any{"roles": in.Roles, "status": in.Status})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) writeAdminUser(ctx context.Context, id string, in adminUserInput, create bool) error {
	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO users(id,subject,username,email,status,roles_managed) VALUES(?,?,?,?,?,1)`), id, in.Subject, in.Username, in.Email, in.Status)
	} else {
		result, updateErr := tx.ExecContext(ctx, a.store.Rebind(`UPDATE users SET username=?,email=?,status=?,roles_managed=1 WHERE id=? AND id NOT IN ('bootstrap-admin','break-glass-admin')`), in.Username, in.Email, in.Status, id)
		err = updateErr
		if err == nil {
			if affected, _ := result.RowsAffected(); affected == 0 {
				return sql.ErrNoRows
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM user_roles WHERE user_id=?`), id); err != nil {
		return err
	}
	for _, role := range in.Roles {
		if _, err = tx.ExecContext(ctx, a.store.Rebind(`INSERT INTO user_roles(user_id,role_code) VALUES(?,?)`), id, role); err != nil {
			return err
		}
	}
	if in.Status != "active" {
		_, _ = tx.ExecContext(ctx, a.store.Rebind(`DELETE FROM user_sessions WHERE user_id=?`), id)
		_, _ = tx.ExecContext(ctx, a.store.Rebind(`UPDATE api_keys SET revoked_at=CURRENT_TIMESTAMP WHERE user_id=? AND revoked_at IS NULL`), id)
	}
	return tx.Commit()
}

func (a *App) deleteAdminUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := auth.FromContext(r.Context())
	if id == "" || id == p.UserID {
		problem(w, 409, "self_lockout", "You cannot delete your own administrator account")
		return
	}
	tx, err := a.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), a.store.Rebind(`UPDATE users SET status='deleted' WHERE id=? AND id NOT IN ('bootstrap-admin','break-glass-admin')`), id)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		problem(w, 404, "user_not_found", "User was not found")
		return
	}
	_, _ = tx.ExecContext(r.Context(), a.store.Rebind(`DELETE FROM user_sessions WHERE user_id=?`), id)
	_, _ = tx.ExecContext(r.Context(), a.store.Rebind(`UPDATE api_keys SET revoked_at=CURRENT_TIMESTAMP WHERE user_id=? AND revoked_at IS NULL`), id)
	if err = tx.Commit(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	a.audit(r, p, "user.delete", "user", id, "success", nil)
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) adminAPIKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT k.id,u.username,k.name,k.prefix,k.scopes,k.expires_at,k.disabled_at,k.revoked_at,k.last_used_at,k.created_at FROM api_keys k JOIN users u ON u.id=k.user_id ORDER BY k.created_at DESC LIMIT 1000`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, username, name, prefix, scopes string
		var expires, disabled, revoked, used sql.NullTime
		var created time.Time
		if err := rows.Scan(&id, &username, &name, &prefix, &scopes, &expires, &disabled, &revoked, &used, &created); err != nil {
			return
		}
		status := "active"
		if disabled.Valid {
			status = "disabled"
		}
		if revoked.Valid {
			status = "revoked"
		}
		if expires.Valid && time.Now().After(expires.Time) {
			status = "expired"
		}
		item := map[string]any{"id": id, "username": username, "name": name, "prefix": prefix, "scopes": strings.Split(scopes, ","), "status": status, "createdAt": created}
		if expires.Valid {
			item["expiresAt"] = expires.Time
		}
		if used.Valid {
			item["lastUsedAt"] = used.Time
		}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func (a *App) adminRevokeKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`), time.Now().UTC(), r.PathValue("id"))
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		problem(w, 404, "not_found", "Active API key not found")
		return
	}
	a.audit(r, p, "api_key.admin_revoke", "api_key", r.PathValue("id"), "success", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) adminUpdateKeyScopes(w http.ResponseWriter, r *http.Request) {
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
	if err := a.keys.UpdateScopesAdmin(r.Context(), r.PathValue("id"), in.Scopes); err != nil {
		problem(w, http.StatusBadRequest, "scope_update_failed", err.Error())
		return
	}
	a.audit(r, p, "api_key.admin_scopes_update", "api_key", r.PathValue("id"), "success", map[string]any{"scopes": in.Scopes})
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) listManagedSecrets(w http.ResponseWriter, r *http.Request) {
	items, err := a.secrets.List(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, items)
}
func (a *App) putManagedSecret(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Name, Backend, Value string
	}
	if decode(r, &in) != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "name, backend, and value are required")
		return
	}
	if pathName := r.PathValue("name"); pathName != "" {
		in.Name = pathName
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	item, err := a.secrets.Put(ctx, in.Name, in.Backend, in.Value, p.UserID, "")
	if err != nil {
		a.audit(r, p, "secret.write", "managed_secret", in.Name, "failure", map[string]any{"backend": in.Backend, "error": truncateText(err.Error(), 300)})
		problem(w, http.StatusBadRequest, "secret_write_failed", err.Error())
		return
	}
	a.audit(r, p, "secret.write", "managed_secret", in.Name, "success", map[string]any{"backend": item.Backend, "version": item.Version})
	jsonOut(w, http.StatusCreated, item)
}
func (a *App) disableManagedSecret(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	name := r.PathValue("name")
	if err := a.secrets.Disable(r.Context(), name, p.UserID); err != nil {
		problem(w, http.StatusNotFound, "secret_not_found", err.Error())
		return
	}
	a.audit(r, p, "secret.disable", "managed_secret", name, "success", nil)
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) securityEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT occurred_at,repository_id,ref_name,file_path,finding_type,action FROM index_security_events ORDER BY occurred_at DESC LIMIT 500`)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var at time.Time
		var repo, ref, path, finding, action string
		if err := rows.Scan(&at, &repo, &ref, &path, &finding, &action); err != nil {
			return
		}
		out = append(out, map[string]any{"occurredAt": at, "repositoryId": repo, "refName": ref, "filePath": path, "findingType": finding, "action": action})
	}
	jsonOut(w, 200, out)
}

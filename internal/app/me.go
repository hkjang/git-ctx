package app

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/search"
	"git-ctx/internal/version"
)

// Endpoints that answer for the caller themselves.

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	jsonOut(w, 200, struct {
		auth.Principal
		Version string `json:"Version"`
	}{Principal: p, Version: version.Version})
}
func (a *App) meRepositories(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	principals := searchPrincipals(p)
	if len(principals) == 0 {
		jsonOut(w, 200, []any{})
		return
	}
	join, predicate, args := search.RepositoryACLClause(principals)
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT DISTINCT r.library_id,r.name,r.description,r.default_branch,r.reputation,r.indexed_at FROM repositories r `+join+` WHERE r.enabled=1 AND `+predicate+` ORDER BY r.library_id`), args...)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, name, description, branch, reputation string
		var indexed sql.NullTime
		if err := rows.Scan(&id, &name, &description, &branch, &reputation, &indexed); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		if p.KeyID != "" && !repositoryAllowed(id, p.AllowedRepositories) {
			continue
		}
		item := map[string]any{"libraryId": id, "name": name, "description": description, "defaultBranch": branch, "reputation": reputation}
		if indexed.Valid {
			item["indexedAt"] = indexed.Time
		}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func repositoryAllowed(id string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(id, item) {
			return true
		}
	}
	return false
}
func (a *App) meUsage(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT tool,outcome,COUNT(*),COALESCE(AVG(duration_ms),0),COALESCE(MAX(duration_ms),0),COALESCE(AVG(response_bytes),0),COALESCE(MAX(response_bytes),0),COALESCE(SUM(truncated),0),
-- What truncated calls produced and did not send. "Three calls were cut" does
-- not say whether a tail was trimmed or most of the answer was thrown away.
COALESCE(SUM(CASE WHEN truncated=1 THEN produced_bytes-response_bytes ELSE 0 END),0),
COALESCE(SUM(CASE WHEN truncated=1 THEN produced_bytes ELSE 0 END),0)
FROM mcp_calls WHERE user_id=? GROUP BY tool,outcome ORDER BY tool,outcome`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var tool, outcome string
		var count, truncated, maxBytes, discarded, produced int64
		var avg, maxLatency, averageBytes float64
		if err := rows.Scan(&tool, &outcome, &count, &avg, &maxLatency, &averageBytes, &maxBytes, &truncated, &discarded, &produced); err != nil {
			return
		}
		out = append(out, map[string]any{"tool": tool, "outcome": outcome, "calls": count, "averageLatencyMs": avg, "maximumLatencyMs": maxLatency,
			"averageResponseBytes": averageBytes, "maximumResponseBytes": maxBytes, "truncatedCalls": truncated,
			"discardedBytes": discarded, "producedBytes": produced})
	}
	jsonOut(w, 200, out)
}
func (a *App) meCalls(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT id,occurred_at,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,result_count,trace_summary FROM mcp_calls WHERE user_id=? ORDER BY occurred_at DESC LIMIT 200`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var at time.Time
		var id, prefix, tool, libraryID, outcome, ip, summary string
		var duration, results int64
		if err := rows.Scan(&id, &at, &prefix, &tool, &libraryID, &outcome, &duration, &ip, &results, &summary); err != nil {
			return
		}
		out = append(out, map[string]any{"id": id, "occurredAt": at, "apiKeyPrefix": prefix, "tool": tool, "libraryId": libraryID,
			"outcome": outcome, "durationMs": duration, "clientIp": ip, "resultCount": results, "traceSummary": summary})
	}
	jsonOut(w, 200, out)
}
func (a *App) meNotifications(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT id,notification_type,title,message,read_at,created_at FROM notifications WHERE user_id=? ORDER BY created_at DESC LIMIT 200`), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, kind, title, message string
		var read sql.NullTime
		var created time.Time
		if rows.Scan(&id, &kind, &title, &message, &read, &created) != nil {
			continue
		}
		item := map[string]any{"id": id, "type": kind, "title": title, "message": message, "createdAt": created, "read": read.Valid}
		out = append(out, item)
	}
	jsonOut(w, 200, out)
}
func (a *App) readNotification(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE notifications SET read_at=? WHERE id=? AND user_id=?`), time.Now().UTC(), r.PathValue("id"), p.UserID)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		problem(w, 404, "not_found", "Notification not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

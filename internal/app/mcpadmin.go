package app

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/calltrace"
	"git-ctx/internal/mcp"
	"git-ctx/internal/search"
)

// MCP operations surface: tool catalog, call analytics, traces and self-check.

func (a *App) mcpTools(w http.ResponseWriter, r *http.Request) {
	catalog := mcp.Catalog()
	for _, tool := range catalog {
		name, _ := tool["name"].(string)
		var enabled, timeout, cache, budget int
		if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT enabled,timeout_ms,cache_seconds,max_response_bytes FROM mcp_tools WHERE name=?`), name).Scan(&enabled, &timeout, &cache, &budget); err == nil {
			tool["enabled"] = enabled == 1
			tool["timeoutMs"] = timeout
			tool["cacheSeconds"] = cache
			// 0 means the server default, which the screen shows explicitly.
			tool["maxResponseBytes"] = budget
			tool["effectiveResponseBytes"] = budget
			if budget <= 0 {
				tool["effectiveResponseBytes"] = mcp.DefaultResponseBytes
			}
		}
	}
	jsonOut(w, 200, catalog)
}
func (a *App) updateMCPTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	found := false
	for _, tool := range mcp.Catalog() {
		if tool["name"] == name {
			found = true
			break
		}
	}
	if !found {
		problem(w, 404, "not_found", "MCP tool not found")
		return
	}
	var in struct {
		Enabled          bool `json:"enabled"`
		TimeoutMS        int  `json:"timeoutMs"`
		CacheSeconds     int  `json:"cacheSeconds"`
		MaxResponseBytes int  `json:"maxResponseBytes"`
	}
	if decode(r, &in) != nil || in.TimeoutMS < 100 || in.TimeoutMS > 120000 || in.CacheSeconds < 0 || in.CacheSeconds > 86400 {
		problem(w, 400, "invalid_request", "timeoutMs must be 100..120000 and cacheSeconds 0..86400")
		return
	}
	// 0 keeps the server default; anything else has to be a budget an answer can
	// actually fit in.
	if in.MaxResponseBytes != 0 && (in.MaxResponseBytes < mcp.MinResponseBytes || in.MaxResponseBytes > mcp.MaxResponseBytes) {
		problem(w, 400, "invalid_request", fmt.Sprintf("maxResponseBytes must be 0 for the default, or %d..%d", mcp.MinResponseBytes, mcp.MaxResponseBytes))
		return
	}
	p, _ := auth.FromContext(r.Context())
	_, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE mcp_tools SET enabled=?,timeout_ms=?,cache_seconds=?,max_response_bytes=?,updated_by=?,updated_at=? WHERE name=?`), boolInt(in.Enabled), in.TimeoutMS, in.CacheSeconds, in.MaxResponseBytes, p.UserID, time.Now().UTC(), name)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	a.audit(r, p, "mcp_tool.update", "mcp_tool", name, "success", map[string]any{"enabled": in.Enabled, "timeoutMs": in.TimeoutMS, "cacheSeconds": in.CacheSeconds, "maxResponseBytes": in.MaxResponseBytes})
	jsonOut(w, 200, map[string]any{"name": name, "enabled": in.Enabled, "timeoutMs": in.TimeoutMS, "cacheSeconds": in.CacheSeconds, "maxResponseBytes": in.MaxResponseBytes})
}

// mcpWindow maps the screen's window selector to a duration and a bucket size.
// Only three windows exist on purpose: an operator tuning a tool wants today,
// this week or this month, and every one of them has to stay a cheap query.
func mcpWindow(value string) (time.Duration, string, string) {
	switch value {
	case "24h":
		return 24 * time.Hour, "24h", "hour"
	case "30d":
		return 30 * 24 * time.Hour, "30d", "day"
	default:
		return 7 * 24 * time.Hour, "7d", "day"
	}
}

// bucketExpression renders a timestamp column as a bucket label. The two
// supported databases spell this differently and neither accepts the other's
// form, so the driver decides.
func (a *App) bucketExpression(column, unit string) string {
	if a.store.Driver() == "postgres" {
		return fmt.Sprintf(`to_char(date_trunc('%s', %s), 'YYYY-MM-DD"T"HH24:00:00Z')`, unit, column)
	}
	if unit == "hour" {
		return fmt.Sprintf(`strftime('%%Y-%%m-%%dT%%H:00:00Z', %s)`, column)
	}
	return fmt.Sprintf(`strftime('%%Y-%%m-%%dT00:00:00Z', %s)`, column)
}

// mcpToolStats is one row of the analytics table.
type mcpToolStats struct {
	Tool             string  `json:"tool"`
	Calls            int64   `json:"calls"`
	Success          int64   `json:"success"`
	Empty            int64   `json:"empty"`
	Errors           int64   `json:"errors"`
	CacheHits        int64   `json:"cacheHits"`
	Truncated        int64   `json:"truncated"`
	Users            int64   `json:"users"`
	AverageLatencyMS float64 `json:"averageLatencyMs"`
	P50LatencyMS     int64   `json:"p50LatencyMs"`
	P95LatencyMS     int64   `json:"p95LatencyMs"`
	MaxLatencyMS     int64   `json:"maxLatencyMs"`
	AverageBytes     float64 `json:"averageResponseBytes"`
	MaxBytes         int64   `json:"maximumResponseBytes"`
	AverageResults   float64 `json:"averageResultCount"`
	TimeoutMS        int     `json:"timeoutMs"`
	CacheSeconds     int     `json:"cacheSeconds"`
	BudgetBytes      int     `json:"responseBudgetBytes"`
}

// mcpAnalytics answers the questions an operator has about MCP traffic: which
// tools are used, which ones fail or come back empty, how long they take, how
// much context they spend, and what should be changed as a result. Every number
// is scoped to one window so a long-running deployment does not average away a
// problem that started yesterday.
func (a *App) mcpAnalytics(w http.ResponseWriter, r *http.Request) {
	duration, window, unit := mcpWindow(r.URL.Query().Get("window"))
	from := time.Now().UTC().Add(-duration)
	out := map[string]any{"window": window, "from": from, "generatedAt": time.Now().UTC()}

	var summary struct {
		Calls, Success, Empty, Errors, CacheHits, Truncated, Sessions int64
		AverageLatency, AverageBytes                                  float64
	}
	err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*),
COALESCE(SUM(CASE WHEN outcome='success' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(cache_hit),0), COALESCE(SUM(truncated),0),
COUNT(DISTINCT CASE WHEN session_id<>'' THEN session_id END),
COALESCE(AVG(duration_ms),0), COALESCE(AVG(response_bytes),0)
FROM mcp_calls WHERE occurred_at>=?`), from).Scan(&summary.Calls, &summary.Success, &summary.Empty, &summary.Errors,
		&summary.CacheHits, &summary.Truncated, &summary.Sessions, &summary.AverageLatency, &summary.AverageBytes)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	out["summary"] = map[string]any{
		"calls": summary.Calls, "success": summary.Success, "empty": summary.Empty, "errors": summary.Errors,
		"cacheHits": summary.CacheHits, "truncated": summary.Truncated, "sessions": summary.Sessions,
		"averageLatencyMs": summary.AverageLatency, "averageResponseBytes": summary.AverageBytes,
	}

	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT tool, COUNT(*),
COALESCE(SUM(CASE WHEN outcome='success' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(cache_hit),0), COALESCE(SUM(truncated),0), COUNT(DISTINCT user_id),
COALESCE(AVG(duration_ms),0), COALESCE(MAX(duration_ms),0),
COALESCE(AVG(response_bytes),0), COALESCE(MAX(response_bytes),0), COALESCE(AVG(result_count),0)
FROM mcp_calls WHERE occurred_at>=? GROUP BY tool ORDER BY COUNT(*) DESC`), from)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	var tools []mcpToolStats
	for rows.Next() {
		var item mcpToolStats
		var maxLatency float64
		if err = rows.Scan(&item.Tool, &item.Calls, &item.Success, &item.Empty, &item.Errors, &item.CacheHits,
			&item.Truncated, &item.Users, &item.AverageLatencyMS, &maxLatency, &item.AverageBytes, &item.MaxBytes, &item.AverageResults); err != nil {
			rows.Close()
			problem(w, 500, "internal_error", err.Error())
			return
		}
		item.MaxLatencyMS = int64(maxLatency)
		tools = append(tools, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	// Percentiles are read with an offset per tool rather than computed over the
	// whole table: the index on (tool, occurred_at) keeps each one small, and
	// neither SQLite nor an older PostgreSQL needs a window function for it.
	for index := range tools {
		tools[index].P50LatencyMS = a.latencyPercentile(r.Context(), tools[index].Tool, from, tools[index].Calls, 0.50)
		tools[index].P95LatencyMS = a.latencyPercentile(r.Context(), tools[index].Tool, from, tools[index].Calls, 0.95)
		// A tool that is no longer in the catalog still has calls in the window, so
		// the effective defaults are filled in before the lookup rather than left
		// at zero, which would produce a "raise the budget to 0" recommendation.
		tools[index].TimeoutMS, tools[index].BudgetBytes = 30000, mcp.DefaultResponseBytes
		var timeout, cache, budget int
		if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT timeout_ms,cache_seconds,max_response_bytes FROM mcp_tools WHERE name=?`), tools[index].Tool).Scan(&timeout, &cache, &budget); err == nil {
			tools[index].TimeoutMS, tools[index].CacheSeconds = timeout, cache
			if budget > 0 {
				tools[index].BudgetBytes = budget
			}
		}
	}
	out["tools"] = tools
	out["recommendations"] = mcpRecommendations(tools)

	group := func(key string, statement string) {
		list, err := a.groupedCounts(r.Context(), statement, from)
		if err == nil {
			out[key] = list
		}
	}
	group("clients", `SELECT CASE WHEN client_name='' THEN 'unknown' ELSE client_name || ' ' || client_version END, COUNT(*) FROM mcp_calls WHERE occurred_at>=? GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)
	group("libraries", `SELECT library_id, COUNT(*) FROM mcp_calls WHERE occurred_at>=? AND library_id<>'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)
	group("retrieval", `SELECT CASE WHEN retrieval_mode='' THEN 'unknown' ELSE retrieval_mode END, COUNT(*) FROM mcp_calls WHERE occurred_at>=? AND outcome<>'error' GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)
	group("errors", `SELECT CASE WHEN error_code='' THEN 'unknown' ELSE error_code END, COUNT(*) FROM mcp_calls WHERE occurred_at>=? AND outcome='error' GROUP BY 1 ORDER BY 2 DESC LIMIT 10`)

	// The questions that came back empty are the most actionable list on this
	// screen: each one is a repository that is not indexed, an ACL that is too
	// narrow, or a tool description that misleads the agent.
	emptyRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT tool, arguments_preview, COUNT(*) FROM mcp_calls
WHERE occurred_at>=? AND outcome='empty' AND arguments_preview<>'' GROUP BY tool, arguments_hash, arguments_preview ORDER BY COUNT(*) DESC LIMIT 15`), from)
	if err == nil {
		var unanswered []map[string]any
		for emptyRows.Next() {
			var tool, preview string
			var count int64
			if emptyRows.Scan(&tool, &preview, &count) == nil {
				unanswered = append(unanswered, map[string]any{"tool": tool, "arguments": preview, "calls": count})
			}
		}
		emptyRows.Close()
		out["unanswered"] = unanswered
	}

	bucket := a.bucketExpression("occurred_at", unit)
	timelineRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(fmt.Sprintf(`SELECT %s AS bucket, COUNT(*),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0)
FROM mcp_calls WHERE occurred_at>=? GROUP BY bucket ORDER BY bucket`, bucket)), from)
	if err == nil {
		var timeline []map[string]any
		for timelineRows.Next() {
			var label string
			var calls, errorCount, emptyCount int64
			if timelineRows.Scan(&label, &calls, &errorCount, &emptyCount) == nil {
				timeline = append(timeline, map[string]any{"bucket": label, "calls": calls, "errors": errorCount, "empty": emptyCount})
			}
		}
		timelineRows.Close()
		out["timeline"] = timeline
	}
	jsonOut(w, 200, out)
}

// latencyPercentile reads one ordered row instead of sorting in the process.
func (a *App) latencyPercentile(ctx context.Context, tool string, from time.Time, calls int64, quantile float64) int64 {
	if calls <= 0 {
		return 0
	}
	offset := int64(float64(calls-1) * quantile)
	var value int64
	if err := a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT duration_ms FROM mcp_calls WHERE tool=? AND occurred_at>=? ORDER BY duration_ms LIMIT 1 OFFSET ?`), tool, from, offset).Scan(&value); err != nil {
		return 0
	}
	return value
}

func (a *App) groupedCounts(ctx context.Context, statement string, from time.Time) ([]map[string]any, error) {
	rows, err := a.store.DB.QueryContext(ctx, a.store.Rebind(statement), from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var label string
		var count int64
		if err = rows.Scan(&label, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"label": label, "calls": count})
	}
	return out, rows.Err()
}

// mcpRecommendations turns the measurements into the change an operator should
// make. Each one carries the field and value the settings form would set, so
// the screen can offer to apply it instead of asking the reader to do the
// arithmetic.
func mcpRecommendations(tools []mcpToolStats) []map[string]any {
	out := []map[string]any{}
	add := func(tool, severity, message, field string, value int) {
		item := map[string]any{"tool": tool, "severity": severity, "message": message}
		if field != "" {
			item["field"], item["value"] = field, value
		}
		out = append(out, item)
	}
	for _, item := range tools {
		if item.Calls < 5 {
			continue
		}
		share := func(count int64) float64 { return float64(count) / float64(item.Calls) }
		if rate := share(item.Truncated); rate >= 0.2 {
			suggested := min(item.BudgetBytes*2, mcp.MaxResponseBytes)
			add(item.Tool, "warning", fmt.Sprintf("응답의 %.0f%%가 예산(%d KB)에서 잘렸습니다. 에이전트가 같은 질문을 반복하게 됩니다.", rate*100, item.BudgetBytes/1024),
				"maxResponseBytes", suggested)
		}
		if item.TimeoutMS > 0 && item.P95LatencyMS > int64(float64(item.TimeoutMS)*0.8) {
			suggested := min(item.TimeoutMS*2, 120000)
			add(item.Tool, "warning", fmt.Sprintf("p95 지연 %d ms가 Timeout %d ms에 근접합니다. 타임아웃 직전 응답은 결과가 잘린 채 반환됩니다.", item.P95LatencyMS, item.TimeoutMS),
				"timeoutMs", suggested)
		}
		if rate := share(item.Empty); rate >= 0.4 {
			add(item.Tool, "critical", fmt.Sprintf("빈 응답 비율 %.0f%%. 색인 진단과 저장소 ACL을 먼저 확인하세요. 아래 '답하지 못한 질문' 목록이 원인을 좁혀 줍니다.", rate*100), "", 0)
		}
		if rate := share(item.Errors); rate >= 0.1 {
			add(item.Tool, "critical", fmt.Sprintf("오류율 %.0f%%. 오류 코드 분포에서 timeout과 forbidden을 구분해 대응하세요.", rate*100), "", 0)
		}
		if item.CacheSeconds == 0 && item.Calls >= 50 && item.AverageLatencyMS > 1000 {
			add(item.Tool, "info", fmt.Sprintf("캐시가 꺼져 있고 평균 %.0f ms가 걸립니다. 짧은 캐시만으로 반복 호출을 크게 줄일 수 있습니다.", item.AverageLatencyMS),
				"cacheSeconds", 30)
		}
	}
	return out
}

// mcpCalls is the audit view of individual calls. It is deliberately separate
// from the aggregate screen: aggregates tune the platform, this reconstructs
// what one credential or one session actually did.
func (a *App) mcpCalls(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	duration, window, _ := mcpWindow(query.Get("window"))
	from := time.Now().UTC().Add(-duration)
	where := []string{"occurred_at>=?"}
	args := []any{from}
	// Fixed order: a map would build a differently ordered statement on every
	// request, which defeats the driver's prepared-statement cache and makes the
	// slow query log unreadable.
	for _, filter := range []struct{ column, value string }{
		{"tool", query.Get("tool")}, {"outcome", query.Get("outcome")}, {"user_id", query.Get("user")},
		{"api_key_prefix", query.Get("keyPrefix")}, {"session_id", query.Get("session")}, {"error_code", query.Get("errorCode")},
	} {
		if filter.value != "" {
			where = append(where, filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	if search := strings.TrimSpace(query.Get("q")); search != "" {
		where = append(where, "(arguments_preview LIKE ? OR library_id LIKE ?)")
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	condition := strings.Join(where, " AND ")
	limit := 100
	if value, err := strconv.Atoi(query.Get("limit")); err == nil && value > 0 && value <= 1000 {
		limit = value
	}
	offset := 0
	if value, err := strconv.Atoi(query.Get("offset")); err == nil && value > 0 {
		offset = value
	}
	var total int64
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM mcp_calls WHERE `+condition), args...).Scan(&total); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	statement := `SELECT id,occurred_at,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,result_count,cache_hit,error_code,retrieval_mode,trace_summary
FROM mcp_calls WHERE ` + condition + ` ORDER BY occurred_at DESC LIMIT ? OFFSET ?`
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(statement), append(args, limit, offset)...)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var at time.Time
		var id, user, prefix, tool, libraryID, outcome, ip, session, requestID, clientName, clientVersion, preview, code, mode, summary string
		var latency, bytes, results int64
		var truncated, cacheHit int
		if err = rows.Scan(&id, &at, &user, &prefix, &tool, &libraryID, &outcome, &latency, &ip, &bytes, &truncated, &session, &requestID,
			&clientName, &clientVersion, &preview, &results, &cacheHit, &code, &mode, &summary); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		items = append(items, map[string]any{
			"id": id, "traceSummary": summary,
			"occurredAt": at, "userId": user, "apiKeyPrefix": prefix, "tool": tool, "libraryId": libraryID,
			"outcome": outcome, "durationMs": latency, "clientIp": ip, "responseBytes": bytes, "truncated": truncated == 1,
			"sessionId": session, "requestId": requestID, "client": strings.TrimSpace(clientName + " " + clientVersion),
			"arguments": preview, "resultCount": results, "cacheHit": cacheHit == 1, "errorCode": code, "retrievalMode": mode,
		})
	}
	if query.Get("format") == "csv" {
		p, _ := auth.FromContext(r.Context())
		a.audit(r, p, "mcp_calls.export", "mcp_calls", window, "success", map[string]any{"rows": len(items)})
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"mcp-calls-%s.csv\"", window))
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"call_id", "occurred_at", "user_id", "api_key_prefix", "client", "session_id", "request_id", "tool", "library_id", "outcome", "error_code", "retrieval_mode", "duration_ms", "response_bytes", "truncated", "result_count", "cache_hit", "client_ip", "arguments", "trace_summary"})
		for _, item := range items {
			_ = writer.Write([]string{
				item["id"].(string), item["occurredAt"].(time.Time).Format(time.RFC3339), item["userId"].(string), item["apiKeyPrefix"].(string),
				item["client"].(string), item["sessionId"].(string), item["requestId"].(string), item["tool"].(string),
				item["libraryId"].(string), item["outcome"].(string), item["errorCode"].(string), item["retrievalMode"].(string),
				fmt.Sprint(item["durationMs"]), fmt.Sprint(item["responseBytes"]), fmt.Sprint(item["truncated"]),
				fmt.Sprint(item["resultCount"]), fmt.Sprint(item["cacheHit"]), item["clientIp"].(string), item["arguments"].(string),
				item["traceSummary"].(string),
			})
		}
		writer.Flush()
		return
	}
	jsonOut(w, 200, map[string]any{"window": window, "total": total, "limit": limit, "offset": offset, "items": items})
}

// mcpCallTrace is the X-ray of one call: the stage-by-stage record of what ran,
// what each stage looked at and what it passed on, plus the neighbouring calls
// of the same session so the sequence the agent followed is visible. Reading a
// result as good or bad is only possible with both.
func (a *App) mcpCallTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := auth.FromContext(r.Context())
	// A caller reaching this through the personal route may only open their own
	// calls; the administrative route is already role-checked.
	ownOnly := strings.HasPrefix(r.URL.Path, "/api/v1/me/")
	statement := `SELECT id,occurred_at,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,result_count,cache_hit,error_code,retrieval_mode,trace_summary FROM mcp_calls WHERE id=?`
	args := []any{id}
	if ownOnly {
		statement += ` AND user_id=?`
		args = append(args, p.UserID)
	}
	var at time.Time
	var callID, user, prefix, tool, libraryID, outcome, ip, session, requestID, clientName, clientVersion, preview, code, mode, summary string
	var latency, bytes, results int64
	var truncated, cacheHit int
	err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(statement), args...).Scan(&callID, &at, &user, &prefix, &tool, &libraryID, &outcome,
		&latency, &ip, &bytes, &truncated, &session, &requestID, &clientName, &clientVersion, &preview, &results, &cacheHit, &code, &mode, &summary)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "not_found", "MCP call not found")
		return
	}
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	call := map[string]any{
		"id": callID, "occurredAt": at, "userId": user, "apiKeyPrefix": prefix, "tool": tool, "libraryId": libraryID,
		"outcome": outcome, "durationMs": latency, "clientIp": ip, "responseBytes": bytes, "truncated": truncated == 1,
		"sessionId": session, "requestId": requestID, "client": strings.TrimSpace(clientName + " " + clientVersion),
		"arguments": preview, "resultCount": results, "cacheHit": cacheHit == 1, "errorCode": code,
		"retrievalMode": mode, "traceSummary": summary,
	}
	steps := []map[string]any{}
	stepRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT sequence,stage,target,status,detail,candidates,results,duration_ms,offset_ms FROM mcp_call_steps WHERE call_id=? ORDER BY sequence`), callID)
	if err == nil {
		for stepRows.Next() {
			var sequence, candidates, stepResults, duration, offset int64
			var stage, target, status, detail string
			if stepRows.Scan(&sequence, &stage, &target, &status, &detail, &candidates, &stepResults, &duration, &offset) == nil {
				steps = append(steps, map[string]any{"sequence": sequence, "stage": stage, "target": target, "status": status,
					"detail": detail, "candidates": candidates, "results": stepResults, "durationMs": duration, "offsetMs": offset})
			}
		}
		stepRows.Close()
	}
	// Stages do not cover the whole call: connection handling, formatting and the
	// response budget run outside them. Reporting the remainder keeps the
	// waterfall honest instead of implying the stages add up to the total.
	var traced int64
	for _, step := range steps {
		traced += step["durationMs"].(int64)
	}
	call["tracedMs"], call["untracedMs"] = traced, max(latency-traced, 0)

	sequence := []map[string]any{}
	if session != "" {
		sequenceStatement := `SELECT id,occurred_at,tool,outcome,result_count,duration_ms,error_code,retrieval_mode,arguments_preview FROM mcp_calls WHERE session_id=?`
		sequenceArgs := []any{session}
		if ownOnly {
			sequenceStatement += ` AND user_id=?`
			sequenceArgs = append(sequenceArgs, p.UserID)
		}
		sequenceStatement += ` ORDER BY occurred_at LIMIT 200`
		sequenceRows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(sequenceStatement), sequenceArgs...)
		if err == nil {
			for sequenceRows.Next() {
				var rowID, rowTool, rowOutcome, rowCode, rowMode, rowArgs string
				var rowAt time.Time
				var rowResults, rowDuration int64
				if sequenceRows.Scan(&rowID, &rowAt, &rowTool, &rowOutcome, &rowResults, &rowDuration, &rowCode, &rowMode, &rowArgs) == nil {
					sequence = append(sequence, map[string]any{"id": rowID, "occurredAt": rowAt, "tool": rowTool, "outcome": rowOutcome,
						"resultCount": rowResults, "durationMs": rowDuration, "errorCode": rowCode, "retrievalMode": rowMode,
						"arguments": rowArgs, "current": rowID == callID})
				}
			}
			sequenceRows.Close()
		}
	}
	jsonOut(w, 200, map[string]any{"call": call, "steps": steps, "sessionSequence": sequence})
}

// mcpSessions groups the calls of one agent conversation. A single call rarely
// tells the whole story: an agent that searched, got nothing, searched again
// with different words and gave up looks fine call by call and is a failure as
// a conversation. Sessions that ended without an answer are ranked first
// because they are the ones worth reading.
func (a *App) mcpSessions(w http.ResponseWriter, r *http.Request) {
	duration, window, _ := mcpWindow(r.URL.Query().Get("window"))
	from := time.Now().UTC().Add(-duration)
	rows, err := a.store.DB.QueryContext(r.Context(), a.store.Rebind(`SELECT session_id,
MIN(occurred_at), MAX(occurred_at), COUNT(*),
COALESCE(SUM(CASE WHEN outcome='success' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='empty' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN outcome='error' THEN 1 ELSE 0 END),0),
COALESCE(SUM(duration_ms),0), COALESCE(SUM(response_bytes),0),
MIN(user_id), MIN(client_name), MIN(client_version)
FROM mcp_calls WHERE occurred_at>=? AND session_id<>'' GROUP BY session_id ORDER BY MAX(occurred_at) DESC LIMIT 100`), from)
	if err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	type sessionRow struct {
		id, user, client                  string
		first, last                       time.Time
		calls, success, empty, errorCount int64
		durationMS, bytes                 int64
	}
	var sessions []sessionRow
	for rows.Next() {
		var item sessionRow
		var clientName, clientVersion string
		// MIN and MAX over a timestamp lose the column type that lets the SQLite
		// driver hand back a time.Time, so both are scanned loosely and converted.
		var first, last any
		if err = rows.Scan(&item.id, &first, &last, &item.calls, &item.success, &item.empty, &item.errorCount,
			&item.durationMS, &item.bytes, &item.user, &clientName, &clientVersion); err != nil {
			problem(w, 500, "internal_error", err.Error())
			return
		}
		item.first, item.last = scanTime(first), scanTime(last)
		item.client = strings.TrimSpace(clientName + " " + clientVersion)
		sessions = append(sessions, item)
	}
	if err = rows.Err(); err != nil {
		problem(w, 500, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, item := range sessions {
		// The tool chain is what the conversation actually did, and the outcome of
		// the last call is how it ended.
		chainRows, chainErr := a.store.DB.QueryContext(r.Context(), a.store.Rebind(
			`SELECT id,tool,outcome FROM mcp_calls WHERE session_id=? AND occurred_at>=? ORDER BY occurred_at LIMIT 50`), item.id, from)
		chain := []string{}
		lastOutcome, lastCallID := "", ""
		if chainErr == nil {
			for chainRows.Next() {
				var callID, tool, outcome string
				if chainRows.Scan(&callID, &tool, &outcome) == nil {
					chain = append(chain, tool)
					lastOutcome, lastCallID = outcome, callID
				}
			}
			chainRows.Close()
		}
		out = append(out, map[string]any{
			"sessionId": item.id, "userId": item.user, "client": item.client,
			"firstCallAt": item.first, "lastCallAt": item.last, "calls": item.calls,
			"success": item.success, "empty": item.empty, "errors": item.errorCount,
			"durationMs": item.durationMS, "responseBytes": item.bytes,
			"toolChain": chain, "lastOutcome": lastOutcome, "lastCallId": lastCallID,
			// A conversation that ended on an empty or failed call is one where the
			// agent walked away without an answer.
			"unresolved": lastOutcome == "empty" || lastOutcome == "error",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i]["unresolved"].(bool), out[j]["unresolved"].(bool)
		if left != right {
			return left
		}
		return out[i]["lastCallAt"].(time.Time).After(out[j]["lastCallAt"].(time.Time))
	})
	jsonOut(w, 200, map[string]any{"window": window, "sessions": out})
}

// mcpSelfCheck runs the retrieval path end to end as the calling administrator
// and returns the same stage trace an MCP call would produce.
//
// "설정은 저장됐는데 검색이 되는가"는 설정 화면에서 답할 수 없던 질문이었습니다.
// This runs the real service with the caller's own principals, so it proves the
// ACL mapping, the index, the source connectors and the embedding path together
// rather than testing each one in isolation.
func (a *App) mcpSelfCheck(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	principals := searchPrincipals(p)
	var in struct {
		Query   string `json:"query"`
		Pattern string `json:"pattern"`
	}
	_ = decode(r, &in)
	if strings.TrimSpace(in.Query) == "" {
		in.Query = "README"
	}
	if strings.TrimSpace(in.Pattern) == "" {
		in.Pattern = "README*"
	}
	started := time.Now()
	type check struct {
		Name    string            `json:"name"`
		Status  string            `json:"status"`
		Detail  string            `json:"detail"`
		Action  string            `json:"action"`
		Steps   []calltrace.Step  `json:"steps"`
		Elapsed int64             `json:"durationMs"`
		Extra   map[string]string `json:"extra,omitempty"`
	}
	checks := []check{}
	// A check runs one retrieval call under its own recorder, so the caller sees
	// the same stage breakdown the MCP X-ray shows.
	run := func(name string, fn func(ctx context.Context) (int, error)) {
		ctx, recorder := calltrace.New(r.Context())
		at := time.Now()
		found, err := fn(ctx)
		item := check{Name: name, Steps: recorder.Steps(), Elapsed: time.Since(at).Milliseconds()}
		switch {
		case err != nil:
			item.Status, item.Detail = "fail", err.Error()
			item.Action = "오류 메시지의 단계를 X-ray에서 확인하고 해당 연동 설정을 점검하세요."
		case found == 0:
			item.Status = "warn"
			item.Detail = "결과가 없습니다."
			if summary := recorder.Summary(); summary != "" {
				item.Detail += " " + summary
			}
			item.Action = "색인 진단과 저장소 ACL을 확인하세요. 단계별 후보 수가 0이면 권한, 후보는 있는데 통과가 0이면 색인 내용 문제입니다."
		default:
			item.Status, item.Detail = "ok", fmt.Sprintf("%d건", found)
		}
		checks = append(checks, item)
	}

	var accessible int
	if err := a.store.DB.QueryRowContext(r.Context(), a.store.Rebind(`SELECT COUNT(*) FROM repositories WHERE enabled=1`)).Scan(&accessible); err == nil {
		status, detail, action := "ok", fmt.Sprintf("등록 저장소 %d개", accessible), ""
		if accessible == 0 {
			status, detail = "fail", "등록된 저장소가 없습니다."
			action = "소스·색인 화면에서 저장소를 탐색·등록하세요."
		}
		checks = append(checks, check{Name: "저장소 카탈로그", Status: status, Detail: detail, Action: action})
	}
	if len(principals) == 0 {
		checks = append(checks, check{Name: "ACL 주체", Status: "fail",
			Detail: "이 계정에 매핑된 소스 주체가 없습니다.",
			Action: "Keycloak 매핑에서 bitbucket_user_slug 또는 gitlab_user_id 클레임을 연결하세요."})
	} else {
		scope := strings.Join(principals, ", ")
		if search.Unrestricted(principals) {
			scope = "관리자 역할 - ACL 우회"
		}
		checks = append(checks, check{Name: "ACL 주체", Status: "ok", Detail: scope})
	}

	run("코드 검색 (search-code)", func(ctx context.Context) (int, error) {
		result, err := a.search.SearchCode(ctx, principals, in.Query, "", "", "", "", 5)
		return len(result.Repositories) + len(result.Hits), err
	})
	run("파일명 검색 (find-file)", func(ctx context.Context) (int, error) {
		result, err := a.search.FindFiles(ctx, principals, in.Pattern, "", "", "", "", "", 5)
		return len(result.Files), err
	})
	run("의미 검색 (search-semantic)", func(ctx context.Context) (int, error) {
		result, err := a.search.SemanticSearch(ctx, principals, in.Query, "", "", 5)
		return len(result.Hits), err
	})

	verdict := "ok"
	for _, item := range checks {
		if item.Status == "fail" {
			verdict = "fail"
			break
		}
		if item.Status == "warn" {
			verdict = "warn"
		}
	}
	a.audit(r, p, "mcp.selfcheck", "mcp", verdict, "success", map[string]any{"query": in.Query})
	jsonOut(w, 200, map[string]any{"verdict": verdict, "durationMs": time.Since(started).Milliseconds(),
		"query": in.Query, "pattern": in.Pattern, "checks": checks})
}

// scanTime converts a loosely scanned timestamp. An aggregate such as
// MIN(occurred_at) comes back as a string from SQLite and as a time.Time from
// PostgreSQL, and the layouts differ between drivers, so every form the two
// produce is accepted rather than failing the whole query on one of them.
func scanTime(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC()
	case []byte:
		return parseTimestamp(string(typed))
	case string:
		return parseTimestamp(typed)
	default:
		return time.Time{}
	}
}

func parseTimestamp(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

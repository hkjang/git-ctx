package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/search"
	"git-ctx/internal/store"
	"git-ctx/internal/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type Server struct {
	search   *search.Service
	store    *store.Store
	mu       sync.Mutex
	sessions map[string]*session
	cacheMu  sync.Mutex
	cache    map[string]cacheEntry
}
type session struct {
	expires time.Time
	done    chan struct{}
}
type cacheEntry struct {
	text    string
	expires time.Time
}

func New(s *search.Service, db *store.Store) *Server {
	return &Server{search: s, store: db, sessions: map[string]*session{}, cache: map[string]cacheEntry{}}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if version := r.Header.Get("MCP-Protocol-Version"); version != "" && version != "2025-06-18" && version != "2024-11-05" {
		http.Error(w, "unsupported MCP protocol version", http.StatusBadRequest)
		return
	}
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID != "" && !s.validSession(sessionID) {
		http.Error(w, "MCP session not found", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		if sessionID == "" {
			http.Error(w, "Mcp-Session-Id is required for an SSE stream", http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "Accept must include text/event-stream", http.StatusNotAcceptable)
			return
		}
		done, ok := s.sessionDone(sessionID)
		if !ok {
			http.Error(w, "MCP session not found", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming is unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(": git-ctx stream ready\n\n"))
		flusher.Flush()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
	if r.Method == http.MethodDelete {
		if sessionID != "" {
			s.closeSession(sessionID)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		write(w, response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "Parse error"}})
		return
	}
	if req.JSONRPC != "2.0" {
		write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "Invalid Request"}})
		return
	}
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		protocol := "2025-06-18"
		if params.ProtocolVersion == "2024-11-05" {
			protocol = params.ProtocolVersion
		}
		sessionID = s.newSession()
		w.Header().Set("Mcp-Session-Id", sessionID)
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocol, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]any{"name": "git-ctx", "version": version.Version}}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		available := Catalog()
		enabled := available[:0]
		for _, tool := range available {
			name, _ := tool["name"].(string)
			if s.toolEnabled(r.Context(), name) {
				enabled = append(enabled, tool)
			}
		}
		available = enabled
		if p, ok := auth.FromContext(r.Context()); ok && p.KeyID != "" {
			filtered := available[:0]
			for _, tool := range available {
				name, _ := tool["name"].(string)
				if contains(p.Scopes, name) {
					filtered = append(filtered, tool)
				}
			}
			available = filtered
		}
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": available}})
	case "tools/call":
		s.call(w, r, req)
	default:
		write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}})
	}
}
func (s *Server) newSession() string {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	id := hex.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, current := range s.sessions {
		if now.After(current.expires) {
			close(current.done)
			delete(s.sessions, key)
		}
	}
	s.sessions[id] = &session{expires: now.Add(30 * time.Minute), done: make(chan struct{})}
	return id
}
func (s *Server) validSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(current.expires) {
		close(current.done)
		delete(s.sessions, id)
		return false
	}
	current.expires = time.Now().Add(30 * time.Minute)
	return true
}

func (s *Server) sessionDone(id string) (<-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[id]
	if !ok || time.Now().After(current.expires) {
		if ok {
			close(current.done)
			delete(s.sessions, id)
		}
		return nil, false
	}
	current.expires = time.Now().Add(30 * time.Minute)
	return current.done, true
}

func (s *Server) closeSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.sessions[id]; ok {
		close(current.done)
		delete(s.sessions, id)
	}
}

func Catalog() []map[string]any {
	return []map[string]any{
		{"name": "resolve-library-id", "description": "Resolves a repository or library name to a Context7-compatible library ID.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryName", "query"}, "properties": map[string]any{
				"libraryName": map[string]string{"type": "string", "description": "Library or repository name"},
				"query":       map[string]string{"type": "string", "description": "Task context used to rank candidates"}}}},
		{"name": "query-docs", "description": "Searches versioned documentation and code examples for a library ID.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "query"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string", "description": "Context7-compatible /organization/project[/version] ID"},
				"query":     map[string]string{"type": "string", "description": "Focused documentation question"}}}},
	}
}

func (s *Server) call(w http.ResponseWriter, r *http.Request, req request) {
	start := time.Now()
	p, _ := auth.FromContext(r.Context())
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "Invalid params"}})
		return
	}
	if params.Name != "resolve-library-id" && params.Name != "query-docs" {
		write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "Unknown tool"}})
		return
	}
	ctx, span := otel.Tracer("git-ctx/mcp").Start(r.Context(), "mcp."+params.Name,
		oteltrace.WithAttributes(attribute.String("mcp.tool.name", params.Name)))
	defer span.End()
	r = r.WithContext(ctx)
	if !s.toolEnabled(r.Context(), params.Name) {
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"content": []map[string]string{{"type": "text", "text": "This MCP tool is disabled by the platform administrator."}}, "isError": true}})
		return
	}
	timeout := s.toolTimeout(r.Context(), params.Name)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r = r.WithContext(ctx)
	text := ""
	var err error
	libraryID := ""
	if params.Name == "query-docs" {
		libraryID = stringArg(params.Arguments, "libraryId")
	}
	if p.KeyID != "" && !contains(p.Scopes, params.Name) {
		err = errors.New("this API key is not allowed to call the requested tool")
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "MCP authorization failed")
		s.finishCall(w, r, req, p, params.Name, libraryID, start, "", err)
		return
	}
	cacheKey := s.cacheKey(p, params.Name, params.Arguments)
	if cached, ok := s.cached(cacheKey); ok {
		span.SetAttributes(attribute.Bool("git_ctx.cache.hit", true))
		s.finishCall(w, r, req, p, params.Name, libraryID, start, cached, nil)
		return
	}
	span.SetAttributes(attribute.Bool("git_ctx.cache.hit", false))
	switch params.Name {
	case "resolve-library-id":
		var items []search.Library
		items, err = s.search.Resolve(r.Context(), principalACLs(p), stringArg(params.Arguments, "libraryName"), stringArg(params.Arguments, "query"))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				items = filterLibraries(items, p.AllowedRepositories)
			}
			text = formatLibraries(items)
		}
	case "query-docs":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			text, err = s.search.Query(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "query"))
		}
	}
	if err == nil {
		s.storeCache(r.Context(), params.Name, cacheKey, text)
	} else {
		span.RecordError(err)
		span.SetStatus(codes.Error, "MCP tool call failed")
	}
	s.finishCall(w, r, req, p, params.Name, libraryID, start, text, err)
}
func (s *Server) toolEnabled(ctx context.Context, name string) bool {
	var enabled int
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT enabled FROM mcp_tools WHERE name=?`), name).Scan(&enabled); err != nil {
		return true
	}
	return enabled == 1
}
func (s *Server) toolTimeout(ctx context.Context, name string) time.Duration {
	var timeout int
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT timeout_ms FROM mcp_tools WHERE name=?`), name).Scan(&timeout); err != nil || timeout < 100 {
		return 30 * time.Second
	}
	return time.Duration(timeout) * time.Millisecond
}
func (s *Server) cacheKey(p auth.Principal, tool string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	return tool + "|" + p.ACLPrincipal + "|" + strings.Join(p.AllowedRepositories, ",") + "|" + string(raw)
}
func (s *Server) cached(key string) (string, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, ok := s.cache[key]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expires) {
		delete(s.cache, key)
		return "", false
	}
	return entry.text, true
}
func (s *Server) storeCache(ctx context.Context, tool, key, text string) {
	var seconds int
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT cache_seconds FROM mcp_tools WHERE name=?`), tool).Scan(&seconds); err != nil || seconds <= 0 {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if len(s.cache) > 10000 {
		now := time.Now()
		for item, entry := range s.cache {
			if now.After(entry.expires) {
				delete(s.cache, item)
			}
		}
	}
	s.cache[key] = cacheEntry{text: text, expires: time.Now().Add(time.Duration(seconds) * time.Second)}
}
func (s *Server) finishCall(w http.ResponseWriter, r *http.Request, req request, p auth.Principal, tool, libraryID string, start time.Time, text string, err error) {
	outcome := "success"
	if err != nil {
		outcome = "error"
		text = err.Error()
	}
	_, _ = s.store.DB.ExecContext(r.Context(), s.store.Rebind(`INSERT INTO mcp_calls(id,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip) VALUES(?,?,?,?,?,?,?,?)`),
		fmt.Sprintf("%d", time.Now().UnixNano()), p.UserID, p.KeyPrefix, tool, libraryID, outcome, time.Since(start).Milliseconds(), clientIP(r))
	write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": err != nil}})
}

func formatLibraries(items []search.Library) string {
	if len(items) == 0 {
		return "No accessible libraries matched. Check the name or use a broader query."
	}
	var b strings.Builder
	b.WriteString("Available Libraries:\n")
	for _, x := range items {
		fmt.Fprintf(&b, "\n- Name: %s\n- Library ID: %s\n- Description: %s\n- Code Snippets: %d\n- Source Reputation: %s\n- Versions: %s\n", x.Name, x.ID, x.Description, x.Snippets, x.Reputation, strings.Join(x.Versions, ", "))
	}
	return b.String()
}
func stringArg(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func write(w http.ResponseWriter, v response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
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
func principalACLs(p auth.Principal) []string {
	if len(p.ACLPrincipals) > 0 {
		return p.ACLPrincipals
	}
	if p.ACLPrincipal != "" {
		return []string{p.ACLPrincipal}
	}
	return nil
}

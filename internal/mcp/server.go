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
)

type Server struct {
	search   *search.Service
	store    *store.Store
	mu       sync.Mutex
	sessions map[string]time.Time
	cacheMu  sync.Mutex
	cache    map[string]cacheEntry
}
type cacheEntry struct {
	text    string
	expires time.Time
}

func New(s *search.Service, db *store.Store) *Server {
	return &Server{search: s, store: db, sessions: map[string]time.Time{}, cache: map[string]cacheEntry{}}
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
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodDelete {
		if sessionID != "" {
			s.mu.Lock()
			delete(s.sessions, sessionID)
			s.mu.Unlock()
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
			"serverInfo": map[string]any{"name": "git-ctx", "version": "0.1.0"}}})
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
	for key, expires := range s.sessions {
		if now.After(expires) {
			delete(s.sessions, key)
		}
	}
	s.sessions[id] = now.Add(30 * time.Minute)
	return id
}
func (s *Server) validSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(s.sessions, id)
		return false
	}
	s.sessions[id] = time.Now().Add(30 * time.Minute)
	return true
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
		s.finishCall(w, r, req, p, params.Name, libraryID, start, "", err)
		return
	}
	cacheKey := s.cacheKey(p, params.Name, params.Arguments)
	if cached, ok := s.cached(cacheKey); ok {
		s.finishCall(w, r, req, p, params.Name, libraryID, start, cached, nil)
		return
	}
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

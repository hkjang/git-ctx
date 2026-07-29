package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"git-ctx/internal/auth"
	"git-ctx/internal/calltrace"
	"git-ctx/internal/contentsecurity"
	"git-ctx/internal/search"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type Server struct {
	search *search.Service
	store  *store.Store
	// health reports the state of the source connectors, so an administrator
	// asking an agent for platform status sees a paused integration.
	health          func() []source.BreakerState
	embeddingHealth func(context.Context) string
	strictMode      func(context.Context) bool
	mu              sync.Mutex
	sessions        map[string]*session
	cacheMu         sync.Mutex
	cache           map[string]cacheEntry
}
type session struct {
	clientName    string
	clientVersion string
	expires       time.Time
	done          chan struct{}
}
type cacheEntry struct {
	text    string
	expires time.Time
}

func New(s *search.Service, db *store.Store) *Server {
	return &Server{search: s, store: db, sessions: map[string]*session{}, cache: map[string]cacheEntry{}}
}

func (s *Server) SetStrictCompatibilityLoader(loader func(context.Context) bool) {
	s.strictMode = loader
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
	if sessionID != "" && !s.validSession(r.Context(), sessionID) {
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
		done, ok := s.sessionDone(r.Context(), sessionID)
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
				if !s.validSession(r.Context(), sessionID) {
					return
				}
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
	if r.Method == http.MethodDelete {
		if sessionID != "" {
			s.closeSession(r.Context(), sessionID)
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
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &params)
		protocol := "2025-06-18"
		if params.ProtocolVersion == "2024-11-05" {
			protocol = params.ProtocolVersion
		}
		var err error
		// The client identity is only sent here, so it is kept on the session and
		// copied onto every call: "which agent asked this" is the first question of
		// any MCP incident review.
		sessionID, err = s.newSession(r.Context(), params.ClientInfo.Name, params.ClientInfo.Version)
		if err != nil {
			write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32603, Message: "Unable to create MCP session"}})
			return
		}
		w.Header().Set("Mcp-Session-Id", sessionID)
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocol, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":   map[string]any{"name": "git-ctx", "version": version.Version},
			"instructions": serverInstructions}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		available := Catalog()
		enabled := available[:0]
		p, _ := auth.FromContext(r.Context())
		for _, tool := range available {
			name, _ := tool["name"].(string)
			if s.toolVisible(r.Context(), p, name) {
				enabled = append(enabled, tool)
			}
		}
		available = enabled
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": available}})
	case "tools/call":
		s.call(w, r, req)
	default:
		write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found"}})
	}
}

// serverInstructions is returned by initialize. MCP clients hand it to the
// model, so it is the one place where tool choice can be taught once instead of
// being rediscovered in every conversation.
const serverInstructions = `git-ctx searches internal Bitbucket and GitLab repositories. Every result is filtered by the caller's repository permissions.

Choosing a tool:
- Any question about source code, configuration or "where is X": start with search-code. It returns matching repositories AND file contents in one call.
- When search-code finds nothing because the wording differs from the code: search-semantic matches by meaning across repositories.
- Looking for a file by name or extension: find-file (Dockerfile, *.tf, **/migrations/*.sql).
- Need the file itself: read-file, optionally with startLine and endLine.
- Orienting in an unknown repository: list-directory, then get-repository-map.
- "Why is this like this", "when did this change": get-file-history for commits, search-merge-requests for the reasoning behind them.
- Exact identifier: find-symbol, then get-symbol-context.
- Before changing shared code: find-dependents shows every repository that uses it.
- Documentation for a known library id: query-docs.
- search-repositories returns repository names only, never file contents.

Reading the results:
- Search responses end with Notes explaining which path ran, what the ACL filtered and whether a timeout was hit. An empty result with an ACL or indexing note is not proof that the code does not exist.
- Repositories that are still indexing are answered live from the source code search API and the response says so.
- Snippets and files are secret-masked. Cite the Source line, which points at the exact ref and lines.
- Answers are bounded by a byte budget. A Truncated section states how many results were withheld; narrow the call (libraryId, path, limit, read-file line range) rather than repeating it. Pass maxBytes to ask for a smaller answer.`

func (s *Server) newSession(ctx context.Context, clientName, clientVersion string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	now := time.Now().UTC()
	expires := now.Add(30 * time.Minute)
	if _, err := s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO mcp_sessions(id_hash,expires_at,last_seen_at,client_name,client_version) VALUES(?,?,?,?,?)`),
		mcpSessionHash(id), expires, now, clip(clientName, 80), clip(clientVersion, 40)); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, current := range s.sessions {
		if now.After(current.expires) {
			close(current.done)
			delete(s.sessions, key)
		}
	}
	s.sessions[id] = &session{expires: expires, done: make(chan struct{}), clientName: clip(clientName, 80), clientVersion: clip(clientVersion, 40)}
	return id, nil
}
func (s *Server) validSession(ctx context.Context, id string) bool {
	now := time.Now().UTC()
	expires := now.Add(30 * time.Minute)
	result, err := s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE mcp_sessions SET expires_at=?,last_seen_at=? WHERE id_hash=? AND expires_at>?`), expires, now, mcpSessionHash(id), now)
	if err != nil {
		return false
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return false
	}
	s.mu.Lock()
	if current, ok := s.sessions[id]; ok {
		current.expires = expires
	}
	s.mu.Unlock()
	return true
}

func (s *Server) sessionDone(ctx context.Context, id string) (<-chan struct{}, bool) {
	if !s.validSession(ctx, id) {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[id]
	if !ok {
		current = &session{expires: time.Now().UTC().Add(30 * time.Minute), done: make(chan struct{})}
		s.sessions[id] = current
	}
	return current.done, true
}

func (s *Server) closeSession(ctx context.Context, id string) {
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM mcp_sessions WHERE id_hash=?`), mcpSessionHash(id))
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.sessions[id]; ok {
		close(current.done)
		delete(s.sessions, id)
	}
}

func mcpSessionHash(id string) []byte {
	sum := sha256.Sum256([]byte(id))
	return sum[:]
}

// Catalog is the tool list advertised to MCP clients. Every tool accepts
// maxBytes, so an agent that only wants a short answer can say so instead of
// spending a full page of its context window on one call.
func Catalog() []map[string]any {
	tools := catalog()
	for _, tool := range tools {
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			continue
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		properties["maxBytes"] = map[string]any{
			"type": "integer", "minimum": MinResponseBytes, "maximum": MaxResponseBytes,
			"description": "Optional smaller response budget in bytes for this call. It can only lower the configured budget, never raise it.",
		}
	}
	return tools
}

func catalog() []map[string]any {
	return []map[string]any{
		{"name": "resolve-library-id", "description": "Resolves a repository or library name to a Context7-compatible library ID.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryName", "query"}, "properties": map[string]any{
				"libraryName": map[string]string{"type": "string", "description": "Library or repository name"},
				"query":       map[string]string{"type": "string", "description": "Task context used to rank candidates"}}}},
		{"name": "query-docs", "description": "Searches versioned documentation and code examples for a library ID.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "query"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string", "description": "Context7-compatible /organization/project[/version] ID"},
				"query":     map[string]string{"type": "string", "description": "Focused documentation question"}}}},
		{"name": "search-repositories", "description": "Lists matching Bitbucket and GitLab repositories by name or description only. This tool never returns file contents; call search-code when the user asks about code, symbols, configuration, or any file text.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query":      map[string]string{"type": "string", "description": "Project, repository, product, or description search text"},
				"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}, "description": "Optional source filter"},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}}},
		{"name": "search-source", "description": "Searches file contents through the connected Bitbucket or GitLab code search API across accessible repositories. Prefer search-code, which runs this search and repository discovery together.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query":      map[string]string{"type": "string", "description": "Code, symbol, API, or text query"},
				"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
				"project":    map[string]string{"type": "string", "description": "Optional project key or namespace"},
				"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
				"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}}},
		{"name": "search-code", "description": "Primary code search. Finds repositories AND matching file contents in one call, without a library ID, and falls back to the live Bitbucket or GitLab code search API for repositories that are not indexed yet. Use this first for any question about source code, symbols, configuration, or where something is implemented.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query":      map[string]string{"type": "string", "description": "Natural-language request, repository name, code symbol, API, or text query"},
				"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
				"project":    map[string]string{"type": "string", "description": "Optional project key or namespace"},
				"repository": map[string]string{"type": "string", "description": "Optional repository slug or /library/id to search inside one repository"},
				"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}}},
		{"name": "find-file", "description": "Finds files by name or path across accessible repositories. Use it for questions like where a Dockerfile, migration, config or module lives. Supports plain names (README), globs (*.tf, auth*.py) and path patterns (**/migrations/*.sql, internal/**/service.go).",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"pattern"}, "properties": map[string]any{
				"pattern":    map[string]string{"type": "string", "description": "File name, glob or path pattern. Without a slash it matches the file name, with a slash the whole path."},
				"libraryId":  map[string]string{"type": "string", "description": "Optional Context7 library ID scope"},
				"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
				"project":    map[string]string{"type": "string", "description": "Optional project key or namespace"},
				"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
				"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}}},
		{"name": "read-file", "description": "Reads one file from a repository, optionally a line range. Use it after find-file or search-code to see the whole file instead of a snippet. Files without indexed content are read live from the source server; credentials are masked and long files are truncated with the range stated.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path"}, "properties": map[string]any{
				"path":       map[string]string{"type": "string", "description": "Repository-relative file path, for example charts/values.yaml"},
				"libraryId":  map[string]string{"type": "string", "description": "Library ID; required when the path exists in more than one repository"},
				"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
				"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
				"startLine":  map[string]any{"type": "integer", "minimum": 1, "description": "Optional first line, 1-based"},
				"endLine":    map[string]any{"type": "integer", "minimum": 1, "description": "Optional last line, inclusive"}}}},
		{"name": "search-semantic", "description": "Finds code and documentation across accessible repositories without requiring a library ID. In hybrid mode it searches by meaning; when embeddings are disabled or unavailable it transparently uses ACL-safe keyword and Bitbucket/GitLab query search.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query":      map[string]string{"type": "string", "description": "Describe the behaviour or concept in natural language"},
				"libraryId":  map[string]string{"type": "string", "description": "Optional library scope"},
				"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}}},
		{"name": "find-dependents", "description": "Finds every accessible repository that imports, calls or otherwise depends on a symbol, module, table or service. Use it before changing shared code to see who breaks; trace-dependencies only covers one repository.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target"}, "properties": map[string]any{
				"target":     map[string]string{"type": "string", "description": "Imported module, called symbol, table or service name"},
				"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 300}}}},
		{"name": "search-merge-requests", "description": "Searches GitLab merge requests and Bitbucket pull requests. Use it for why-questions: the reasoning, trade-offs and rollout notes live in the request description, not in the code or the commit subject.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"query":      map[string]string{"type": "string", "description": "Text matched against title and description"},
				"libraryId":  map[string]string{"type": "string", "description": "Optional library scope"},
				"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
				"state":      map[string]any{"type": "string", "enum": []string{"all", "open", "merged", "closed"}},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}}},
		{"name": "get-file-history", "description": "Lists the commits that changed a file, newest first, with author, date and message. Use it to explain why code looks the way it does, when a behaviour changed, or who to ask.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path"}, "properties": map[string]any{
				"path":       map[string]string{"type": "string", "description": "Repository-relative file path"},
				"libraryId":  map[string]string{"type": "string", "description": "Library ID; required when the path exists in more than one repository"},
				"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
				"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}}},
		{"name": "list-directory", "description": "Lists the immediate contents of a repository directory, folders first. Use it to orient yourself before reading files.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path":       map[string]string{"type": "string", "description": "Directory path; omit or use an empty string for the repository root"},
				"libraryId":  map[string]string{"type": "string", "description": "Library ID; required when several repositories are accessible"},
				"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
				"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"}}}},
		{"name": "get-repository-map", "description": "Returns the indexed languages, directories, key files, and entry points for a repository.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string", "description": "Context7-compatible library ID"},
				"ref":       map[string]string{"type": "string", "description": "Optional branch or tag"}}}},
		{"name": "find-symbol", "description": "Finds functions, methods, classes, interfaces, and database objects in accessible repositories.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query":     map[string]string{"type": "string", "description": "Symbol name or signature"},
				"libraryId": map[string]string{"type": "string", "description": "Optional library scope"},
				"ref":       map[string]string{"type": "string", "description": "Optional branch or tag"},
				"kind":      map[string]string{"type": "string", "description": "Optional symbol kind"},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}}},
		{"name": "get-symbol-context", "description": "Returns an indexed symbol definition, documentation, source context, and location.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "symbol"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string"},
				"symbol":    map[string]string{"type": "string", "description": "Exact or partial qualified symbol name"},
				"ref":       map[string]string{"type": "string"}}}},
		{"name": "trace-dependencies", "description": "Traces imports, calls, and data dependencies for a symbol or module.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "symbol"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string"},
				"symbol":    map[string]string{"type": "string"},
				"ref":       map[string]string{"type": "string"},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}}},
		{"name": "compare-refs", "description": "Compares indexed symbols between two branches or tags.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "baseRef", "headRef"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string"},
				"baseRef":   map[string]string{"type": "string"},
				"headRef":   map[string]string{"type": "string"}}}},
		{"name": "get-change-impact", "description": "Combines ref differences with incoming source dependencies to identify affected code.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "baseRef", "headRef"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string"},
				"baseRef":   map[string]string{"type": "string"},
				"headRef":   map[string]string{"type": "string"},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}}},
		{"name": "get-context-pack", "description": "Searches a curated multi-repository context pack while enforcing ACL per repository.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"pack", "query"}, "properties": map[string]any{
				"pack":  map[string]string{"type": "string", "description": "Context pack slug"},
				"query": map[string]string{"type": "string"}}}},
		{"name": "find-runbook", "description": "Finds operational runbooks and playbooks in accessible indexed repositories.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query":     map[string]string{"type": "string"},
				"libraryId": map[string]string{"type": "string"},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}}},
		{"name": "export-context", "description": "Exports ACL-filtered repository context as a bounded Markdown bundle with an untrusted-data safety label.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryIds", "query"}, "properties": map[string]any{
				"libraryIds": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": map[string]string{"type": "string"}},
				"query":      map[string]string{"type": "string"}}}},
		{"name": "explain-search-result", "description": "Explains keyword matches, retrieval mode, source lines, and embedding metadata for accessible search candidates.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "query"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string"},
				"query":     map[string]string{"type": "string"},
				"ref":       map[string]string{"type": "string"},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}}},
		{"name": "get-platform-status", "description": "Returns administrative MCP, source, index, database, and effective embedding retrieval status. Requires an administrator MCP API key.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}},
		{"name": "list-index-jobs", "description": "Lists recent indexing jobs for source administrators and operators using an MCP API key.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"status": map[string]any{"type": "string", "enum": []string{"pending", "running", "completed", "failed"}},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}}},
		{"name": "reindex-repository", "description": "Queues an idempotent repository reindex job. Requires a source administrator MCP API key.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId"}, "properties": map[string]any{
				"libraryId": map[string]string{"type": "string", "description": "Repository library ID such as /project/repository"},
				"ref":       map[string]string{"type": "string", "description": "Optional branch or tag; defaults to the repository default branch"}}}},
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
	if !catalogContains(params.Name) {
		write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "Unknown tool"}})
		return
	}
	ctx, span := otel.Tracer("git-ctx/mcp").Start(r.Context(), "mcp."+params.Name,
		oteltrace.WithAttributes(attribute.String("mcp.tool.name", params.Name)))
	defer span.End()
	r = r.WithContext(ctx)
	if !s.toolVisible(r.Context(), p, params.Name) {
		write(w, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"content": []map[string]string{{"type": "text", "text": "This MCP tool is unavailable for this credential."}}, "isError": true}})
		return
	}
	timeout := s.toolTimeout(r.Context(), params.Name)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	// Every stage of this call records itself against the recorder, so a later
	// reader can see where the results appeared or disappeared.
	ctx, recorder := calltrace.New(ctx)
	r = r.WithContext(ctx)
	text := ""
	var err error
	// empty marks a successful call that returned nothing, so the usage view can
	// separate "the tool failed" from "the catalog had no answer".
	empty := false
	libraryID := ""
	if params.Name == "query-docs" || params.Name == "reindex-repository" || params.Name == "get-repository-map" || params.Name == "get-symbol-context" || params.Name == "trace-dependencies" || params.Name == "compare-refs" || params.Name == "get-change-impact" || params.Name == "explain-search-result" {
		libraryID = stringArg(params.Arguments, "libraryId")
	}
	// The budget is resolved before the tool runs, because the call context is
	// already cancelled by the time a timed-out answer is written.
	budget := s.responseBudget(r.Context(), params.Name)
	// A caller may lower the budget for one call — an agent that only needs a
	// reminder should not have to spend a full page of context on it — but never
	// raise it past what the operator configured.
	if requested := intArg(params.Arguments, "maxBytes", 0); requested >= MinResponseBytes && requested < budget {
		budget = requested
	}
	audit := s.auditContext(w, r, params.Name, params.Arguments)
	audit.trace = recorder
	cacheKey := s.cacheKey(r.Context(), p, params.Name, params.Arguments)
	if cached, ok := s.cached(cacheKey); ok {
		span.SetAttributes(attribute.Bool("git_ctx.cache.hit", true))
		audit.cacheHit = true
		recorder.Note("cache", params.Name, calltrace.StatusOK, "answered from the tool cache without running the search")
		s.finishCall(w, r, req, p, params.Name, libraryID, start, cached, nil, false, budget, audit)
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
			empty = len(items) == 0
			text = formatLibraries(items)
		}
	case "query-docs":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			text, err = s.search.Query(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "query"))
		}
	case "search-repositories":
		var items []search.RepositoryResult
		items, err = s.search.SearchRepositories(r.Context(), principalACLs(p), stringArg(params.Arguments, "query"), stringArg(params.Arguments, "sourceType"), intArg(params.Arguments, "limit", 20))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				filtered := items[:0]
				for _, item := range items {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						filtered = append(filtered, item)
					}
				}
				items = filtered
			}
			empty = len(items) == 0
			text = formatRepositorySearch(items, stringArg(params.Arguments, "query"))
		}
	case "search-source":
		var hits []search.SourceResult
		hits, err = s.search.SearchSource(r.Context(), principalACLs(p), stringArg(params.Arguments, "query"), stringArg(params.Arguments, "sourceType"), stringArg(params.Arguments, "project"), stringArg(params.Arguments, "repository"), stringArg(params.Arguments, "ref"), intArg(params.Arguments, "limit", 20))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				filtered := hits[:0]
				for _, hit := range hits {
					if libraryAllowed(hit.LibraryID, p.AllowedRepositories) {
						filtered = append(filtered, hit)
					}
				}
				hits = filtered
			}
			empty = len(hits) == 0
			text = formatSourceResults(hits)
		}
	case "search-code":
		var result search.CodeSearchResult
		result, err = s.search.SearchCode(r.Context(), principalACLs(p), stringArg(params.Arguments, "query"), stringArg(params.Arguments, "sourceType"), stringArg(params.Arguments, "project"), stringArg(params.Arguments, "repository"), stringArg(params.Arguments, "ref"), intArg(params.Arguments, "limit", 20))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				repositories := result.Repositories[:0]
				for _, item := range result.Repositories {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						repositories = append(repositories, item)
					}
				}
				result.Repositories = repositories
				hits := result.Hits[:0]
				for _, hit := range result.Hits {
					if libraryAllowed(hit.LibraryID, p.AllowedRepositories) {
						hits = append(hits, hit)
					}
				}
				result.Hits = hits
			}
			empty = len(result.Repositories) == 0 && len(result.Hits) == 0
			text = formatCodeSearch(result)
		}
	case "find-file":
		var files search.FileSearchResult
		files, err = s.search.FindFiles(r.Context(), principalACLs(p), stringArg(params.Arguments, "pattern"), stringArg(params.Arguments, "libraryId"),
			stringArg(params.Arguments, "sourceType"), stringArg(params.Arguments, "project"), stringArg(params.Arguments, "repository"),
			stringArg(params.Arguments, "ref"), intArg(params.Arguments, "limit", 50))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				allowed := files.Files[:0]
				for _, item := range files.Files {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						allowed = append(allowed, item)
					}
				}
				files.Files = allowed
			}
			empty = len(files.Files) == 0
			text = formatFileResults(files)
		}
	case "read-file":
		var file search.FileContent
		file, err = s.search.ReadFile(r.Context(), principalACLs(p), stringArg(params.Arguments, "libraryId"), stringArg(params.Arguments, "repository"),
			stringArg(params.Arguments, "path"), stringArg(params.Arguments, "ref"), intArg(params.Arguments, "startLine", 0), intArg(params.Arguments, "endLine", 0))
		if err == nil && p.KeyID != "" && !libraryAllowed(file.LibraryID, p.AllowedRepositories) {
			err = errors.New("file is unavailable or access is denied")
		}
		if err == nil {
			text = formatFileContent(file)
		}
	case "search-semantic":
		var semantic search.SemanticSearch
		semantic, err = s.search.SemanticSearch(r.Context(), principalACLs(p), stringArg(params.Arguments, "query"),
			stringArg(params.Arguments, "libraryId"), stringArg(params.Arguments, "sourceType"), intArg(params.Arguments, "limit", 10))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				allowed := semantic.Hits[:0]
				for _, item := range semantic.Hits {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						allowed = append(allowed, item)
					}
				}
				semantic.Hits = allowed
			}
			empty = len(semantic.Hits) == 0
			text = formatSemanticSearch(semantic)
		}
	case "find-dependents":
		var dependents search.DependentSearch
		dependents, err = s.search.FindDependents(r.Context(), principalACLs(p), stringArg(params.Arguments, "target"),
			stringArg(params.Arguments, "sourceType"), intArg(params.Arguments, "limit", 100))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				allowed := dependents.Dependents[:0]
				for _, item := range dependents.Dependents {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						allowed = append(allowed, item)
					}
				}
				dependents.Dependents = allowed
			}
			empty = len(dependents.Dependents) == 0
			text = formatDependents(dependents)
		}
	case "search-merge-requests":
		var requests search.ChangeRequestSearch
		requests, err = s.search.SearchChangeRequests(r.Context(), principalACLs(p), stringArg(params.Arguments, "query"),
			stringArg(params.Arguments, "libraryId"), stringArg(params.Arguments, "repository"), stringArg(params.Arguments, "state"),
			intArg(params.Arguments, "limit", 20))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				allowed := requests.Requests[:0]
				for _, item := range requests.Requests {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						allowed = append(allowed, item)
					}
				}
				requests.Requests = allowed
			}
			empty = len(requests.Requests) == 0
			text = formatChangeRequests(requests)
		}
	case "get-file-history":
		var history search.FileHistory
		history, err = s.search.FileHistory(r.Context(), principalACLs(p), stringArg(params.Arguments, "libraryId"), stringArg(params.Arguments, "repository"),
			stringArg(params.Arguments, "path"), stringArg(params.Arguments, "ref"), intArg(params.Arguments, "limit", 20))
		if err == nil && p.KeyID != "" && !libraryAllowed(history.LibraryID, p.AllowedRepositories) {
			err = errors.New("file is unavailable or access is denied")
		}
		if err == nil {
			empty = len(history.Commits) == 0
			text = formatFileHistory(history)
		}
	case "list-directory":
		var listing search.DirectoryListing
		listing, err = s.search.ListDirectory(r.Context(), principalACLs(p), stringArg(params.Arguments, "libraryId"), stringArg(params.Arguments, "repository"),
			stringArg(params.Arguments, "path"), stringArg(params.Arguments, "ref"))
		if err == nil && p.KeyID != "" && !libraryAllowed(listing.LibraryID, p.AllowedRepositories) {
			err = errors.New("directory is unavailable or access is denied")
		}
		if err == nil {
			empty = len(listing.Entries) == 0
			text = formatDirectory(listing)
		}
	case "get-repository-map":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var item search.RepositoryMap
			item, err = s.search.RepositoryMap(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "ref"))
			if err == nil {
				text = formatRepositoryMap(item)
			}
		}
	case "find-symbol":
		libraryID = stringArg(params.Arguments, "libraryId")
		if p.KeyID != "" && libraryID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var items []search.SymbolResult
			items, err = s.search.FindSymbols(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "ref"), stringArg(params.Arguments, "query"), stringArg(params.Arguments, "kind"), intArg(params.Arguments, "limit", 20))
			if err == nil {
				if len(p.AllowedRepositories) > 0 {
					filtered := items[:0]
					for _, item := range items {
						if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
							filtered = append(filtered, item)
						}
					}
					items = filtered
				}
				text = formatSymbols(items)
			}
		}
	case "get-symbol-context":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var item search.SymbolResult
			item, err = s.search.SymbolContext(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "ref"), stringArg(params.Arguments, "symbol"))
			if err == nil {
				text = formatSymbolContext(item)
			}
		}
	case "trace-dependencies":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var items []search.DependencyResult
			items, err = s.search.TraceDependencies(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "ref"), stringArg(params.Arguments, "symbol"), intArg(params.Arguments, "limit", 50))
			if err == nil {
				text = formatDependencies(items)
			}
		}
	case "compare-refs":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var item search.RefComparison
			item, err = s.search.CompareRefs(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "baseRef"), stringArg(params.Arguments, "headRef"))
			if err == nil {
				text = formatRefComparison(item)
			}
		}
	case "get-change-impact":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var item search.ChangeImpact
			item, err = s.search.ChangeImpact(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "baseRef"), stringArg(params.Arguments, "headRef"), intArg(params.Arguments, "limit", 100))
			if err == nil {
				text = formatChangeImpact(item)
			}
		}
	case "get-context-pack":
		var item search.ContextPackResult
		item, err = s.search.ContextPack(r.Context(), principalACLs(p), stringArg(params.Arguments, "pack"), stringArg(params.Arguments, "query"))
		if err == nil {
			text = fmt.Sprintf("# Context Pack: %s\n\n%s\n\n%s", item.Name, item.Description, item.Content)
		}
	case "find-runbook":
		libraryID = stringArg(params.Arguments, "libraryId")
		if p.KeyID != "" && libraryID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var items []search.RunbookResult
			items, err = s.search.FindRunbooks(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "query"), intArg(params.Arguments, "limit", 10))
			if err == nil {
				text = formatRunbooks(items)
			}
		}
	case "export-context":
		libraries := stringSliceArg(params.Arguments, "libraryIds")
		if p.KeyID != "" {
			for _, id := range libraries {
				if !libraryAllowed(id, p.AllowedRepositories) {
					err = errors.New("context is unavailable or access is denied")
					break
				}
			}
		}
		if err == nil {
			text, err = s.search.ExportContext(r.Context(), principalACLs(p), libraries, stringArg(params.Arguments, "query"))
		}
	case "explain-search-result":
		if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
			err = errors.New("library is unavailable or access is denied")
		} else {
			var item search.SearchExplanation
			item, err = s.search.ExplainSearch(r.Context(), principalACLs(p), libraryID, stringArg(params.Arguments, "ref"), stringArg(params.Arguments, "query"), intArg(params.Arguments, "limit", 10))
			if err == nil {
				text = formatSearchExplanation(item)
			}
		}
	case "get-platform-status":
		text, err = s.platformStatus(r.Context())
	case "list-index-jobs":
		text, err = s.indexJobs(r.Context(), p, stringArg(params.Arguments, "status"), intArg(params.Arguments, "limit", 20))
	case "reindex-repository":
		text, err = s.reindexRepository(r.Context(), p, libraryID, stringArg(params.Arguments, "ref"))
	}
	if err == nil {
		if !empty {
			// An empty answer is often a transient state while indexing runs, so it
			// is never cached.
			s.storeCache(r.Context(), params.Name, cacheKey, text)
		}
	} else {
		span.RecordError(err)
		span.SetStatus(codes.Error, "MCP tool call failed")
	}
	s.finishCall(w, r, req, p, params.Name, libraryID, start, text, err, empty, budget, audit)
}

// callAudit is what an operator or a reviewer needs to reconstruct one call:
// which client and session asked, what was asked, and how it was answered.
type callAudit struct {
	sessionID     string
	requestID     string
	clientName    string
	clientVersion string
	preview       string
	hash          string
	cacheHit      bool
	trace         *calltrace.Recorder
}

// auditContext collects the identifying facts of one call. The session is
// hashed in storage, so only a short prefix of the client-visible id is kept:
// enough to group a conversation, not enough to replay it.
func (s *Server) auditContext(w http.ResponseWriter, r *http.Request, tool string, args map[string]any) callAudit {
	// The HTTP middleware stamps the request id on the response, and the trace id
	// is the identifier the distributed traces use; either one joins this row to
	// the server logs.
	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		if span := oteltrace.SpanContextFromContext(r.Context()); span.HasTraceID() {
			requestID = span.TraceID().String()
		}
	}
	audit := callAudit{requestID: requestID}
	if id := r.Header.Get("Mcp-Session-Id"); len(id) >= 12 {
		audit.sessionID = id[:12]
		s.mu.Lock()
		if current, ok := s.sessions[id]; ok {
			audit.clientName, audit.clientVersion = current.clientName, current.clientVersion
		}
		s.mu.Unlock()
		if audit.clientName == "" {
			// A session created by another replica is only in the database.
			_ = s.store.DB.QueryRowContext(r.Context(), s.store.Rebind(`SELECT client_name,client_version FROM mcp_sessions WHERE id_hash=?`), mcpSessionHash(id)).
				Scan(&audit.clientName, &audit.clientVersion)
		}
	}
	audit.preview, audit.hash = argumentDigest(tool, args)
	return audit
}

// argumentDigest renders the arguments for the audit log and hashes them for
// grouping. Values are masked and clipped: an audit trail must survive a secret
// pasted into a query, and a log line is not a place for a whole file.
func argumentDigest(tool string, args map[string]any) (string, string) {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%v", key, args[key])
	}
	masked, _ := contentsecurity.Sanitize(b.String())
	preview := clip(masked, 300)
	sum := sha256.Sum256([]byte(tool + "\x00" + b.String()))
	return preview, hex.EncodeToString(sum[:8])
}

// retrievalMode reports which path produced an answer, so the analytics screen
// can show how often the platform fell back instead of using its index.
func retrievalMode(text string) string {
	switch {
	case strings.Contains(text, "retrieval: vector database ANN"):
		return "vector"
	case strings.Contains(text, "retrieval: in-database scan"):
		return "scan"
	case strings.Contains(text, "retrieval: keyword/source-query"), strings.Contains(text, "keyword/source-query retrieval"):
		return "keyword"
	case strings.Contains(text, "answered live"), strings.Contains(text, "live source code search"):
		return "remote"
	case strings.Contains(text, "still indexing"):
		return "indexing"
	default:
		return "index"
	}
}

// errorCode classifies a failure so the analytics screen can separate "the
// source server was slow" from "the caller asked for something that is gone".
func errorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case strings.Contains(strings.ToLower(err.Error()), "not accessible"), strings.Contains(strings.ToLower(err.Error()), "permission"):
		return "forbidden"
	case strings.Contains(strings.ToLower(err.Error()), "not found"):
		return "not_found"
	default:
		return "error"
	}
}

// runeSafeCut returns the largest prefix of value that fits in limit bytes and
// still ends on a rune boundary. A plain byte slice splits multi-byte text —
// every Korean character is three bytes — and stores invalid UTF-8 that later
// renders as replacement characters.
func runeSafeCut(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func clip(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return strings.TrimSpace(runeSafeCut(value, limit)) + "…"
}

// SetHealthLoader installs the source connector health source.
func (s *Server) SetHealthLoader(loader func() []source.BreakerState) { s.health = loader }

// SetEmbeddingHealthLoader adds the effective retrieval policy and model
// circuit state to the administrator-only platform status tool.
func (s *Server) SetEmbeddingHealthLoader(loader func(context.Context) string) {
	s.embeddingHealth = loader
}

func catalogContains(name string) bool {
	for _, tool := range Catalog() {
		if tool["name"] == name {
			return true
		}
	}
	return false
}

func coreTool(name string) bool {
	return name == "resolve-library-id" || name == "query-docs"
}

func adminTool(name string) bool {
	return name == "get-platform-status" || name == "list-index-jobs" || name == "reindex-repository"
}

func adminToolAllowed(p auth.Principal, name string) bool {
	if p.KeyID == "" {
		return false
	}
	switch name {
	case "get-platform-status":
		for _, role := range []string{"platform-admin", "readonly-operator", "source-admin", "mcp-admin", "search-admin", "security-admin", "auditor"} {
			if p.HasRole(role) {
				return true
			}
		}
	case "list-index-jobs":
		return p.HasRole("source-admin") || p.HasRole("readonly-operator")
	case "reindex-repository":
		return p.HasRole("source-admin")
	}
	return false
}

func (s *Server) toolVisible(ctx context.Context, p auth.Principal, name string) bool {
	if s.strictMode != nil && s.strictMode(ctx) && !coreTool(name) {
		return false
	}
	if !s.toolEnabled(ctx, name) {
		return false
	}
	if adminTool(name) && !adminToolAllowed(p, name) {
		return false
	}
	return p.KeyID == "" || contains(p.Scopes, name)
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

const (
	// DefaultResponseBytes bounds one tool answer when the operator set no
	// per-tool budget. Roughly six thousand tokens: large enough for a full
	// search page, small enough to leave the agent room to work.
	DefaultResponseBytes = 24 << 10
	// MinResponseBytes keeps a budget from becoming a header with no content.
	MinResponseBytes = 2000
	// MaxResponseBytes is the ceiling an operator or caller may raise a tool to.
	MaxResponseBytes = 256 << 10
)

// responseBudget is the byte budget for one answer of this tool.
func (s *Server) responseBudget(ctx context.Context, name string) int {
	var budget int
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT max_response_bytes FROM mcp_tools WHERE name=?`), name).Scan(&budget); err != nil || budget <= 0 {
		return DefaultResponseBytes
	}
	return min(max(budget, MinResponseBytes), MaxResponseBytes)
}

// clampResponse trims one answer to the byte budget without hiding that it did
// so. Two properties matter for an agent reading the result:
//
//   - the cut lands on a result boundary, so the last entry is whole rather than
//     ending mid-line, and
//   - the trailing Notes section survives, because that is where the tool
//     explains which retrieval path ran and what the ACL filtered. Losing it
//     would turn "indexing, answered live" into an unexplained short answer.
func clampResponse(text string, budget int) string {
	if budget <= 0 || len(text) <= budget {
		return text
	}
	body, notes := text, ""
	if at := strings.LastIndex(text, "\n### Notes\n"); at >= 0 {
		body, notes = text[:at], text[at:]
	}
	// The notes only keep their reservation while they stay a small part of the
	// budget; an oversized tail would leave no room for actual results.
	reserved := len(notes)
	if reserved > budget/3 {
		body, notes, reserved = text, "", 0
	}
	total := sectionCount(body)
	room := budget - reserved - responseNoticeBytes
	if room < MinResponseBytes/2 {
		room = budget / 2
	}
	kept := cutAtBoundary(body, room)
	shown := sectionCount(kept)
	notice := fmt.Sprintf("\n\n### Truncated\n- This answer was cut to the %s byte budget of this tool; %s bytes were produced.\n",
		thousands(budget), thousands(len(text)))
	if total > 0 {
		notice += fmt.Sprintf("- %d of %d result sections are included. The rest are not lost, only unsent.\n", shown, total)
	}
	notice += "- Narrow the next call instead of retrying the same one: add libraryId or path, lower limit, or read a line range with read-file.\n"
	return strings.TrimRight(kept, "\n") + notice + notes
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// responseNoticeBytes reserves room for the truncation notice itself, so adding
// it can never push the answer back over the budget.
const responseNoticeBytes = 320

// sectionCount counts the result entries of a formatted answer. The formatters
// use a `### ` heading per result, and the list formatters use a `- ` item.
func sectionCount(text string) int {
	if count := strings.Count(text, "\n### "); count > 0 {
		return count
	}
	return strings.Count(text, "\n- ")
}

// cutAtBoundary returns the longest prefix within limit that ends on a result
// boundary. A section start is preferred so the last result stays whole, but
// not at any price: when the nearest section start is near the beginning — one
// long section, or a heading right after the title — cutting there would throw
// away most of the budget, so a paragraph or line boundary is used instead and
// the content is kept.
func cutAtBoundary(text string, limit int) string {
	if limit >= len(text) {
		return text
	}
	window := text[:limit]
	best := ""
	if at := strings.LastIndex(window, "\n### "); at > 0 {
		best = text[:at]
	}
	if len(best)*10 >= limit*6 {
		return best
	}
	if at := strings.LastIndex(window, "\n\n"); at > len(best) {
		return text[:at]
	}
	if at := strings.LastIndexByte(window, '\n'); at > len(best) {
		return text[:at]
	}
	if best != "" {
		return best
	}
	return runeSafeCut(window, len(window))
}

// thousands formats a byte count the way the notice reads best.
func thousands(value int) string {
	digits := strconv.Itoa(value)
	var b strings.Builder
	for index, char := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(char)
	}
	return b.String()
}

func (s *Server) platformStatus(ctx context.Context) (string, error) {
	if err := s.store.DB.PingContext(ctx); err != nil {
		return "", fmt.Errorf("metadata database unavailable: %w", err)
	}
	var repositories, bitbucket, gitlab, pending, running, failed int
	err := s.store.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN source_type='bitbucket' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN source_type='gitlab' THEN 1 ELSE 0 END),0) FROM repositories WHERE enabled=1`).Scan(&repositories, &bitbucket, &gitlab)
	if err != nil {
		return "", err
	}
	err = s.store.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0) FROM index_jobs`).Scan(&pending, &running, &failed)
	if err != nil {
		return "", err
	}
	status := fmt.Sprintf("## git-ctx Platform Status\n\n- Version: %s\n- Metadata Database: connected\n- Enabled Repositories: %d\n- Bitbucket Repositories: %d\n- GitLab Repositories: %d\n- Index Jobs Pending: %d\n- Index Jobs Running: %d\n- Index Jobs Failed: %d\n", version.Full(), repositories, bitbucket, gitlab, pending, running, failed)
	// Connector health belongs in the status an operator asks an agent for: a
	// paused source is the difference between "nothing matched" and "we are not
	// currently able to look".
	if s.health != nil {
		if states := s.health(); len(states) > 0 {
			status += "\n### Source Connectors\n"
			for _, state := range states {
				status += fmt.Sprintf("- %s: %s", state.Source, state.State)
				if state.LastError != "" {
					status += " — " + state.LastError
				}
				if !state.RetryAt.IsZero() {
					status += fmt.Sprintf(" (retry at %s)", state.RetryAt.UTC().Format(time.RFC3339))
				}
				status += "\n"
			}
		}
	}
	if s.embeddingHealth != nil {
		if embeddingStatus := strings.TrimSpace(s.embeddingHealth(ctx)); embeddingStatus != "" {
			status += "\n### Embedding Retrieval\n" + embeddingStatus + "\n"
		}
	}
	return status, nil
}

func (s *Server) indexJobs(ctx context.Context, p auth.Principal, status string, limit int) (string, error) {
	status = strings.TrimSpace(status)
	if status != "" && status != "pending" && status != "running" && status != "completed" && status != "failed" {
		return "", errors.New("status must be pending, running, completed, or failed")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	statement := `SELECT j.id,r.library_id,j.ref_name,j.kind,j.status,j.attempts,j.files_processed,j.error_message,j.created_at
FROM index_jobs j JOIN repositories r ON r.id=j.repository_id`
	var args []any
	if status != "" {
		statement += ` WHERE j.status=?`
		args = append(args, status)
	}
	statement += ` ORDER BY j.created_at DESC LIMIT ?`
	args = append(args, min(limit*5, 500))
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("## Recent Index Jobs\n")
	count := 0
	for rows.Next() {
		var id, libraryID, ref, kind, state, message string
		var attempts, files int
		var created time.Time
		if err = rows.Scan(&id, &libraryID, &ref, &kind, &state, &attempts, &files, &message, &created); err != nil {
			return "", err
		}
		if !libraryAllowed(libraryID, p.AllowedRepositories) {
			continue
		}
		fmt.Fprintf(&b, "\n- Job: %s\n  Library ID: %s\n  Ref: %s\n  Kind: %s\n  Status: %s\n  Attempts: %d\n  Files: %d\n  Created: %s\n", id, libraryID, ref, kind, state, attempts, files, created.UTC().Format(time.RFC3339))
		if message != "" {
			fmt.Fprintf(&b, "  Error: %s\n", truncate(message, 300))
		}
		count++
		if count == limit {
			break
		}
	}
	if count == 0 {
		return "No index jobs matched the requested scope.", rows.Err()
	}
	return b.String(), rows.Err()
}

func (s *Server) reindexRepository(ctx context.Context, p auth.Principal, libraryID, ref string) (string, error) {
	if !libraryAllowed(libraryID, p.AllowedRepositories) {
		return "", errors.New("library is unavailable or access is denied")
	}
	base := baseLibraryID(libraryID)
	if base == "" {
		return "", errors.New("libraryId must use /organization/project[/version]")
	}
	var repositoryID, defaultRef string
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id,default_branch FROM repositories WHERE library_id=? AND enabled=1`), base).Scan(&repositoryID, &defaultRef); err != nil {
		return "", errors.New("library is unavailable or access is denied")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = defaultRef
	}
	var existing string
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id FROM index_jobs WHERE repository_id=? AND ref_name=? AND status IN ('pending','running') ORDER BY created_at DESC LIMIT 1`), repositoryID, ref).Scan(&existing)
	if err == nil {
		return fmt.Sprintf("Reindex is already queued or running.\n\n- Job: %s\n- Library ID: %s\n- Ref: %s\n", existing, base, ref), nil
	}
	raw := make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return "", err
	}
	jobID := "mcp:" + hex.EncodeToString(raw)
	if _, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,next_run_at) VALUES(?,?,?,'manual','pending',?)`), jobID, repositoryID, ref, time.Now().UTC()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reindex queued.\n\n- Job: %s\n- Library ID: %s\n- Ref: %s\n", jobID, base, ref), nil
}
func (s *Server) cacheKey(ctx context.Context, p auth.Principal, tool string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	principals := principalACLs(p)
	sort.Strings(principals)
	repositories := append([]string(nil), p.AllowedRepositories...)
	sort.Strings(repositories)
	aclRevision := s.aclRevision(ctx, principals)
	configRevision := s.searchConfigRevision(ctx)
	return tool + "|" + strings.Join(principals, ",") + "|" + strings.Join(repositories, ",") + "|" + aclRevision + "|" + configRevision + "|" + string(raw)
}

func (s *Server) searchConfigRevision(ctx context.Context) string {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT category,version FROM system_settings
WHERE category IN ('search','model','vector','opensearch') ORDER BY category`)
	if err != nil {
		return "unavailable"
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var category string
		var version int
		if rows.Scan(&category, &version) == nil {
			_, _ = fmt.Fprintf(digest, "%s:%d\n", category, version)
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (s *Server) aclRevision(ctx context.Context, principals []string) string {
	if len(principals) == 0 {
		return "none"
	}
	placeholders := make([]string, len(principals))
	args := make([]any, 0, len(principals))
	for index, principal := range principals {
		placeholders[index] = "?"
		args = append(args, principal)
	}
	query := `SELECT p.repository_id,p.principal,p.permission,COALESCE(CAST(r.indexed_at AS TEXT),'')
FROM repository_permissions p JOIN repositories r ON r.id=p.repository_id
WHERE p.principal IN (` + strings.Join(placeholders, ",") + `) OR p.principal='*'
ORDER BY p.repository_id,p.principal`
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(query), args...)
	if err != nil {
		return "unavailable"
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var repositoryID, principal, permission, indexedAt string
		if rows.Scan(&repositoryID, &principal, &permission, &indexedAt) == nil {
			_, _ = fmt.Fprintf(digest, "%s\x00%s\x00%s\x00%s\n", repositoryID, principal, permission, indexedAt)
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
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
func (s *Server) finishCall(w http.ResponseWriter, r *http.Request, req request, p auth.Principal, tool, libraryID string, start time.Time, text string, err error, empty bool, budget int, audit callAudit) {
	outcome, truncated, results, mode := "success", false, 0, ""
	switch {
	case err != nil:
		outcome = "error"
		text = err.Error()
	case empty:
		outcome = "empty"
		mode = retrievalMode(text)
	default:
		// The cache holds the whole answer, so a later call with a larger budget
		// still gets everything; only what is sent now is bounded.
		produced := len(text)
		results, mode = sectionCount(text), retrievalMode(text)
		text = clampResponse(text, budget)
		truncated = len(text) != produced
	}
	// The audit row is written with the server context: a call that timed out
	// must still be recorded, and its request context is already cancelled.
	ctx := context.WithoutCancel(r.Context())
	callID := fmt.Sprintf("%d", time.Now().UnixNano())
	summary := audit.trace.Summary()
	if err != nil && summary == "" {
		summary = errorCode(err) + ": " + clip(err.Error(), 160)
	}
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO mcp_calls(id,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,arguments_hash,result_count,cache_hit,error_code,retrieval_mode,trace_summary) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		callID, p.UserID, p.KeyPrefix, tool, libraryID, outcome, time.Since(start).Milliseconds(), clientIP(r), len(text), boolInt(truncated),
		audit.sessionID, audit.requestID, audit.clientName, audit.clientVersion, audit.preview, audit.hash, results, boolInt(audit.cacheHit), errorCode(err), mode, summary)
	// One statement for the whole trace. A row per stage would put up to sixty
	// round trips on the response path of every call.
	if steps := audit.trace.Steps(); len(steps) > 0 {
		values := make([]string, 0, len(steps))
		args := make([]any, 0, len(steps)*10)
		for _, step := range steps {
			values = append(values, "(?,?,?,?,?,?,?,?,?,?)")
			args = append(args, callID, step.Sequence, step.Stage, clip(step.Target, 200), step.Status,
				clip(step.Detail, 300), step.Candidates, step.Results, step.DurationMS, step.OffsetMS)
		}
		_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO mcp_call_steps(call_id,sequence,stage,target,status,detail,candidates,results,duration_ms,offset_ms) VALUES `+strings.Join(values, ",")), args...)
	}
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
		if x.Snippets == 0 {
			b.WriteString("- Note: not indexed yet; query-docs answers this library from the live source code search API.\n")
		}
	}
	return b.String()
}

func formatRepositories(items []search.RepositoryResult) string {
	if len(items) == 0 {
		return "No accessible Bitbucket or GitLab repositories matched the query."
	}
	var b strings.Builder
	b.WriteString("## Accessible Repositories\n")
	for _, item := range items {
		indexed := "not indexed"
		if item.IndexedAt.Valid {
			indexed = item.IndexedAt.Time.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "\n- Name: %s\n  Library ID: %s\n  Source: %s\n  Project: %s\n  Repository: %s\n  Default Branch: %s\n  Indexed At: %s\n  Description: %s\n", item.Name, item.LibraryID, item.SourceType, item.ProjectKey, item.Slug, item.DefaultBranch, indexed, item.Description)
	}
	return b.String()
}

func formatSourceResults(items []search.SourceResult) string {
	if len(items) == 0 {
		return "No source matches were found in accessible repositories. Broaden the query, check the source connection, or run an index."
	}
	var b strings.Builder
	b.WriteString("## Source Search Results\n")
	for _, item := range items {
		fmt.Fprintf(&b, "\n### %s · %s\n\n%s\n\nSource: %s://%s/%s@%s/%s#L%d-L%d\n", item.LibraryID, item.Path, item.Snippet, item.SourceType, item.ProjectKey, item.RepositorySlug, item.CommitID, item.Path, item.LineStart, item.LineEnd)
	}
	return b.String()
}

// formatRepositorySearch appends the next step so a coding agent that asked for
// repositories does not stop before it has any code.
func formatRepositorySearch(items []search.RepositoryResult, query string) string {
	text := formatRepositories(items)
	if len(items) == 0 {
		return text
	}
	return text + fmt.Sprintf("\nThis tool matched repository names only. For file contents call `search-code {\"query\":%q}`.\n", query)
}

func formatCodeSearch(result search.CodeSearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Code Search\n\nNormalized query: `%s`\n", result.Query)
	if result.Warning != "" {
		fmt.Fprintf(&b, "\n> %s\n", result.Warning)
	}
	b.WriteString("\n### Repository Matches\n")
	if len(result.Repositories) == 0 {
		b.WriteString("\nNo repository names or descriptions matched.\n")
	} else {
		for _, item := range result.Repositories {
			fmt.Fprintf(&b, "\n- **%s** — `%s` (%s, default ref `%s`)\n", item.Name, item.LibraryID, item.SourceType, item.DefaultBranch)
			if item.Description != "" {
				fmt.Fprintf(&b, "  %s\n", item.Description)
			}
		}
	}
	fmt.Fprintf(&b, "\n### Source Matches (%d)\n", len(result.Hits))
	if len(result.Hits) == 0 {
		b.WriteString("\nNo file contents matched. This is not the same as \"the code does not exist\": check the notes below.\n")
		if len(result.Repositories) > 0 {
			fmt.Fprintf(&b, "\nNext step: retry with a narrower scope, for example `search-code {\"query\":%q,\"repository\":%q}`, or use `find-symbol` for an exact identifier.\n",
				result.Query, result.Repositories[0].LibraryID)
		}
	} else {
		for _, item := range result.Hits {
			fmt.Fprintf(&b, "\n#### %s · %s\n\n%s\n\nSource: %s://%s/%s@%s/%s#L%d-L%d\n", item.LibraryID, item.Path, item.Snippet, item.SourceType, item.ProjectKey, item.RepositorySlug, item.CommitID, item.Path, item.LineStart, item.LineEnd)
		}
	}
	// Diagnostics always ship: an agent that knows the search ran a name-only
	// path, hit a timeout or was ACL-filtered can pick a better next call
	// instead of telling the user the code is missing.
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

// formatFileResults groups paths by repository and states what can be read next
// for each hit, so an agent can chain straight into content tools.
func formatFileResults(result search.FileSearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## File Search\n\nPattern: `%s`\nMatches: %d\n", result.Pattern, len(result.Files))
	if len(result.Files) == 0 {
		b.WriteString("\nNo file name matched. Try a wildcard such as `*.sql`, a path fragment such as `migrations/`, or `search-code` for file contents.\n")
	} else {
		current := ""
		for _, item := range result.Files {
			if item.LibraryID != current {
				current = item.LibraryID
				fmt.Fprintf(&b, "\n### %s (%s, ref `%s`)\n", item.LibraryID, item.SourceType, item.Ref)
			}
			readable := "content not indexed; use search-code or the source UI"
			if item.ContentIndexed {
				readable = "content indexed; query-docs and get-symbol-context can read it"
			}
			// Bitbucket reports file sizes, GitLab tree listings do not; only print
			// a size when the source actually gave one.
			size := ""
			if item.SizeBytes > 0 {
				size = fmt.Sprintf("%d bytes, ", item.SizeBytes)
			}
			fmt.Fprintf(&b, "- `%s` (%s%s)\n", item.Path, size, readable)
		}
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

// formatFileContent returns the file in a fenced block with a citation header,
// so an agent can quote it and link back to the exact ref and lines.
func formatFileContent(file search.FileContent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n`%s` · ref `%s` · lines %d-%d of %d · %s\n\n",
		file.Path, file.LibraryID, file.Ref, file.StartLine, file.EndLine, file.TotalLines, file.Origin)
	fence := "```"
	if strings.Contains(file.Content, "```") {
		fence = "````"
	}
	fmt.Fprintf(&b, "%s%s\n%s\n%s\n", fence, languageHint(file.Path), file.Content, fence)
	fmt.Fprintf(&b, "\nSource: `%s://%s/%s@%s/%s#L%d-L%d`\n", file.SourceType, file.ProjectKey, file.RepositorySlug,
		firstNonEmpty(file.CommitID, file.Ref), file.Path, file.StartLine, file.EndLine)
	if len(file.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range file.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

// languageHint labels the fenced block so clients highlight it correctly.
func languageHint(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "":
		return ""
	case "yml":
		return "yaml"
	case "tf", "tfvars":
		return "hcl"
	case "mod", "sum":
		return ""
	default:
		return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func formatSemanticSearch(result search.SemanticSearch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Semantic Search\n\nQuery: `%s`\nMatches: %d · retrieval: %s\n", result.Query, len(result.Hits), result.Mode)
	scoreLabel := "similarity"
	if strings.Contains(result.Mode, "keyword") || strings.Contains(result.Mode, "source-query") {
		scoreLabel = "rank score"
	}
	for _, hit := range result.Hits {
		fmt.Fprintf(&b, "\n### %s · %s (%s %.2f)\n\n%s\n\nSource: `%s://%s@%s/%s#L%d-L%d`\n",
			hit.LibraryID, hit.FilePath, scoreLabel, hit.Score, strings.TrimSpace(hit.Content),
			hit.SourceType, strings.TrimPrefix(hit.LibraryID, "/"), firstNonEmpty(hit.CommitID, hit.Ref), hit.FilePath, hit.LineStart, hit.LineEnd)
	}
	if len(result.Hits) == 0 {
		b.WriteString("\nNo accessible code or documentation matched. Try another term, source type, or repository scope.\n")
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

func formatDependents(result search.DependentSearch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Dependents of %s\n\n%d reference(s) in %d repository(ies)\n", result.Target, len(result.Dependents), len(result.Repositories))
	current := ""
	for _, item := range result.Dependents {
		if item.LibraryID != current {
			current = item.LibraryID
			fmt.Fprintf(&b, "\n### %s (ref `%s`)\n", item.LibraryID, item.Ref)
		}
		from := item.FromSymbol
		if from == "" {
			from = "(file scope)"
		}
		fmt.Fprintf(&b, "- `%s:%d` %s %s → `%s`\n", item.FilePath, item.LineNumber, from, item.Kind, item.Target)
	}
	if len(result.Dependents) == 0 {
		b.WriteString("\nNothing depends on it in the indexed refs, or the dependency is expressed in a way the parser does not capture. Confirm with search-code before assuming it is unused.\n")
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

func formatChangeRequests(result search.ChangeRequestSearch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Merge and Pull Requests\n\nQuery: `%s`\nMatches: %d\n", result.Query, len(result.Requests))
	for _, item := range result.Requests {
		fmt.Fprintf(&b, "\n### %s %s — %s\n\n%s · %s · `%s` → `%s`",
			item.ID, item.Title, item.State, item.LibraryID, item.Author, item.SourceRef, item.TargetRef)
		if !item.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, " · updated %s", item.UpdatedAt.UTC().Format("2006-01-02"))
		}
		b.WriteString("\n")
		if description := strings.TrimSpace(item.Description); description != "" {
			fmt.Fprintf(&b, "\n%s\n", description)
		}
		if item.URL != "" {
			fmt.Fprintf(&b, "\n%s\n", item.URL)
		}
	}
	if len(result.Requests) == 0 {
		b.WriteString("\nNo merge or pull request matched. Try a broader term, state \"all\", or get-file-history for the commits themselves.\n")
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

func formatFileHistory(history search.FileHistory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## History of %s\n\n`%s` · ref `%s` · %d commit(s)\n", history.Path, history.LibraryID, history.Ref, len(history.Commits))
	for _, commit := range history.Commits {
		when := ""
		if !commit.AuthoredAt.IsZero() {
			when = commit.AuthoredAt.UTC().Format("2006-01-02 15:04 MST")
		}
		subject := commit.Message
		if index := strings.Index(subject, "\n"); index > 0 {
			subject = subject[:index]
		}
		fmt.Fprintf(&b, "\n- `%s` %s — %s\n  %s\n", commit.DisplayID, when, commit.Author, subject)
		if commit.URL != "" {
			fmt.Fprintf(&b, "  %s\n", commit.URL)
		}
	}
	for _, diagnostic := range history.Diagnostics {
		fmt.Fprintf(&b, "\n- %s\n", diagnostic)
	}
	return b.String()
}

func formatDirectory(listing search.DirectoryListing) string {
	var b strings.Builder
	root := listing.Path
	if root == "" {
		root = "(repository root)"
	}
	fmt.Fprintf(&b, "## %s\n\n`%s` · ref `%s` · %d entries\n\n", root, listing.LibraryID, listing.Ref, len(listing.Entries))
	for _, entry := range listing.Entries {
		if entry.Directory {
			fmt.Fprintf(&b, "- %s/ (%d files)\n", entry.Name, entry.Files)
			continue
		}
		state := ""
		if !entry.ContentIndexed {
			state = " — content not indexed"
		}
		fmt.Fprintf(&b, "- %s%s\n", entry.Name, state)
	}
	return b.String()
}

func formatRepositoryMap(item search.RepositoryMap) string {
	var decoded any
	if json.Unmarshal([]byte(item.SummaryJSON), &decoded) != nil {
		decoded = map[string]any{}
	}
	pretty, _ := json.MarshalIndent(decoded, "", "  ")
	text := fmt.Sprintf("## Repository Map\n\n- Library ID: %s\n- Ref: %s\n- Commit: %s\n\n```json\n%s\n```\n", item.LibraryID, item.Ref, item.CommitID, pretty)
	if len(item.Conventions) > 0 {
		text += "\n### Project conventions\n\nRead these before writing code for this repository; use read-file.\n"
		for _, path := range item.Conventions {
			text += fmt.Sprintf("- `%s`\n", path)
		}
	}
	return text
}

func formatSymbols(items []search.SymbolResult) string {
	if len(items) == 0 {
		return "No accessible symbols matched the query. Reindex the repository or broaden the symbol name."
	}
	var b strings.Builder
	b.WriteString("## Symbol Search Results\n")
	for _, item := range items {
		fmt.Fprintf(&b, "\n### %s\n\n- Kind: %s\n- Language: %s\n- Library ID: %s/%s\n- Signature: `%s`\n- Source: bitcontext://%s@%s/%s#L%d-L%d\n",
			item.QualifiedName, item.Kind, item.Language, item.LibraryID, item.Ref, item.Signature, item.LibraryID, item.CommitID, item.FilePath, item.LineStart, item.LineEnd)
		if item.Documentation != "" {
			fmt.Fprintf(&b, "- Documentation: %s\n", item.Documentation)
		}
	}
	return b.String()
}

func formatSymbolContext(item search.SymbolResult) string {
	return fmt.Sprintf("## %s\n\n- Kind: %s\n- Language: %s\n- Signature: `%s`\n- Source: bitcontext://%s@%s/%s#L%d-L%d\n\n%s\n\n```%s\n%s\n```\n",
		item.QualifiedName, item.Kind, item.Language, item.Signature, item.LibraryID, item.CommitID, item.FilePath, item.LineStart, item.LineEnd, item.Documentation, item.Language, item.Content)
}

func formatDependencies(items []search.DependencyResult) string {
	if len(items) == 0 {
		return "No indexed dependencies matched the symbol or module."
	}
	var b strings.Builder
	b.WriteString("## Dependency Trace\n")
	for _, item := range items {
		from := item.FromSymbol
		if from == "" {
			from = item.FilePath
		}
		fmt.Fprintf(&b, "\n- `%s` --%s--> `%s`\n  Source: bitcontext://%s@%s/%s#L%d\n", from, item.Kind, item.Target, item.LibraryID, item.CommitID, item.FilePath, item.LineNumber)
	}
	return b.String()
}

func formatRefComparison(item search.RefComparison) string {
	if len(item.Changes) == 0 {
		return fmt.Sprintf("## Ref Comparison\n\nNo indexed symbol changes between `%s` and `%s` in %s.", item.BaseRef, item.HeadRef, item.LibraryID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Ref Comparison\n\n- Library ID: %s\n- Base: %s\n- Head: %s\n", item.LibraryID, item.BaseRef, item.HeadRef)
	for _, change := range item.Changes {
		fmt.Fprintf(&b, "\n- %s `%s` (%s) · %s\n", strings.ToUpper(change.Type), change.Name, change.Kind, change.FilePath)
		if change.BeforeSignature != "" {
			fmt.Fprintf(&b, "  Before: `%s`\n", change.BeforeSignature)
		}
		if change.AfterSignature != "" {
			fmt.Fprintf(&b, "  After: `%s`\n", change.AfterSignature)
		}
	}
	return b.String()
}

func formatChangeImpact(item search.ChangeImpact) string {
	var b strings.Builder
	b.WriteString(formatRefComparison(item.Comparison))
	b.WriteString("\n\n## Potentially Affected Dependencies\n")
	if len(item.Dependents) == 0 {
		b.WriteString("\nNo incoming indexed dependencies were found.")
		return b.String()
	}
	for _, dependency := range item.Dependents {
		from := dependency.FromSymbol
		if from == "" {
			from = dependency.FilePath
		}
		fmt.Fprintf(&b, "\n- `%s` depends on `%s` (%s)\n  Source: bitcontext://%s@%s/%s#L%d\n", from, dependency.Target, dependency.Kind, dependency.LibraryID, dependency.CommitID, dependency.FilePath, dependency.LineNumber)
	}
	return b.String()
}

func formatRunbooks(items []search.RunbookResult) string {
	if len(items) == 0 {
		return "No accessible runbooks matched the query."
	}
	var b strings.Builder
	b.WriteString("## Runbooks\n")
	for _, item := range items {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n\nSource: bitcontext://%s@%s/%s#L%d-L%d\n", item.Heading, item.Content, item.LibraryID, item.CommitID, item.FilePath, item.LineStart, item.LineEnd)
	}
	return b.String()
}

func formatSearchExplanation(item search.SearchExplanation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Search Explanation\n\n- Library ID: %s\n- Ref: %s\n- Retrieval: %s\n", item.LibraryID, item.Ref, item.RetrievalMode)
	if len(item.Hits) == 0 {
		b.WriteString("\nNo lexical candidates matched the normalized query.")
		return b.String()
	}
	for _, hit := range item.Hits {
		fmt.Fprintf(&b, "\n### %s\n\n- Matched Terms: %d\n- Keyword Occurrences: %d\n- Reasons: %s\n- Embedding: %s / %s / %s\n- Source: bitcontext://%s@%s/%s#L%d-L%d\n",
			hit.Heading, hit.MatchedTerms, hit.KeywordOccurrences, strings.Join(hit.Reasons, "; "), hit.EmbeddingProvider, hit.EmbeddingModel, hit.EmbeddingRevision, item.LibraryID, hit.CommitID, hit.FilePath, hit.LineStart, hit.LineEnd)
	}
	return b.String()
}

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

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/search"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"git-ctx/internal/version"
	"net/http"
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

func New(s *search.Service, db *store.Store) *Server {
	return &Server{search: s, store: db, sessions: map[string]*session{}, cache: map[string]cacheEntry{}}
}

func (s *Server) SetStrictCompatibilityLoader(loader func(context.Context) bool) {
	s.strictMode = loader
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
		// The process-wide HTTP WriteTimeout protects ordinary API responses,
		// but an MCP SSE session is intentionally long lived. Clear only this
		// response's deadline; disconnects and session deletion still cancel it.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
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
		// Walking the registry keeps the advertised set and the callable set the
		// same list, and is where a per-client tool profile would narrow it.
		p, _ := auth.FromContext(r.Context())
		available := make([]map[string]any, 0, len(registry))
		for i := range registry {
			if !s.toolVisible(r.Context(), p, &registry[i]) {
				continue
			}
			available = append(available, describeTool(&registry[i]))
		}
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
const serverInstructions = `git-ctx searches an organization's own Bitbucket and GitLab repositories, and the Confluence spaces and Jira projects connected alongside them. Every result is filtered by the caller's permissions on the repository or space it came from.

Choosing a tool:
- Any question about source code, configuration or "where is X": start with search-code. It returns matching repositories AND file contents in one call, and it covers wiki pages and issues too, because those are searched from the same index.
- When search-code finds nothing because the wording differs from the code: search-semantic matches by meaning across repositories.
- Looking for a file by name or extension: find-file (Dockerfile, *.tf, **/migrations/*.sql).
- Need the file itself: read-file, optionally with startLine and endLine.
- Orienting in an unknown repository: list-directory, then get-repository-map, which also lists the third-party packages the repository declares.
- "Why is this like this", "when did this change": get-file-history for commits, search-merge-requests for the reasoning behind them.
- Exact identifier: find-symbol, then get-symbol-context.
- Before changing shared code: find-dependents shows every repository that uses it, and build-context assembles callers, dependencies, tests and history in one call.
- "Who owns this", "who should review this": find-code-owner answers from the repository's CODEOWNERS declaration when it has one, and ranks recent contributors otherwise.
- Third-party library questions ("who uses this package", "which version are we on", an advisory): find-dependency-usage reads the manifests and lock files and groups repositories by version. find-dependents cannot answer it — an import line has no version and a transitive dependency has no import line.
- Documentation for a known library id: query-docs. resolve-library-id turns a product or repository name into the id every tool here takes.
- search-repositories returns repository names only, never file contents. search-source asks the source servers alone; prefer search-code, which asks them and the index.
- What calls this and what it calls: trace-dependencies. What covers it: find-tests.
- Reviewing a change: compare-refs for what moved, get-change-impact for what depends on it, assess-change-risk for a judgement on both.
- Incidents and procedure: find-runbook, over the runbook and operations pages in the connected wikis.
- Sizing up a repository: get-repository-health for index age, test coverage and documented conventions; get-architecture-map for how repositories import each other.
- A result that looks wrong: explain-search-result gives the ranking and the path that produced it.
- Context for elsewhere: export-context bundles libraries into one document, get-context-pack returns a pack an operator curated.

Reading the results:
- Search responses end with Notes explaining which path ran, what the ACL filtered and whether a timeout was hit. An empty result with an ACL or indexing note is not proof that the code does not exist.
- The live source search and the local index are both used. A note saying an answer came from the index means it is as recent as the last index run, not as recent as the repository; a note saying the live path was unavailable explains why. Wiki pages and issues are only ever in the index.
- A note may also say that the reranker or the vector database could not be reached. The answer still stands, but its order or its breadth is not what it would have been.
- An argument a tool does not have is dropped, and the reply says which one and lists the ones it accepts. Read that line before retrying.
- Snippets and files are secret-masked. Cite the Source line, which points at the exact ref and lines.
- Answers are bounded by a byte budget. A Truncated section states how many results were withheld; narrow the call (libraryId, path, limit, read-file line range) rather than repeating it. Pass maxBytes to ask for a smaller answer.`

// SetHealthLoader installs the source connector health source.
func (s *Server) SetHealthLoader(loader func() []source.BreakerState) { s.health = loader }

// SetEmbeddingHealthLoader adds the effective retrieval policy and model
// circuit state to the administrator-only platform status tool.
func (s *Server) SetEmbeddingHealthLoader(loader func(context.Context) string) {
	s.embeddingHealth = loader
}

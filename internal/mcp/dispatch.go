package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/calltrace"
	"git-ctx/internal/search"
	"git-ctx/internal/toolcatalog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"net/http"
	"sort"
)

// Tool dispatch: decode one JSON-RPC call, resolve it through the registry, run
// it, and write the answer together with its audit record.

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
	entry, known := lookupTool(params.Name)
	if !known {
		write(w, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "Unknown tool"}})
		return
	}
	ctx, span := otel.Tracer("git-ctx/mcp").Start(r.Context(), "mcp."+params.Name,
		oteltrace.WithAttributes(attribute.String("mcp.tool.name", params.Name)))
	defer span.End()
	r = r.WithContext(ctx)
	if !s.toolVisible(r.Context(), p, entry) {
		// A credential reaching for a tool it was not granted is exactly what a
		// security review looks for, so the refusal is audited like any other
		// call instead of being answered and forgotten.
		denied := s.auditContext(w, r, params.Name, params.Arguments)
		_, deniedTrace := calltrace.New(r.Context())
		denied.trace = deniedTrace
		s.finishCall(w, r, req, p, params.Name, "", start, "", errToolNotPermitted, false, MinResponseBytes, denied)
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
	// Resolved before dispatch so a cache hit audits the same repository a miss
	// would; find-symbol and find-runbook used to set this inside their own case
	// and so recorded nothing when the answer came from the cache.
	libraryID := ""
	if entry.usesLibraryID {
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
	text, empty, err = entry.handler(s, r, p, params.Arguments)
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

// unknownArgumentNote names arguments the tool does not have.
//
// Every schema declares additionalProperties:false, and nothing enforced it.
// An agent that sent maxResults instead of limit, or library_id instead of
// libraryId, got an answer computed without it — the wrong size, or the wrong
// repository — with nothing in the reply to say the argument had been dropped.
// The call is still answered: refusing it would break agents that pass a
// harmless extra. It is answered with the drop written down.
func unknownArgumentNote(tool string, params json.RawMessage) string {
	entry, ok := lookupTool(tool)
	if !ok {
		return ""
	}
	var decoded struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil || len(decoded.Arguments) == 0 {
		return ""
	}
	arguments := decoded.Arguments
	properties, ok := entry.schema["properties"].(map[string]any)
	if !ok {
		return ""
	}
	var unknown []string
	for name := range arguments {
		if name == "maxBytes" {
			continue // added to the served schema by the catalog, not the registry
		}
		if strings.HasPrefix(name, "_") {
			continue // protocol fields such as _meta belong to the client, not the tool
		}
		if _, known := properties[name]; !known {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	known := make([]string, 0, len(properties)+1)
	for name := range properties {
		known = append(known, name)
	}
	known = append(known, "maxBytes")
	sort.Strings(known)
	return fmt.Sprintf("\n- ignored: %s is not an argument of %s, so this answer was produced without it. It accepts %s.\n",
		strings.Join(unknown, ", "), tool, strings.Join(known, ", "))
}

func (s *Server) finishCall(w http.ResponseWriter, r *http.Request, req request, p auth.Principal, tool, libraryID string, start time.Time, text string, err error, empty bool, budget int, audit callAudit) {
	outcome, truncated, results, mode := "success", false, 0, ""
	// produced is the answer's full size before the budget is applied. Recording
	// only the boolean threw away the one number that says how much a cut costs.
	produced := 0
	switch {
	case err != nil:
		outcome = "error"
		text = err.Error() + unknownArgumentNote(tool, req.Params)
	case empty:
		outcome = "empty"
		mode = retrievalMode(text)
		// An empty answer is where the age matters most: "this directory holds
		// nothing" and "this file has no history" read as facts about the
		// repository when they may be facts about a month-old index.
		text += unknownArgumentNote(tool, req.Params) + s.freshnessNote(r.Context(), p, tool, libraryID)
	default:
		// The cache holds the whole answer, so a later call with a larger budget
		// still gets everything; only what is sent now is bounded.
		produced = len(text)
		results, mode = sectionCount(text), retrievalMode(text)
		text = clampResponse(text, budget)
		truncated = len(text) != produced
		// Added here rather than inside each tool, for two reasons: the age is
		// read now rather than when a cached answer was first built, and a note
		// about the answer must not be what the budget cuts off.
		text += unknownArgumentNote(tool, req.Params) + s.freshnessNote(r.Context(), p, tool, libraryID)
	}
	// The audit row is written with the server context: a call that timed out
	// must still be recorded, and its request context is already cancelled.
	ctx := context.WithoutCancel(r.Context())
	callID := fmt.Sprintf("%d", time.Now().UnixNano())
	summary := audit.trace.Summary()
	if err != nil && summary == "" {
		summary = errorCode(err) + ": " + clip(err.Error(), 160)
	}
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO mcp_calls(id,user_id,api_key_prefix,tool,library_id,outcome,duration_ms,client_ip,response_bytes,truncated,session_id,request_id,client_name,client_version,arguments_preview,arguments_hash,result_count,cache_hit,error_code,retrieval_mode,trace_summary,produced_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		callID, p.UserID, p.KeyPrefix, tool, libraryID, outcome, time.Since(start).Milliseconds(), clientIP(r), len(text), boolInt(truncated),
		audit.sessionID, audit.requestID, audit.clientName, audit.clientVersion, audit.preview, audit.hash, results, boolInt(audit.cacheHit), errorCode(err), mode, summary, produced)
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

// freshnessNote says how old the index behind this answer is, when that is old
// enough to matter.
//
// Four tools said it and the ones an agent reads code with did not: read-file
// returned a month-old function body and said only that it came from the index,
// and find-symbol, query-docs, list-directory and the rest said nothing at all.
// An answer that does not carry its own age is read as current.
func (s *Server) freshnessNote(ctx context.Context, p auth.Principal, tool, libraryID string) string {
	if libraryID == "" || !contentTools[tool] {
		return ""
	}
	ages, err := s.search.IndexAges(ctx, principalACLs(p), []string{libraryID}, time.Now().UTC())
	if err != nil || len(ages) == 0 {
		return ""
	}
	note := search.FreshnessNote(ages)
	if note == "" {
		return ""
	}
	return "\n\n> " + note + "\n"
}

// contentTools are the tools whose answer is indexed content, or a fact read
// out of it. The ones that already carry the note themselves are not repeated
// here, and neither are the administrative tools, whose subject is the
// installation rather than the code.
var contentTools = map[string]bool{
	toolcatalog.ReadFile:            true,
	toolcatalog.QueryDocs:           true,
	toolcatalog.FindSymbol:          true,
	toolcatalog.GetSymbolContext:    true,
	toolcatalog.FindTests:           true,
	toolcatalog.TraceDependencies:   true,
	toolcatalog.ListDirectory:       true,
	toolcatalog.GetRepositoryMap:    true,
	toolcatalog.ExplainSearchResult: true,
	toolcatalog.GetFileHistory:      true,
	toolcatalog.CompareRefs:         true,
	toolcatalog.GetChangeImpact:     true,
}

func write(w http.ResponseWriter, v response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

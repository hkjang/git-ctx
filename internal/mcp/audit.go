package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"git-ctx/internal/calltrace"
	"git-ctx/internal/contentsecurity"
	oteltrace "go.opentelemetry.io/otel/trace"
	"net/http"
)

// What an operator or a reviewer needs to reconstruct one call: which client and
// session asked, what was asked, and how it was answered.

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
// errToolNotPermitted is the answer to a tool the credential was not granted.
// Its text is what the client sees, and it carries its own audit code so a
// refusal is never counted among ordinary failures.
var errToolNotPermitted = errors.New("This MCP tool is unavailable for this credential.")

func errorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errToolNotPermitted):
		return "tool_not_permitted"
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

package mcp

import (
	"context"
	"time"

	"git-ctx/internal/auth"
)

// Per-tool policy: whether this caller may see a tool, and the limits it runs
// under. The tool entry carries the rules; this file applies them.

func (s *Server) toolVisible(ctx context.Context, p auth.Principal, entry *tool) bool {
	if s.strictMode != nil && s.strictMode(ctx) && !entry.core {
		return false
	}
	if !s.toolEnabled(ctx, entry.name) {
		return false
	}
	if !entry.allowed(p) {
		return false
	}
	return p.KeyID == "" || contains(p.Scopes, entry.name)
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

// responseBudget is the byte budget for one answer of this tool.
func (s *Server) responseBudget(ctx context.Context, name string) int {
	var budget int
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT max_response_bytes FROM mcp_tools WHERE name=?`), name).Scan(&budget); err != nil || budget <= 0 {
		return DefaultResponseBytes
	}
	return min(max(budget, MinResponseBytes), MaxResponseBytes)
}

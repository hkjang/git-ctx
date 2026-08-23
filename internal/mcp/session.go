package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// MCP sessions. initialize opens one and every later call is checked against
// it, so a client that goes away stops holding server resources.

type session struct {
	clientName    string
	clientVersion string
	expires       time.Time
	done          chan struct{}
}

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

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"git-ctx/internal/auth"
)

// The tool answer cache. Its keys fold in the ACL and index state, so a cached
// answer can never outlive the permissions it was computed under.

type cacheEntry struct {
	text    string
	expires time.Time
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

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

// maxToolCacheEntries is deliberately a hard in-process bound. Cache keys
// include principals, repository restrictions, configuration revisions and
// arguments, so a busy multi-tenant server can otherwise accumulate many
// still-valid entries before their TTLs elapse.
const maxToolCacheEntries = 10000

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
	if !time.Now().Before(entry.expires) {
		delete(s.cache, key)
		return "", false
	}
	return entry.text, true
}

func (s *Server) deleteExpiredCacheEntriesLocked(now time.Time) {
	for key, entry := range s.cache {
		if !now.Before(entry.expires) {
			delete(s.cache, key)
		}
	}
}

// trimCacheLocked removes entries that will expire soonest. Choosing the key
// lexicographically when expirations tie makes eviction deterministic without
// maintaining a second unbounded ordering structure. In normal operation this
// loop removes one entry; the loop also repairs a map left above the limit by
// an older server version.
func (s *Server) trimCacheLocked(limit int) {
	for len(s.cache) > limit {
		victimKey := ""
		var victimExpiry time.Time
		for key, entry := range s.cache {
			if victimKey == "" || entry.expires.Before(victimExpiry) ||
				(entry.expires.Equal(victimExpiry) && key < victimKey) {
				victimKey = key
				victimExpiry = entry.expires
			}
		}
		delete(s.cache, victimKey)
	}
}

func (s *Server) storeCache(ctx context.Context, tool, key, text string) {
	var seconds int
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT cache_seconds FROM mcp_tools WHERE name=?`), tool).Scan(&seconds); err != nil || seconds <= 0 {
		return
	}
	now := time.Now()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if len(s.cache) >= maxToolCacheEntries {
		s.deleteExpiredCacheEntriesLocked(now)
	}

	// Updating an existing entry needs no spare slot. A new key reserves one
	// before insertion so the map never transiently exceeds the hard limit.
	limit := maxToolCacheEntries
	if _, exists := s.cache[key]; !exists {
		limit--
	}
	s.trimCacheLocked(limit)
	s.cache[key] = cacheEntry{text: text, expires: now.Add(time.Duration(seconds) * time.Second)}
}

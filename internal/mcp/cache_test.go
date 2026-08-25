package mcp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func enableToolCache(t *testing.T, server *Server) {
	t.Helper()
	if _, err := server.store.DB.Exec(`UPDATE mcp_tools SET cache_seconds=7200 WHERE name='resolve-library-id'`); err != nil {
		t.Fatal(err)
	}
}

func TestToolCacheHardLimitEvictsDeterministically(t *testing.T) {
	server := fixture(t)
	enableToolCache(t, server)
	expires := time.Now().Add(time.Hour)
	// Seed above the new limit to model a process that accumulated entries
	// under the previous implementation before this version took over.
	for index := 0; index < maxToolCacheEntries+1; index++ {
		key := fmt.Sprintf("entry-%05d", index)
		server.cache[key] = cacheEntry{text: key, expires: expires}
	}

	server.storeCache(context.Background(), "resolve-library-id", "new-entry", "new")

	server.cacheMu.Lock()
	defer server.cacheMu.Unlock()
	if got := len(server.cache); got != maxToolCacheEntries {
		t.Fatalf("cache size=%d, want hard limit %d", got, maxToolCacheEntries)
	}
	if _, exists := server.cache["entry-00000"]; exists {
		t.Fatal("lexicographically first key with the same expiry was not evicted")
	}
	if _, exists := server.cache["entry-00001"]; exists {
		t.Fatal("cache inherited above the limit was not fully trimmed")
	}
	if entry, exists := server.cache["new-entry"]; !exists || entry.text != "new" {
		t.Fatalf("new cache entry=%+v exists=%v", entry, exists)
	}
}

func TestToolCachePurgesExpiredEntriesBeforeLiveEntries(t *testing.T) {
	server := fixture(t)
	enableToolCache(t, server)
	now := time.Now()
	for index := 0; index < maxToolCacheEntries-1; index++ {
		key := fmt.Sprintf("live-%05d", index)
		server.cache[key] = cacheEntry{text: key, expires: now.Add(time.Hour)}
	}
	server.cache["expired"] = cacheEntry{text: "stale", expires: now.Add(-time.Second)}

	server.storeCache(context.Background(), "resolve-library-id", "new-entry", "new")

	server.cacheMu.Lock()
	if got := len(server.cache); got != maxToolCacheEntries {
		server.cacheMu.Unlock()
		t.Fatalf("cache size=%d, want %d after expiry cleanup", got, maxToolCacheEntries)
	}
	_, expiredExists := server.cache["expired"]
	_, liveExists := server.cache["live-00000"]
	server.cacheMu.Unlock()
	if expiredExists {
		t.Fatal("expired entry survived cache cleanup")
	}
	if !liveExists {
		t.Fatal("a live entry was evicted even though an expired slot was available")
	}
	if _, ok := server.cached("expired"); ok {
		t.Fatal("expired entry was returned as a cache hit")
	}
}

func TestToolCacheConcurrentAccessStaysBounded(t *testing.T) {
	server := fixture(t)
	enableToolCache(t, server)
	expires := time.Now().Add(time.Hour)
	for index := 0; index < maxToolCacheEntries-20; index++ {
		key := fmt.Sprintf("seed-%05d", index)
		server.cache[key] = cacheEntry{text: key, expires: expires}
	}

	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := 0; index < 10; index++ {
				key := fmt.Sprintf("worker-%02d-%02d", worker, index)
				server.storeCache(context.Background(), "resolve-library-id", key, key)
				_, _ = server.cached(key)
			}
		}()
	}
	workers.Wait()

	server.cacheMu.Lock()
	defer server.cacheMu.Unlock()
	if got := len(server.cache); got > maxToolCacheEntries {
		t.Fatalf("cache size=%d exceeds hard limit %d", got, maxToolCacheEntries)
	}
}

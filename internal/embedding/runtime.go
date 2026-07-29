package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RuntimePolicy controls process-local protection around an embedding endpoint.
// The provider still owns HTTP retries; this policy prevents every MCP request
// and index job from repeating those retries throughout an outage.
type RuntimePolicy struct {
	FailureThreshold int
	Cooldown         time.Duration
	CacheTTL         time.Duration
	CacheEntries     int
}

func (p RuntimePolicy) normalized() RuntimePolicy {
	if p.FailureThreshold < 1 {
		p.FailureThreshold = 2
	}
	if p.Cooldown <= 0 {
		p.Cooldown = time.Minute
	}
	if p.CacheTTL < 0 {
		p.CacheTTL = 0
	}
	if p.CacheEntries < 1 {
		p.CacheEntries = 1000
	}
	return p
}

type CircuitOpenError struct {
	RetryAt   time.Time
	LastError string
}

func (e *CircuitOpenError) Error() string {
	message := "embedding circuit is open"
	if !e.RetryAt.IsZero() {
		message += " until " + e.RetryAt.UTC().Format(time.RFC3339)
	}
	if e.LastError != "" {
		message += ": " + e.LastError
	}
	return message
}

type RuntimeSnapshot struct {
	Identity            string         `json:"identity"`
	State               string         `json:"state"`
	ConsecutiveFailures int            `json:"consecutiveFailures"`
	RetryAt             time.Time      `json:"retryAt,omitempty"`
	LastSuccess         time.Time      `json:"lastSuccess,omitempty"`
	LastFailure         time.Time      `json:"lastFailure,omitempty"`
	LastError           string         `json:"lastError,omitempty"`
	Requests            uint64         `json:"requests"`
	Failures            uint64         `json:"failures"`
	CacheHits           uint64         `json:"cacheHits"`
	CacheEntries        int            `json:"cacheEntries"`
	Fallbacks           map[string]int `json:"fallbacks"`
}

type runtimeState struct {
	consecutiveFailures      int
	openUntil, lastSuccess   time.Time
	lastFailure              time.Time
	lastError                string
	requests, failures, hits uint64
	halfOpen                 bool
}

type runtimeCacheEntry struct {
	identity string
	vector   []float32
	expires  time.Time
}

// Runtime shares health and query-vector cache state between MCP search and
// index workers. It contains no credentials and can safely be exposed through
// administrative diagnostics.
type Runtime struct {
	mu        sync.Mutex
	states    map[string]*runtimeState
	cache     map[string]runtimeCacheEntry
	fallbacks map[string]int
}

func NewRuntime() *Runtime {
	return &Runtime{
		states: map[string]*runtimeState{}, cache: map[string]runtimeCacheEntry{},
		fallbacks: map[string]int{},
	}
}

// Guard returns a tracked provider or fails immediately while the endpoint's
// circuit is open. factory is only called after the breaker allows an attempt.
func (r *Runtime) Guard(identity string, policy RuntimePolicy, factory func() (Provider, error)) (Provider, error) {
	if r == nil {
		return factory()
	}
	policy = policy.normalized()
	probe, err := r.allow(identity, policy)
	if err != nil {
		return nil, err
	}
	provider, err := factory()
	if err != nil {
		r.finish(identity, policy, err)
		return nil, err
	}
	return &runtimeProvider{runtime: r, identity: identity, policy: policy, provider: provider, probe: probe}, nil
}

func (r *Runtime) allow(identity string, policy RuntimePolicy) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state(identity)
	now := time.Now()
	if state.openUntil.After(now) {
		return false, &CircuitOpenError{RetryAt: state.openUntil, LastError: state.lastError}
	}
	if !state.openUntil.IsZero() {
		if state.halfOpen {
			return false, &CircuitOpenError{RetryAt: now.Add(time.Second), LastError: state.lastError}
		}
		state.halfOpen = true
		return true, nil
	}
	return false, nil
}

func (r *Runtime) state(identity string) *runtimeState {
	state := r.states[identity]
	if state == nil {
		state = &runtimeState{}
		r.states[identity] = state
	}
	return state
}

func (r *Runtime) finish(identity string, policy RuntimePolicy, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state(identity)
	state.requests++
	state.halfOpen = false
	if err == nil {
		state.consecutiveFailures = 0
		state.openUntil = time.Time{}
		state.lastSuccess = time.Now()
		state.lastError = ""
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	state.failures++
	state.consecutiveFailures++
	state.lastFailure = time.Now()
	state.lastError = clipRuntimeError(err.Error(), 300)
	if state.consecutiveFailures >= policy.FailureThreshold {
		state.openUntil = time.Now().Add(policy.Cooldown)
	}
}

func clipRuntimeError(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func (r *Runtime) cacheKey(identity, text string) string {
	sum := sha256.Sum256([]byte(identity + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) cached(identity, text string) ([]float32, bool) {
	key := r.cacheKey(identity, text)
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok || time.Now().After(entry.expires) {
		delete(r.cache, key)
		return nil, false
	}
	state := r.state(identity)
	state.hits++
	return append([]float32(nil), entry.vector...), true
}

func (r *Runtime) storeVector(identity, text string, vector []float32, policy RuntimePolicy) {
	if policy.CacheTTL <= 0 || len(vector) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= policy.CacheEntries {
		now := time.Now()
		for key, entry := range r.cache {
			if now.After(entry.expires) {
				delete(r.cache, key)
			}
		}
	}
	for len(r.cache) >= policy.CacheEntries {
		for key := range r.cache {
			delete(r.cache, key)
			break
		}
	}
	r.cache[r.cacheKey(identity, text)] = runtimeCacheEntry{
		identity: identity, vector: append([]float32(nil), vector...), expires: time.Now().Add(policy.CacheTTL),
	}
}

func (r *Runtime) RecordFallback(reason string) {
	if r == nil || reason == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallbacks[reason]++
}

func (r *Runtime) Snapshot(identity string) RuntimeSnapshot {
	snapshot := RuntimeSnapshot{Identity: identity, State: "idle", Fallbacks: map[string]int{}}
	if r == nil {
		return snapshot
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.states[identity]
	if state != nil {
		snapshot.ConsecutiveFailures = state.consecutiveFailures
		snapshot.RetryAt = state.openUntil
		snapshot.LastSuccess = state.lastSuccess
		snapshot.LastFailure = state.lastFailure
		snapshot.LastError = state.lastError
		snapshot.Requests = state.requests
		snapshot.Failures = state.failures
		snapshot.CacheHits = state.hits
		switch {
		case state.openUntil.After(time.Now()):
			snapshot.State = "open"
		case state.halfOpen:
			snapshot.State = "half-open"
		case state.requests > 0:
			snapshot.State = "closed"
		}
	}
	for _, entry := range r.cache {
		if entry.identity == identity && time.Now().Before(entry.expires) {
			snapshot.CacheEntries++
		}
	}
	for reason, count := range r.fallbacks {
		snapshot.Fallbacks[reason] = count
	}
	return snapshot
}

type runtimeProvider struct {
	runtime  *Runtime
	identity string
	policy   RuntimePolicy
	provider Provider
	probe    bool
}

func (p *runtimeProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if !p.probe && p.policy.CacheTTL > 0 {
		if vector, ok := p.runtime.cached(p.identity, text); ok {
			return vector, nil
		}
	}
	vector, err := p.provider.Embed(ctx, text)
	if err == nil && len(vector) == 0 {
		err = errors.New("embedding provider returned an empty vector")
	}
	p.runtime.finish(p.identity, p.policy, err)
	if err != nil {
		return nil, err
	}
	p.runtime.storeVector(p.identity, text, vector, p.policy)
	return vector, nil
}

func (p *runtimeProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	vectors := make([][]float32, len(texts))
	var missing []string
	var positions []int
	for index, text := range texts {
		if !p.probe && p.policy.CacheTTL > 0 {
			if vector, ok := p.runtime.cached(p.identity, text); ok {
				vectors[index] = vector
				continue
			}
		}
		missing = append(missing, text)
		positions = append(positions, index)
	}
	if len(missing) == 0 {
		return vectors, nil
	}
	var embedded [][]float32
	var err error
	if batch, ok := p.provider.(BatchEmbedder); ok {
		embedded, err = batch.EmbedBatch(ctx, missing)
	} else {
		embedded, err = EmbedAll(ctx, p.provider, missing)
	}
	if err == nil && len(embedded) != len(missing) {
		err = fmt.Errorf("embedding provider returned %d vectors for %d inputs", len(embedded), len(missing))
	}
	if err == nil {
		for _, vector := range embedded {
			if len(vector) == 0 {
				err = errors.New("embedding provider returned an empty vector")
				break
			}
		}
	}
	p.runtime.finish(p.identity, p.policy, err)
	if err != nil {
		return nil, err
	}
	for index, vector := range embedded {
		vectors[positions[index]] = vector
		p.runtime.storeVector(p.identity, missing[index], vector, p.policy)
	}
	return vectors, nil
}

func (p *runtimeProvider) EmbeddingMetadata() Metadata {
	if provider, ok := p.provider.(MetadataProvider); ok {
		return provider.EmbeddingMetadata()
	}
	return Metadata{}
}

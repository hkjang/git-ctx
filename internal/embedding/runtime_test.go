package embedding

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runtimeTestProvider struct {
	calls int
	err   error
}

func (p *runtimeTestProvider) Embed(context.Context, string) ([]float32, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return []float32{1, 0}, nil
}

func TestRuntimeCachesVectorsAndOpensCircuit(t *testing.T) {
	runtime := NewRuntime()
	policy := RuntimePolicy{FailureThreshold: 2, Cooldown: time.Minute, CacheTTL: time.Minute, CacheEntries: 10}
	okProvider := &runtimeTestProvider{}
	guarded, err := runtime.Guard("model-a", policy, func() (Provider, error) { return okProvider, nil })
	if err != nil {
		t.Fatal(err)
	}
	first, err := guarded.Embed(context.Background(), "same query")
	if err != nil {
		t.Fatal(err)
	}
	guarded, _ = runtime.Guard("model-a", policy, func() (Provider, error) { return okProvider, nil })
	second, err := guarded.Embed(context.Background(), "same query")
	if err != nil || okProvider.calls != 1 || len(first) != len(second) {
		t.Fatalf("calls=%d first=%v second=%v err=%v", okProvider.calls, first, second, err)
	}
	if snapshot := runtime.Snapshot("model-a"); snapshot.CacheHits != 1 || snapshot.CacheEntries != 1 {
		t.Fatalf("model-a cache snapshot=%#v", snapshot)
	}
	noCache := policy
	noCache.CacheTTL = 0
	guarded, _ = runtime.Guard("model-a", noCache, func() (Provider, error) { return okProvider, nil })
	if _, err = guarded.Embed(context.Background(), "same query"); err != nil || okProvider.calls != 2 {
		t.Fatalf("disabled cache calls=%d err=%v", okProvider.calls, err)
	}

	failing := &runtimeTestProvider{err: errors.New("endpoint down")}
	for attempt := 0; attempt < 2; attempt++ {
		guarded, err = runtime.Guard("model-b", policy, func() (Provider, error) { return failing, nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err = guarded.Embed(context.Background(), "query"); err == nil {
			t.Fatal("expected embedding failure")
		}
	}
	_, err = runtime.Guard("model-b", policy, func() (Provider, error) {
		t.Fatal("factory must not run while the circuit is open")
		return failing, nil
	})
	if err == nil || !strings.Contains(err.Error(), "circuit is open") {
		t.Fatalf("open error=%v", err)
	}
	snapshot := runtime.Snapshot("model-b")
	if snapshot.State != "open" || snapshot.Failures != 2 || snapshot.ConsecutiveFailures != 2 || snapshot.CacheEntries != 0 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

type coalescingTestProvider struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *coalescingTestProvider) Embed(context.Context, string) ([]float32, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.started) })
	<-p.release
	return []float32{0, 1}, nil
}

func TestRuntimeCoalescesConcurrentIdenticalQueries(t *testing.T) {
	manager := NewRuntime()
	policy := RuntimePolicy{FailureThreshold: 2, Cooldown: time.Minute, CacheTTL: time.Minute, CacheEntries: 10}
	provider := &coalescingTestProvider{started: make(chan struct{}), release: make(chan struct{})}
	const requests = 8
	ready := make(chan struct{}, requests)
	start := make(chan struct{})
	results := make(chan error, requests)
	for range requests {
		go func() {
			guarded, err := manager.Guard("model", policy, func() (Provider, error) { return provider, nil })
			if err != nil {
				results <- err
				return
			}
			ready <- struct{}{}
			<-start
			_, err = guarded.Embed(context.Background(), "same concurrent query")
			results <- err
		}()
	}
	for range requests {
		<-ready
	}
	close(start)
	<-provider.started
	deadline := time.Now().Add(time.Second)
	for manager.Snapshot("model").Coalesced != requests-1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(provider.release)
	for range requests {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	snapshot := manager.Snapshot("model")
	if provider.calls.Load() != 1 || snapshot.Requests != 1 || snapshot.Coalesced != requests-1 {
		t.Fatalf("provider calls=%d snapshot=%#v", provider.calls.Load(), snapshot)
	}
}

type cancelAwareCoalescingProvider struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *cancelAwareCoalescingProvider) Embed(ctx context.Context, _ string) ([]float32, error) {
	p.calls.Add(1)
	close(p.started)
	select {
	case <-p.release:
		return []float32{1, 1}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRuntimeLeaderCancellationDoesNotPoisonCoalescedWaiters(t *testing.T) {
	manager := NewRuntime()
	policy := RuntimePolicy{FailureThreshold: 2, Cooldown: time.Minute, CacheTTL: time.Minute, CacheEntries: 10}
	provider := &cancelAwareCoalescingProvider{started: make(chan struct{}), release: make(chan struct{})}

	leaderProvider, err := manager.Guard("model", policy, func() (Provider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, callErr := leaderProvider.Embed(leaderCtx, "shared query")
		leaderResult <- callErr
	}()
	<-provider.started

	followerProvider, err := manager.Guard("model", policy, func() (Provider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	followerResult := make(chan error, 1)
	go func() {
		_, callErr := followerProvider.Embed(context.Background(), "shared query")
		followerResult <- callErr
	}()
	deadline := time.Now().Add(time.Second)
	for manager.Snapshot("model").Coalesced != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancelLeader()
	if err = <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error=%v", err)
	}
	close(provider.release)
	if err = <-followerResult; err != nil {
		t.Fatalf("follower error=%v", err)
	}
	snapshot := manager.Snapshot("model")
	if provider.calls.Load() != 1 || snapshot.Requests != 1 || snapshot.Failures != 0 || snapshot.Coalesced != 1 {
		t.Fatalf("provider calls=%d snapshot=%#v", provider.calls.Load(), snapshot)
	}
}

package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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

// finish clips the recorded failure to 300 bytes before Snapshot publishes it as
// LastError, which the administration console serialises into its health JSON
// (internal/app/health.go). Diagnostics in this platform are written in Korean,
// so a clip that lands inside a character used to leave a half character behind:
// invalid UTF-8 that encoding/json then rewrites as a replacement character in
// the operator's copy of the endpoint's own message. The padding table places a
// three-byte character across the limit and clear of it, so the cases that never
// straddled the boundary act as an unchanged-behaviour control.
func TestRuntimeLastErrorClipsOnCharacterBoundaries(t *testing.T) {
	const limit = 300
	for _, pad := range []int{limit - 3, limit - 2, limit - 1, limit} {
		message := strings.Repeat("a", pad) + strings.Repeat("한", 10)
		manager := NewRuntime()
		_, err := manager.Guard("model", RuntimePolicy{}, func() (Provider, error) {
			return nil, errors.New(message)
		})
		if err == nil || err.Error() != message {
			t.Fatalf("pad=%d: guard error=%v", pad, err)
		}
		snapshot := manager.Snapshot("model")
		if !utf8.ValidString(snapshot.LastError) {
			t.Errorf("pad=%d: LastError is not valid UTF-8: %q", pad, snapshot.LastError)
		}
		if !strings.HasSuffix(snapshot.LastError, "…") {
			t.Errorf("pad=%d: LastError lost its ellipsis suffix: %q", pad, snapshot.LastError)
		}
		if head := strings.TrimSuffix(snapshot.LastError, "…"); !strings.HasPrefix(message, head) {
			t.Errorf("pad=%d: LastError is not a prefix of the original error: %q", pad, head)
		}
		// The whole point of clipping on a boundary is what the console shows, so
		// assert on the serialised form too. encoding/json renders an invalid byte
		// as the six-character escape backslash-u-f-f-f-d and never as the literal
		// replacement character, so searching for that character would pass
		// unconditionally. The needle is assembled rather than spelled out so it
		// cannot turn back into the literal character on its way into this file.
		encoded, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			t.Fatalf("pad=%d: marshal: %v", pad, marshalErr)
		}
		if replacement := `\u` + "fffd"; strings.Contains(string(encoded), replacement) {
			t.Errorf("pad=%d: the health JSON escapes a replacement character: %s", pad, encoded)
		}
	}
	// An error inside the limit is still recorded untouched.
	manager := NewRuntime()
	short := strings.Repeat("한", 10)
	if _, err := manager.Guard("short", RuntimePolicy{}, func() (Provider, error) {
		return nil, errors.New(short)
	}); err == nil {
		t.Fatal("expected the factory error to surface")
	}
	if got := manager.Snapshot("short").LastError; got != short {
		t.Errorf("LastError = %q, want the error recorded unchanged as %q", got, short)
	}
}

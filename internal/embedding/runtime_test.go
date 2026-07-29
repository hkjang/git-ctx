package embedding

import (
	"context"
	"errors"
	"strings"
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

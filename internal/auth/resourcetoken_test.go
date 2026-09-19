package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// The identity provider is slow or down. A /mcp token must not hold the
// verifier lock across discovery — the web sign-in verifiers share it — and
// the caller's deadline must be the one that ends the wait. A discovery
// that failed is not retried on every token, while a caller that merely
// gave up leaves nothing behind.
func TestResourceVerifierDiscoveryHonoursTheCallerAndCachesFailure(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var (
		issuer     string
		mode       atomic.Value // "hang", "fail" or "ok"
		discovered atomic.Int32
		inFlight   = make(chan struct{}, 8)
	)
	mode.Store("hang")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			discovered.Add(1)
			switch mode.Load() {
			case "hang":
				inFlight <- struct{}{}
				<-r.Context().Done()
				return
			case "fail":
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token",
				"jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "sso", Algorithm: "RS256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, &jose.SignerOptions{ExtraHeaders: map[jose.HeaderKey]any{"kid": "sso"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "kc-1", Audience: jwt.Audience{"https://git-ctx.company/mcp"}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now)}).Claims(map[string]any{"azp": "claude-mcp", "typ": "Bearer"}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewOIDCVerifier(func(context.Context) (OIDCConfig, error) {
		return OIDCConfig{IssuerURL: issuer, ClientID: "git-ctx", TimeoutSeconds: 30}, nil
	})
	verify := func(ctx context.Context) error {
		_, err := verifier.VerifyResourceToken(ctx, raw, "https://git-ctx.company/mcp", nil)
		return err
	}

	// A caller with a deadline gets its deadline, not the provider's silence,
	// and the lock is free for the sign-in path while discovery is hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- verify(ctx) }()
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery was never requested")
	}
	if !verifier.mu.TryLock() {
		t.Fatal("verifier lock is held while discovery is in flight")
	}
	verifier.mu.Unlock()
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("verification did not return after the caller's deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow discovery: err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("slow discovery: returned after %s, not at the caller's deadline", elapsed)
	}

	// The caller's own cancellation was not remembered as a provider failure:
	// the next caller tries again and, with the provider back, succeeds.
	mode.Store("ok")
	discovered.Store(0)
	if err := verify(context.Background()); err != nil {
		t.Fatalf("after the caller gave up: %v", err)
	}
	if discovered.Load() != 1 {
		t.Fatalf("discovery after a cancelled attempt ran %d times", discovered.Load())
	}
	// Cached: the next token consults nobody.
	if err := verify(context.Background()); err != nil || discovered.Load() != 1 {
		t.Fatalf("cached verifier: err=%v discoveries=%d", err, discovered.Load())
	}

	// A provider that answers with an error is remembered for a while: the
	// second token gets the same refusal without a second round trip.
	verifier.mu.Lock()
	verifier.resource, verifier.resourceExpires = nil, time.Time{}
	verifier.mu.Unlock()
	mode.Store("fail")
	discovered.Store(0)
	first := verify(context.Background())
	if first == nil {
		t.Fatal("discovery failure was not reported")
	}
	second := verify(context.Background())
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("second refusal=%v first=%v", second, first)
	}
	if discovered.Load() != 1 {
		t.Fatalf("failed discovery was retried: %d requests", discovered.Load())
	}
	// Once the memory of the failure lapses, discovery is tried again.
	verifier.mu.Lock()
	verifier.resourceErrExpires = time.Now().Add(-time.Second)
	verifier.mu.Unlock()
	mode.Store("ok")
	if err := verify(context.Background()); err != nil || discovered.Load() != 2 {
		t.Fatalf("after the failure lapsed: err=%v discoveries=%d", err, discovered.Load())
	}
}

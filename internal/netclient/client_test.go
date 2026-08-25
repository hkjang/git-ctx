package netclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPStatusErrorPreservesCauseMessageAndStatus(t *testing.T) {
	cause := errors.New("provider returned its existing message")
	statusErr := NewHTTPStatusError(http.StatusServiceUnavailable, cause)
	if got := statusErr.Error(); got != cause.Error() {
		t.Fatalf("Error() = %q, want %q", got, cause.Error())
	}
	if !errors.Is(statusErr, cause) {
		t.Fatal("HTTP status carrier lost its cause")
	}

	wrapped := fmt.Errorf("connection test: %w", statusErr)
	var carrier interface{ Status() int }
	if !errors.As(wrapped, &carrier) {
		t.Fatal("wrapped error no longer exposes an HTTP status")
	}
	if got := carrier.Status(); got != http.StatusServiceUnavailable {
		t.Fatalf("Status() = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestSecureDefaultsAndValidation(t *testing.T) {
	client, err := New(Config{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification disabled by default")
	}
	if transport.TLSClientConfig.MinVersion < 0x0303 {
		t.Fatal("TLS below 1.2")
	}
	if _, err = New(Config{CACertificate: "not pem"}); err == nil {
		t.Fatal("invalid CA accepted")
	}
	if _, err = New(Config{ProxyURL: "://bad"}); err == nil {
		t.Fatal("invalid proxy accepted")
	}
}

// Every integration talks to a single host while search fans out across
// repositories, so the per-host idle pool decides whether that fan-out reuses
// connections or pays a fresh handshake for most of each batch. Go's default of
// two left 83 of these 100 requests opening a new connection.
func TestClientReusesConnectionsAcrossFanOut(t *testing.T) {
	var opened atomic.Int64
	// ConnState has to be installed before Start; setting it on a running
	// server races with the accept loop.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	client, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := client.Transport.(*http.Transport).MaxIdleConnsPerHost; got != idleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", got, idleConnsPerHost)
	}

	const waves, perWave = 5, 20
	for wave := 0; wave < waves; wave++ {
		var wait sync.WaitGroup
		for i := 0; i < perWave; i++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				resp, err := client.Get(server.URL)
				if err != nil {
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}()
		}
		wait.Wait()
	}

	// Later waves should ride on the pool rather than dialling again. Allow
	// headroom for scheduling, but not for a wave's worth of fresh connections.
	if limit := int64(perWave * 2); opened.Load() > limit {
		t.Errorf("%d connections opened for %d requests, want at most %d",
			opened.Load(), waves*perWave, limit)
	}
}

// The Go type a decoder was working with is meaningless to the person reading
// an MCP answer or an operations screen, and it is what encoding/json puts in
// its error. Every integration reports the failure in terms of the server.
func TestDecodeFailureNamesTheServerNotTheGoType(t *testing.T) {
	var list []struct{ ID string }
	err := json.Unmarshal([]byte(`{"message":"sign in"}`), &list)
	if err == nil {
		t.Fatal("the fixture must fail to decode")
	}
	message := DecodeFailure("GitLab", "GitLab API", "/projects/1/repository/commits", err).Error()
	for _, expected := range []string{"GitLab", "/projects/1/repository/commits", "object where a list was expected", "proxy or login page"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("missing %q in %q", expected, message)
		}
	}
	if strings.Contains(message, "Go value of type") || strings.Contains(message, "struct {") {
		t.Fatalf("the Go type leaked: %q", message)
	}

	// A body that is not JSON at all — a login page, a gateway error — is
	// reported as such rather than as a type mismatch.
	syntax := json.Unmarshal([]byte(`<html>sign in</html>`), &list)
	plain := DecodeFailure("Bitbucket", "Bitbucket REST API", "/rest/api/1.0/projects", syntax).Error()
	if !strings.Contains(plain, "not valid JSON") {
		t.Fatalf("a non-JSON body must be described as one: %q", plain)
	}
}

// Every OpenAI-compatible provider documents its base URL as ending in /v1, so
// that is what an operator pastes in. Appending the version again produced a
// 404 that reached the operations screen as "the endpoint is unavailable" —
// indistinguishable from an outage, with the doubled path never shown.
func TestJoinAPIPathDoesNotRepeatWhatTheOperatorTyped(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"https://ai.internal", "/v1/embeddings", "https://ai.internal/v1/embeddings"},
		{"https://ai.internal/", "/v1/embeddings", "https://ai.internal/v1/embeddings"},
		{"https://ai.internal/v1", "/v1/embeddings", "https://ai.internal/v1/embeddings"},
		{"https://ai.internal/v1/", "/v1/embeddings", "https://ai.internal/v1/embeddings"},
		{"https://ai.internal/v1/embeddings", "/v1/embeddings", "https://ai.internal/v1/embeddings"},
		{"https://ai.internal/openai/v1", "/v1/rerank", "https://ai.internal/openai/v1/rerank"},
		// A path that merely contains the segment elsewhere is left alone.
		{"https://v1.ai.internal/models", "/v1/embeddings", "https://v1.ai.internal/models/v1/embeddings"},
	}
	for _, item := range cases {
		if got := JoinAPIPath(item.base, item.path); got != item.want {
			t.Errorf("JoinAPIPath(%q, %q) = %q, want %q", item.base, item.path, got, item.want)
		}
	}
}

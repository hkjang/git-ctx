package netclient

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

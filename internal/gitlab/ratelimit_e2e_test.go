package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// A rate-limited GitLab replies with RateLimit-Reset as a Unix time. The client
// must wait roughly that long, not the flat cap it used to derive from it.
func TestRateLimitedRequestRetriesWithinTheNamedWindow(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// GitLab sends the reset moment, not a delay, and omits Retry-After
			// on responses that are not a 429 for the user quota.
			w.Header().Set("RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Second).Unix(), 10))
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "t"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if _, err := client.ListProjects(context.Background()); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	elapsed := time.Since(start)

	if calls < 2 {
		t.Fatalf("the request was not retried (calls=%d)", calls)
	}
	t.Logf("재시도까지 %v 대기 (호출 %d회)", elapsed.Round(100*time.Millisecond), calls)
	if elapsed > 5*time.Second {
		t.Errorf("waited %s for a one second window", elapsed)
	}
}

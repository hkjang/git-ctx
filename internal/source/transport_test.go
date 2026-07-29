package source

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// The retry policy has to distinguish the three cases that look alike in an
// untyped error: slow down, try again later, and stop asking.
func TestErrorClassificationAndRetryDelay(t *testing.T) {
	cases := map[int]struct{ retry, auth, notFound bool }{
		http.StatusTooManyRequests:     {retry: true},
		http.StatusBadGateway:          {retry: true},
		http.StatusServiceUnavailable:  {retry: true},
		http.StatusUnauthorized:        {auth: true},
		http.StatusForbidden:           {auth: true},
		http.StatusNotFound:            {notFound: true},
		http.StatusUnprocessableEntity: {},
	}
	for status, expected := range cases {
		err := error(&APIError{Source: "gitlab", StatusCode: status, Status: http.StatusText(status)})
		if RetryableStatus(status) != expected.retry {
			t.Fatalf("status %d retryable=%v", status, !expected.retry)
		}
		if IsAuthFailure(err) != expected.auth {
			t.Fatalf("status %d auth=%v", status, !expected.auth)
		}
		if IsNotFound(err) != expected.notFound {
			t.Fatalf("status %d notFound=%v", status, !expected.notFound)
		}
		if StatusOf(err) != status {
			t.Fatalf("StatusOf=%d", StatusOf(err))
		}
	}
	if StatusOf(errors.New("plain")) != 0 {
		t.Fatal("a plain error has no status")
	}

	// The server's own window wins over the local backoff, and an absurd window
	// is capped rather than holding a tool call open.
	response := &http.Response{Header: http.Header{"Retry-After": []string{"7"}}}
	if delay := RetryDelay(response, 0); delay != 7*time.Second {
		t.Fatalf("Retry-After ignored: %s", delay)
	}
	long := &http.Response{Header: http.Header{"Retry-After": []string{"600"}}}
	if delay := RetryDelay(long, 0); delay != 30*time.Second {
		t.Fatalf("long window not capped: %s", delay)
	}
	if first, second := RetryDelay(nil, 0), RetryDelay(nil, 2); second <= first {
		t.Fatalf("backoff must grow: %s then %s", first, second)
	}
}

// A failing source must be skipped quickly and recover on its own, and normal
// scan outcomes must never disable a healthy one.
func TestBreakerOpensRecoversAndIgnoresExpectedOutcomes(t *testing.T) {
	now := time.Now()
	breaker := &Breaker{Threshold: 3, OpenDuration: time.Minute, Now: func() time.Time { return now }}
	for index := 0; index < 2; index++ {
		breaker.Failure(errors.New("connection refused"))
	}
	if allowed, _ := breaker.Allow(); !allowed {
		t.Fatal("the breaker opened before the threshold")
	}
	if state := breaker.State("gitlab"); state.State != "degraded" || state.Failures != 2 {
		t.Fatalf("state=%#v", state)
	}
	breaker.Failure(errors.New("connection refused"))
	allowed, reason := breaker.Allow()
	if allowed || reason == "" {
		t.Fatalf("the breaker must be open with a reason: %v %q", allowed, reason)
	}

	// A deleted repository or a cancelled call is not an outage.
	fresh := &Breaker{Threshold: 2}
	fresh.Failure(&APIError{StatusCode: http.StatusNotFound, Status: "404"})
	fresh.Failure(context.Canceled)
	if state := fresh.State("bitbucket"); state.Failures != 0 || state.State != "closed" {
		t.Fatalf("expected outcomes must not count: %#v", state)
	}

	// An expired token will not fix itself, so it opens immediately.
	credential := &Breaker{Threshold: 5}
	credential.Failure(&APIError{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized"})
	if allowed, _ := credential.Allow(); allowed {
		t.Fatal("an authentication failure must open the breaker at once")
	}

	// After the window one probe is allowed, and only one.
	now = now.Add(2 * time.Minute)
	if allowed, _ := breaker.Allow(); !allowed {
		t.Fatal("the breaker must half-open after its window")
	}
	if allowed, _ := breaker.Allow(); allowed {
		t.Fatal("only one probe may run at a time")
	}
	breaker.Success()
	if allowed, _ := breaker.Allow(); !allowed {
		t.Fatal("a successful probe must close the breaker")
	}
	if state := breaker.State("gitlab"); state.State != "closed" || state.Failures != 0 {
		t.Fatalf("state after recovery=%#v", state)
	}
}

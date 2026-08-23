package source

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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

// A granted probe that never reports back must not disable the source forever.
// The search layer has paths that load an adapter and then return without making
// a call (an adapter that does not implement the searcher interface, a deadline
// that expired), and before this the breaker stayed half-open for good: every
// later call was refused with "복구를 확인하는 중" and the integration never came
// back without an administrator resetting it.
func TestAbandonedProbeDoesNotPauseSourceForever(t *testing.T) {
	now := time.Now()
	breaker := &Breaker{Threshold: 1, OpenDuration: time.Minute, Now: func() time.Time { return now }}
	breaker.Failure(errors.New("connection refused"))
	if allowed, _ := breaker.Allow(); allowed {
		t.Fatal("the breaker must be open right after the failure")
	}
	// The window passes and one probe is granted, but the caller never reports.
	now = now.Add(2 * time.Minute)
	if allowed, _ := breaker.Allow(); !allowed {
		t.Fatal("a probe must be granted after the window")
	}
	if allowed, _ := breaker.Allow(); allowed {
		t.Fatal("a second concurrent probe must be refused")
	}
	// After the probe deadline the source is tried again instead of staying stuck.
	now = now.Add(probeTimeout + time.Second)
	allowed, reason := breaker.Allow()
	if !allowed {
		t.Fatalf("an abandoned probe must expire: %q", reason)
	}
}

// GitLab documents RateLimit-Reset as a Unix time and RateLimit-ResetTime as an
// HTTP date. Reading the epoch as a delay produced a flat 30 second wait -- the
// default tool timeout -- and the date header was not read at all, so the
// client retried after 250ms and was rate limited again.
func TestRetryDelayReadsRateLimitResetHeaders(t *testing.T) {
	const window = 12 * time.Second
	deadline := time.Now().Add(window)

	cases := []struct {
		name   string
		header http.Header
	}{
		{"RateLimit-Reset as Unix time", http.Header{
			"Ratelimit-Reset": []string{strconv.FormatInt(deadline.Unix(), 10)}}},
		{"RateLimit-ResetTime as HTTP date", http.Header{
			"Ratelimit-Resettime": []string{deadline.UTC().Format(http.TimeFormat)}}},
		{"Retry-After as HTTP date", http.Header{
			"Retry-After": []string{deadline.UTC().Format(http.TimeFormat)}}},
	}
	for _, c := range cases {
		delay := RetryDelay(&http.Response{Header: c.header}, 0)
		if delay < window-2*time.Second || delay > window {
			t.Errorf("%s: delay = %s, want about %s", c.name, delay, window)
		}
	}

	// A delta-form RateLimit-Reset, as the IETF draft defines it, still reads as
	// a delay rather than a timestamp.
	delta := RetryDelay(&http.Response{Header: http.Header{"Ratelimit-Reset": []string{"12"}}}, 0)
	if delta != window {
		t.Errorf("delta-form RateLimit-Reset: delay = %s, want %s", delta, window)
	}

	// Retry-After is the standard header and wins when the server sends both.
	both := RetryDelay(&http.Response{Header: http.Header{
		"Retry-After":     []string{"3"},
		"Ratelimit-Reset": []string{strconv.FormatInt(deadline.Unix(), 10)},
	}}, 0)
	if both != 3*time.Second {
		t.Errorf("Retry-After did not win: %s", both)
	}

	// A window that already elapsed falls back to the local backoff.
	past := RetryDelay(&http.Response{Header: http.Header{
		"Ratelimit-Reset": []string{strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)}}}, 0)
	if past != 250*time.Millisecond {
		t.Errorf("elapsed window: delay = %s, want the backoff", past)
	}

	// A window longer than one call should hold open is still capped.
	capped := RetryDelay(&http.Response{Header: http.Header{
		"Ratelimit-Reset": []string{strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}}}, 0)
	if capped != maxRetryHint {
		t.Errorf("long window not capped: %s", capped)
	}
}

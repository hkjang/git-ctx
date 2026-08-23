package netclient

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxRetryHint caps how long a server-named window may hold one call open. A
// longer wait is better spent failing the call than blocking the caller.
const MaxRetryHint = 30 * time.Second

// epochSeconds separates an absolute Unix time from a delay expressed in
// seconds. Nothing sensible asks a client to wait 31 years, so a bare number
// above this is a timestamp.
const epochSeconds = 1_000_000_000

// RetryDelay is how long to wait before the next attempt. A window the server
// named wins over the local backoff, because guessing shorter than the window
// only gets the client rate limited again.
func RetryDelay(response *http.Response, attempt int) time.Duration {
	backoff := time.Duration(math.Pow(2, float64(attempt))) * 250 * time.Millisecond
	if backoff > 5*time.Second {
		backoff = 5 * time.Second
	}
	if response == nil {
		return backoff
	}
	hint, ok := resetHint(response.Header, time.Now())
	if !ok || hint <= backoff {
		return backoff
	}
	if hint > MaxRetryHint {
		return MaxRetryHint
	}
	return hint
}

// resetHint reads how long the server wants the client to wait.
//
// Retry-After is delta-seconds or an HTTP date (RFC 9110). GitLab additionally
// sends RateLimit-Reset, which it documents as a Unix time rather than the
// delta the IETF draft uses, and RateLimit-ResetTime as an HTTP date. Reading
// an epoch as a delay produced a fixed 30 second wait -- the same as the
// default tool timeout -- for what is usually a few seconds, so a bare number
// is read as a timestamp when it is far too large to be a delay.
func resetHint(header http.Header, now time.Time) (time.Duration, bool) {
	if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second, true
		}
		if when, err := http.ParseTime(value); err == nil {
			return remaining(now, when), true
		}
	}
	if value := strings.TrimSpace(header.Get("RateLimit-Reset")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			if seconds > epochSeconds {
				return remaining(now, time.Unix(seconds, 0)), true
			}
			return time.Duration(seconds) * time.Second, true
		}
	}
	if value := strings.TrimSpace(header.Get("RateLimit-ResetTime")); value != "" {
		if when, err := http.ParseTime(value); err == nil {
			return remaining(now, when), true
		}
	}
	return 0, false
}

// remaining is the wait until a deadline, never negative for one already past.
func remaining(now, when time.Time) time.Duration {
	if wait := when.Sub(now); wait > 0 {
		return wait
	}
	return 0
}

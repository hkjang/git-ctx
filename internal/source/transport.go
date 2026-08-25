package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"git-ctx/internal/netclient"
)

// ErrNotConfigured reports that a source type has no saved configuration. It is
// not an integration failure: a platform that only uses GitLab must not see
// Bitbucket reported as a broken connector.
var ErrNotConfigured = errors.New("source is not configured")

// APIError is a non 2xx response from a source server. The status matters to
// the caller: an expired token has to reach an administrator, a missing
// repository is skipped silently, and a rate limit is retried. Without a typed
// error every one of those looked the same to the search layer, which then
// retried a dead credential across three hundred repositories.
type APIError struct {
	Source     string
	StatusCode int
	Status     string
	Body       string
	// RetryAfter is the delay the server asked for, when it sent one.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API %s: %s", e.Source, e.Status, e.Body)
}

// StatusOf returns the HTTP status carried by an error, or 0.
func StatusOf(err error) int {
	var api *APIError
	if errors.As(err, &api) {
		return api.StatusCode
	}
	// Source adapters and the shared HTTP client expose the same narrow method,
	// so wrapped non-2xx errors retain their status without package coupling.
	var coded interface{ Status() int }
	if errors.As(err, &coded) {
		return coded.Status()
	}
	return 0
}

// IsAuthFailure reports a credential problem: the token is missing, expired or
// lacks the scope. Retrying makes it worse, and only an administrator can fix
// it, so the caller should stop and say so.
func IsAuthFailure(err error) bool {
	status := StatusOf(err)
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// IsNotFound reports that the repository, ref or path is gone. It is a normal
// outcome during a scan and must not be treated as an outage.
func IsNotFound(err error) bool { return StatusOf(err) == http.StatusNotFound }

// IsRateLimited reports that the server asked the client to slow down.
func IsRateLimited(err error) bool { return StatusOf(err) == http.StatusTooManyRequests }

// RetryableStatus reports whether a status is worth another attempt. 429 and
// the gateway family are transient by definition; everything else is the
// server's considered answer.
func RetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// RetryDelay is how long to wait before the next attempt. It delegates to the
// shared HTTP layer so every integration reads a server's window the same way.
func RetryDelay(response *http.Response, attempt int) time.Duration {
	return netclient.RetryDelay(response, attempt)
}

// MaxAttempts bounds one request including the first try. Three attempts covers
// a restarting node or a brief rate limit without turning a broken instance
// into a long stall.
const MaxAttempts = 3

// Sleep waits for the delay or the context, whichever comes first.
func Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Breaker stops calling a source server that is failing.
//
// Without it a dead GitLab costs every MCP call its whole timeout: the search
// fans out over the repository list and each one waits for its own connection
// error. The agent then gets nothing after ninety seconds instead of an indexed
// answer after one. The breaker turns that into an immediate, explained
// degradation — and it closes again on its own, because an on-premises source
// server usually comes back.
type Breaker struct {
	mu           sync.Mutex
	failures     int
	openedAt     time.Time
	retryAt      time.Time
	lastError    string
	probing      bool
	probeAt      time.Time
	Threshold    int           // consecutive failures before opening
	OpenDuration time.Duration // how long to stay open before a probe
	Now          func() time.Time
}

// BreakerState describes a breaker for the administration screen.
type BreakerState struct {
	Source    string    `json:"source"`
	State     string    `json:"state"`
	Failures  int       `json:"failures"`
	OpenedAt  time.Time `json:"openedAt,omitempty"`
	RetryAt   time.Time `json:"retryAt,omitempty"`
	LastError string    `json:"lastError,omitempty"`
}

const (
	defaultBreakerThreshold = 4
	defaultBreakerOpen      = 30 * time.Second
	// probeTimeout bounds how long one recovery probe may stay outstanding.
	// A caller can load an adapter and return without making a call, in which
	// case no outcome is ever reported; without this the breaker would stay
	// half-open forever and the source would never be tried again.
	probeTimeout = 60 * time.Second
)

func (b *Breaker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Breaker) threshold() int {
	if b.Threshold > 0 {
		return b.Threshold
	}
	return defaultBreakerThreshold
}

func (b *Breaker) openDuration() time.Duration {
	if b.OpenDuration > 0 {
		return b.OpenDuration
	}
	return defaultBreakerOpen
}

// Allow reports whether a call may proceed. When it refuses it also returns the
// reason, which is written into the search diagnostics so the caller learns
// that the source was skipped rather than empty.
func (b *Breaker) Allow() (bool, string) {
	if b == nil {
		return true, ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openedAt.IsZero() {
		return true, ""
	}
	now := b.now()
	if now.Before(b.retryAt) {
		return false, fmt.Sprintf("연동이 일시 중단되었습니다(연속 실패 %d회: %s). %.0f초 후 자동으로 다시 시도합니다.",
			b.failures, b.lastError, b.retryAt.Sub(now).Seconds())
	}
	// One probe at a time: a half-open breaker that lets a whole fan-out through
	// would hammer a server that is still down. An outstanding probe expires so a
	// caller that never reported an outcome cannot pause the source for good.
	if b.probing && now.Sub(b.probeAt) < probeTimeout {
		return false, "연동 복구를 확인하는 중입니다. 이번 요청은 색인된 결과만 사용합니다."
	}
	b.probing, b.probeAt = true, now
	return true, ""
}

// Success closes the breaker.
func (b *Breaker) Success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures, b.openedAt, b.retryAt, b.lastError, b.probing, b.probeAt = 0, time.Time{}, time.Time{}, "", false, time.Time{}
}

// Failure records a failed call. Outcomes that are not the server's fault — a
// missing repository, a cancelled request — do not count towards opening,
// otherwise a scan over deleted repositories would disable a healthy source.
func (b *Breaker) Failure(err error) {
	if b == nil || err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || IsNotFound(err) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	b.failures++
	b.lastError = truncateError(err.Error())
	// A credential failure will not fix itself by waiting, so it opens the
	// breaker immediately instead of after four wasted round trips.
	if b.failures >= b.threshold() || IsAuthFailure(err) {
		now := b.now()
		if b.openedAt.IsZero() {
			b.openedAt = now
		}
		b.retryAt = now.Add(b.openDuration())
	}
}

// Reset clears the breaker, for the administrator's "try again now" action.
func (b *Breaker) Reset() { b.Success() }

// State reports the current state for the administration screen.
func (b *Breaker) State(name string) BreakerState {
	if b == nil {
		return BreakerState{Source: name, State: "closed"}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := BreakerState{Source: name, Failures: b.failures, LastError: b.lastError, State: "closed"}
	if !b.openedAt.IsZero() {
		state.OpenedAt, state.RetryAt = b.openedAt, b.retryAt
		state.State = "open"
		if !b.now().Before(b.retryAt) {
			state.State = "half-open"
		}
	} else if b.failures > 0 {
		state.State = "degraded"
	}
	return state
}

func truncateError(value string) string {
	const limit = 200
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && value[cut]&0xC0 == 0x80 {
		cut--
	}
	return value[:cut] + "…"
}

// Breakers is the per source registry. One breaker per source type is the right
// granularity: the credential and the endpoint are shared by every repository
// of that source.
type Breakers struct {
	mu       sync.Mutex
	breakers map[string]*Breaker
}

func NewBreakers() *Breakers { return &Breakers{breakers: map[string]*Breaker{}} }

func (r *Breakers) Get(sourceType string) *Breaker {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.breakers == nil {
		r.breakers = map[string]*Breaker{}
	}
	breaker, ok := r.breakers[sourceType]
	if !ok {
		breaker = &Breaker{}
		r.breakers[sourceType] = breaker
	}
	return breaker
}

// States returns every known breaker, newest problems first.
func (r *Breakers) States() []BreakerState {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	names := make([]string, 0, len(r.breakers))
	for name := range r.breakers {
		names = append(names, name)
	}
	r.mu.Unlock()
	out := make([]BreakerState, 0, len(names))
	for _, name := range names {
		out = append(out, r.Get(name).State(name))
	}
	return out
}

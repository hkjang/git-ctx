// Package calltrace records what happened inside one MCP call.
//
// The aggregate statistics say a tool returned nothing; they cannot say where
// the nothing came from. A search walks several stages — resolve the caller's
// ACL, query the index, fall back to the source server per repository, rerank,
// format — and each of them can be the one that dropped the result. Without a
// per-stage record an operator is left guessing between "the code is not
// indexed", "the ACL hid it" and "the source server timed out".
//
// The recorder is deliberately nil-safe: every method works on a nil *Recorder,
// so a code path can be instrumented once and still run untraced, and no
// component has to branch on whether tracing is on.
package calltrace

import (
	"context"
	"sync"
	"time"
)

// Status values are fixed so the screen can colour and filter them.
const (
	StatusOK      = "ok"
	StatusEmpty   = "empty"
	StatusSkipped = "skipped"
	StatusError   = "error"
	StatusTimeout = "timeout"
)

// Step is one stage of a call.
type Step struct {
	Sequence   int    `json:"sequence"`
	Stage      string `json:"stage"`
	Target     string `json:"target"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	Candidates int    `json:"candidates"`
	Results    int    `json:"results"`
	DurationMS int64  `json:"durationMs"`
	OffsetMS   int64  `json:"offsetMs"`
}

// maxSteps bounds one trace. A pathological fan-out over hundreds of
// repositories must not turn one call into hundreds of audit rows.
const maxSteps = 60

// Recorder collects the steps of one call. It is safe for concurrent use
// because the search service fans out over sources in parallel.
type Recorder struct {
	mu      sync.Mutex
	start   time.Time
	steps   []Step
	dropped int
}

type contextKey struct{}

// New attaches a recorder to the context and returns both.
func New(ctx context.Context) (context.Context, *Recorder) {
	recorder := &Recorder{start: time.Now()}
	return context.WithValue(ctx, contextKey{}, recorder), recorder
}

// From returns the recorder of this context, or nil when tracing is off.
func From(ctx context.Context) *Recorder {
	recorder, _ := ctx.Value(contextKey{}).(*Recorder)
	return recorder
}

// Span is an open step. Calling End records it.
type Span struct {
	recorder *Recorder
	stage    string
	target   string
	started  time.Time
}

// Start opens a step. A nil recorder returns a span that records nothing.
func (r *Recorder) Start(stage, target string) *Span {
	if r == nil {
		return nil
	}
	return &Span{recorder: r, stage: stage, target: target, started: time.Now()}
}

// Start opens a step on the recorder of this context, if there is one.
func Start(ctx context.Context, stage, target string) *Span {
	return From(ctx).Start(stage, target)
}

// End records the span with its outcome. candidates is what the stage looked
// at, results is what it passed on: the gap between the two is exactly where a
// result was lost.
func (s *Span) End(status string, candidates, results int, detail string) {
	if s == nil {
		return
	}
	s.recorder.add(Step{
		Stage: s.stage, Target: s.target, Status: status, Detail: detail,
		Candidates: candidates, Results: results,
		DurationMS: time.Since(s.started).Milliseconds(),
		OffsetMS:   s.started.Sub(s.recorder.start).Milliseconds(),
	})
}

// Fail records a span that ended in an error.
func (s *Span) Fail(err error) {
	if s == nil {
		return
	}
	status := StatusError
	if err == context.DeadlineExceeded {
		status = StatusTimeout
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	s.End(status, 0, 0, detail)
}

// Note records a stage that has no duration of its own, such as a decision.
func (r *Recorder) Note(stage, target, status, detail string) {
	if r == nil {
		return
	}
	r.add(Step{Stage: stage, Target: target, Status: status, Detail: detail,
		OffsetMS: time.Since(r.start).Milliseconds()})
}

// Count records a decision that did move a number of results, such as an ACL
// filter or a limit. Without the counts such a step reads as "0 → 0" in the
// waterfall, which looks like the stage lost everything when it did not.
func (r *Recorder) Count(stage, target, status string, candidates, results int, detail string) {
	if r == nil {
		return
	}
	r.add(Step{Stage: stage, Target: target, Status: status, Detail: detail,
		Candidates: candidates, Results: results, OffsetMS: time.Since(r.start).Milliseconds()})
}

func (r *Recorder) add(step Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) >= maxSteps {
		r.dropped++
		return
	}
	step.Sequence = len(r.steps) + 1
	r.steps = append(r.steps, step)
}

// Steps returns the recorded steps in order, with a final marker when the trace
// had to drop some. Silently truncating would make a partial trace look
// complete, which is worse than no trace.
func (r *Recorder) Steps() []Step {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Step, len(r.steps))
	copy(out, r.steps)
	if r.dropped > 0 {
		out = append(out, Step{
			Sequence: len(out) + 1, Stage: "trace", Status: StatusSkipped,
			Detail:   "step limit reached; further stages were not recorded",
			Results:  r.dropped,
			OffsetMS: time.Since(r.start).Milliseconds(),
		})
	}
	return out
}

// Summary reports the first stage that lost the results, which is the sentence
// an operator wants before reading the whole waterfall.
func (r *Recorder) Summary() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	label := func(step Step) string {
		if step.Target == "" {
			return step.Stage
		}
		return step.Stage + " " + step.Target
	}
	for _, step := range r.steps {
		switch step.Status {
		case StatusError, StatusTimeout:
			return label(step) + ": " + step.Status
		}
	}
	for _, step := range r.steps {
		if step.Candidates > 0 && step.Results == 0 {
			return label(step) + ": " + itoa(step.Candidates) + " candidates, none passed"
		}
	}
	// Nothing was dropped because nothing arrived. Naming the first stage that
	// came up empty still points at the cause: an ACL stage with no candidates is
	// a permission problem, an index stage with none is an indexing one.
	for _, step := range r.steps {
		if step.Status == StatusEmpty {
			return label(step) + ": nothing to work with from here on"
		}
	}
	return ""
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

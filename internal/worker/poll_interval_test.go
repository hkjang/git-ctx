package worker

import (
	"testing"
	"time"

	"git-ctx/internal/indexer"
)

// TestAnUnconfiguredWorkerKeepsTheProductionPollInterval guards the reason the
// interval is injectable at all. Tests ask for a short one so a fixture does
// not idle through a whole poll; an installation that asks for nothing has to
// keep polling at the production rate, because every tick is two queries
// against the shared job queue in every replica and the work behind a claimed
// job is a call to an on-premise source server.
func TestAnUnconfiguredWorkerKeepsTheProductionPollInterval(t *testing.T) {
	w := New(nil, indexer.New(nil, indexer.DefaultPolicy()), nil)
	if w.poll != 2*time.Second {
		t.Fatalf("a new worker polls every %s, want 2s", w.poll)
	}
	if DefaultPollInterval != 2*time.Second {
		t.Fatalf("DefaultPollInterval is %s, want 2s", DefaultPollInterval)
	}
	// A caller that has nothing configured must not turn the loop into a busy
	// wait: app.startBackground hands over whatever the configuration holds,
	// which is the zero value in production.
	for _, nothing := range []time.Duration{0, -time.Second} {
		w.SetPollInterval(nothing)
		if w.poll != DefaultPollInterval {
			t.Fatalf("SetPollInterval(%s) left the interval at %s, want the default %s", nothing, w.poll, DefaultPollInterval)
		}
	}
	w.SetPollInterval(25 * time.Millisecond)
	if w.poll != 25*time.Millisecond {
		t.Fatalf("SetPollInterval(25ms) left the interval at %s", w.poll)
	}
}

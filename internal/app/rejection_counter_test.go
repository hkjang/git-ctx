package app

import (
	"fmt"
	"testing"
	"time"
)

// Counting rejected credentials must not cost the server more than the refusal
// itself. The first version of this counter kept every attempt time per
// address, so a flood grew its own cost: fifty thousand rejections from one
// address spent three and a half seconds of CPU inside the lock, and half a
// million spread across addresses spent eighteen. That hands an
// unauthenticated caller a way to spend the server's time simply by being
// wrong repeatedly, which is a poor trade for an audit row.
//
// The ceilings below are deliberately loose — this is a shape test, not a
// benchmark. The implementations it exists to reject miss them by two orders of
// magnitude, and a loaded machine does not.
func TestCountingRejectionsCostsNoMoreThanTheRefusal(t *testing.T) {
	const attempts = 500_000
	const ceiling = 10 * time.Second

	t.Run("one address repeating", func(t *testing.T) {
		var counter rejectionCounter
		start := time.Now()
		for i := 0; i < attempts; i++ {
			counter.count("10.0.0.1", time.Hour)
		}
		if elapsed := time.Since(start); elapsed > ceiling {
			t.Errorf("%d rejections from one address took %v; the cost is growing with the flood", attempts, elapsed)
		}
		got, _, _ := counter.count("10.0.0.1", time.Hour)
		if got != attempts+1 {
			t.Errorf("the running count is %d after %d attempts", got, attempts+1)
		}
	})

	t.Run("many addresses", func(t *testing.T) {
		var counter rejectionCounter
		start := time.Now()
		for i := 0; i < attempts; i++ {
			counter.count(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255), time.Hour)
		}
		if elapsed := time.Since(start); elapsed > ceiling {
			t.Errorf("%d rejections from distinct addresses took %v; the sweep is not amortised", attempts, elapsed)
		}
		// Memory has to be bounded by what this instance chose to track, not by
		// how many addresses the caller can reach it from.
		counter.mu.Lock()
		held := len(counter.windows)
		counter.mu.Unlock()
		if held > trackedAddresses+1 {
			t.Errorf("the counter holds %d addresses; the bound is %d", held, trackedAddresses)
		}
	})
}

// Past the bound, an address is counted with the others rather than dropped: a
// probe spread wider than this instance tracks is the event most worth
// reporting, and reporting it must not cost memory in proportion to the
// attacker's fleet.
func TestAddressesPastTheBoundAreCountedTogether(t *testing.T) {
	var counter rejectionCounter
	for i := 0; i < trackedAddresses; i++ {
		address := fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255)
		if _, _, tracked := counter.count(address, time.Hour); tracked != address {
			t.Fatalf("address %d of %d was folded early, into %q", i, trackedAddresses, tracked)
		}
	}
	attempts, notable, tracked := counter.count("203.0.113.9", time.Hour)
	if tracked != sharedRejectionKey {
		t.Errorf("an address past the bound was tracked on its own as %q", tracked)
	}
	if attempts != 1 || !notable {
		t.Errorf("the first refusal folded into the shared entry was not reported: attempts=%d notable=%v", attempts, notable)
	}
}

// A window that has passed starts again rather than accumulating for the life
// of the process.
func TestTheCountRestartsWithTheWindow(t *testing.T) {
	var counter rejectionCounter
	for i := 0; i < 5; i++ {
		counter.count("10.0.0.1", time.Hour)
	}
	counter.mu.Lock()
	counter.windows["10.0.0.1"].start = time.Now().Add(-2 * time.Hour)
	counter.mu.Unlock()
	if attempts, notable, _ := counter.count("10.0.0.1", time.Hour); attempts != 1 || !notable {
		t.Errorf("a new window did not start: attempts=%d notable=%v", attempts, notable)
	}
}

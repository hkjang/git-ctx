package webhook

import (
	"context"
	"path/filepath"
	"testing"

	"git-ctx/internal/store"
)

// Identical payloads for one repository and event type are deduplicated on
// purpose: a source server retries a hook it believes timed out, and indexing
// the same push twice is waste.
//
// The duplicate used to be dropped without a trace — no row, no counter, no
// status — so an operator asking why a push never reached the index found a
// screen showing that nothing had arrived. The rejection path records what it
// refuses for exactly that reason; this is the same for repeats.
func TestARepeatedEventIsCountedRatherThanForgotten(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "hooks.db")+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if _, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','gitlab','4242','/gitlab~core/api','main',1)`); err != nil {
		t.Fatal(err)
	}
	service := New(db)
	payload := []byte(`{"object_kind":"push","project":{"id":4242},"ref":"refs/heads/main","after":"deadbeef","commits":[{"id":"deadbeef","added":[],"modified":["README.md"],"removed":[]}]}`)

	first, err := service.Enqueue(ctx, "gitlab", "uuid-1", "Push Hook", payload)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.Jobs == 0 {
		t.Fatalf("the first delivery was not queued: %#v", first)
	}

	// The same push arriving again, with the event id the source server gives a
	// retry — which is not the same one.
	for attempt, id := range []string{"uuid-2", "uuid-3"} {
		repeat, repeatErr := service.Enqueue(ctx, "gitlab", id, "Push Hook", payload)
		if repeatErr != nil {
			t.Fatalf("retry %d: %v", attempt+1, repeatErr)
		}
		if !repeat.Duplicate || repeat.Jobs != 0 {
			t.Fatalf("retry %d was indexed again: %#v", attempt+1, repeat)
		}
	}

	var count int
	var last any
	if err = db.DB.QueryRow(`SELECT duplicate_count,last_duplicate_at FROM webhook_events WHERE id='gitlab:uuid-1'`).Scan(&count, &last); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("two repeats arrived and the event records %d", count)
	}
	if last == nil {
		t.Fatal("the event does not say when it last came back")
	}

	var jobs int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("the same push produced %d index jobs", jobs)
	}
}

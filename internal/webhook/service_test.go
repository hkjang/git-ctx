package webhook

import (
	"context"
	"testing"

	"git-ctx/internal/store"
)

func TestEnqueueIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id) VALUES('bitbucket:7','KCB','demo','Demo','bitbucket','7','/kcb/demo')`)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db)
	payload := []byte(`{"repository":{"id":7},"changes":[{"ref":{"displayId":"main"}},{"ref":{"displayId":"main"}},{"ref":{"displayId":"v1"}}]}`)
	first, err := s.Enqueue(ctx, "bitbucket", "evt-1", "repo:refs_changed", payload)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.Jobs != 2 {
		t.Fatalf("first=%#v", first)
	}
	second, err := s.Enqueue(ctx, "bitbucket", "evt-1", "repo:refs_changed", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Jobs != 0 {
		t.Fatalf("second=%#v", second)
	}
	redelivered, err := s.Enqueue(ctx, "bitbucket", "evt-2", "repo:refs_changed", payload)
	if err != nil || !redelivered.Duplicate {
		t.Fatalf("payload redelivery=%#v err=%v", redelivered, err)
	}
	var events, jobs int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM webhook_events`).Scan(&events)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs`).Scan(&jobs)
	if events != 1 || jobs != 2 {
		t.Fatalf("events=%d jobs=%d", events, jobs)
	}
}

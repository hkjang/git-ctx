package webhook

import (
	"context"
	"strings"
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

// A hook that names a repository this platform does not have is the common
// misconfiguration, and it used to leave no trace: the sender saw an error and
// the operator saw an idle queue with nothing to explain it.
func TestRejectedWebhooksAreRecordedWithTheirReason(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:webhook-rejected?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	service := New(db)

	payload := []byte(`{"project":{"id":4242},"ref":"refs/heads/main"}`)
	if _, err = service.Enqueue(ctx, "gitlab", "evt-1", "Push Hook", payload); err == nil {
		t.Fatal("an unregistered repository must be rejected")
	}
	var status, detail, external string
	if err = db.DB.QueryRow(`SELECT status,detail,external_event_id FROM webhook_events WHERE id='gitlab:evt-1'`).Scan(&status, &detail, &external); err != nil {
		t.Fatalf("the rejection was not recorded: %v", err)
	}
	if status != "rejected" || !strings.Contains(detail, "4242") {
		t.Fatalf("status=%s detail=%q", status, detail)
	}

	// An unparsable payload is recorded too, with the parser's reason.
	if _, err = service.Enqueue(ctx, "gitlab", "evt-2", "Push Hook", []byte(`{"nonsense":true}`)); err == nil {
		t.Fatal("an unparsable payload must be rejected")
	}
	if err = db.DB.QueryRow(`SELECT status,detail FROM webhook_events WHERE id='gitlab:evt-2'`).Scan(&status, &detail); err != nil {
		t.Fatalf("the parse rejection was not recorded: %v", err)
	}
	if status != "rejected" || detail == "" {
		t.Fatalf("status=%s detail=%q", status, detail)
	}

	// Recording a rejection must not create indexing work.
	var jobs int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("jobs=%d err=%v", jobs, err)
	}

	// Once the repository is registered the same hook is accepted, and the
	// earlier rejection does not block it.
	if _, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('gitlab:4242','core','api','api','gitlab','4242','/core/api','main')`); err != nil {
		t.Fatal(err)
	}
	result, err := service.Enqueue(ctx, "gitlab", "evt-3", "Push Hook", payload)
	if err != nil || result.Jobs == 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"git-ctx/internal/store"
)

// A source outage deliberately does not spend a job's retry budget: the server
// is expected back, and marking a healthy repository failed because its host
// restarted would be worse than waiting. What was missing is any notion of an
// outage that has stopped being transient — a repository whose API answers 500
// forever, or whose token was revoked, was retried every thirty seconds for as
// long as the platform ran, stayed 'pending' and told nobody.
func TestAnOutageThatDoesNotEndIsReported(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "outage.db")+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(db.Rebind(query), args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO users(id,subject,username,email,status) VALUES('ops','ops','ops','','active')`)
	exec(`INSERT INTO user_roles(user_id,role_code) VALUES('ops','source-admin')`)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','gitlab','1','/gitlab~core/api','main',1)`)
	exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,attempts,next_run_at,started_at) VALUES('job-1','gitlab:1','main','full','running',0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)

	worker := New(db, nil, nil)
	failure := errNotAvailable{}

	// A fresh outage says nothing: a restart takes minutes and the retry will
	// probably carry it.
	exec(`UPDATE index_jobs SET outage_since=? WHERE id='job-1'`, time.Now().UTC())
	worker.notifyPersistentOutage(ctx, job{ID: "job-1", RepositoryID: "gitlab:1", RefName: "main"}, failure)
	var notified int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE notification_type='index_source_outage'`).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != 0 {
		t.Fatalf("a restart-length outage was reported as an incident: %d notifications", notified)
	}

	// One that has been going since before the threshold is another matter.
	exec(`UPDATE index_jobs SET outage_since=? WHERE id='job-1'`, time.Now().UTC().Add(-2*OutageReportedAfter))
	worker.notifyPersistentOutage(ctx, job{ID: "job-1", RepositoryID: "gitlab:1", RefName: "main"}, failure)
	var title, message string
	if err = db.DB.QueryRow(`SELECT title,message FROM notifications WHERE notification_type='index_source_outage'`).Scan(&title, &message); err != nil {
		t.Fatalf("an outage lasting %s told nobody: %v", 2*OutageReportedAfter, err)
	}
	if title == "" || message == "" {
		t.Fatal("the notification carries no text")
	}

	// Retrying every thirty seconds must not produce a notification every thirty
	// seconds.
	for i := 0; i < 5; i++ {
		worker.notifyPersistentOutage(ctx, job{ID: "job-1", RepositoryID: "gitlab:1", RefName: "main"}, failure)
	}
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE notification_type='index_source_outage'`).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Fatalf("a job retrying in a loop produced %d notifications", notified)
	}
}

type errNotAvailable struct{}

func (errNotAvailable) Error() string { return "gitlab API 500 Internal Server Error" }

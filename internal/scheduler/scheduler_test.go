package scheduler

import (
	"context"
	"testing"
	"time"

	"git-ctx/internal/store"
)

func TestRunOnceSchedulesOnlyOnePendingIntegrityJob(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('gitlab:1','core','demo','Demo','gitlab','1','/core/demo','main')`)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, func(context.Context) time.Duration { return 30 * time.Minute })
	if err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	var kind, ref string
	if err = db.DB.QueryRow(`SELECT COUNT(*),MIN(kind),MIN(ref_name) FROM index_jobs`).Scan(&count, &kind, &ref); err != nil {
		t.Fatal(err)
	}
	if count != 1 || kind != "poll" || ref != "main" {
		t.Fatalf("count=%d kind=%s ref=%s", count, kind, ref)
	}
	_, _ = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','u1','alice','')`)
	_, _ = db.DB.Exec(`INSERT INTO api_keys(id,user_id,name,prefix,key_hash,scopes,expires_at) VALUES('k1','u1','soon','ABCDEF',X'00','query-docs',?)`, time.Now().Add(24*time.Hour))
	if err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var notifications int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id='u1' AND notification_type='api_key_expiring'`).Scan(&notifications)
	if notifications != 1 {
		t.Fatalf("notifications=%d", notifications)
	}
}

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

func TestRunOnceAppliesConfiguredRetention(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:retention?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	old := time.Now().UTC().Add(-10 * 24 * time.Hour)
	_, _ = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','u1','alice','')`)
	_, _ = db.DB.Exec(`INSERT INTO audit_logs(id,occurred_at,actor_id,action,resource_type,resource_id,outcome,ip_address) VALUES('old-audit',?,'u1','test','test','1','success','')`, old)
	_, _ = db.DB.Exec(`INSERT INTO mcp_calls(id,occurred_at,user_id,tool,outcome,duration_ms,client_ip) VALUES('old-call',?,'u1','query-docs','success',1,'')`, old)
	_, _ = db.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,title,message,created_at) VALUES('old-notice','u1','test','old','old',?)`, old)
	_, _ = db.DB.Exec(`INSERT INTO webhook_events(id,source_type,external_event_id,repository_id,event_type,payload_hash,received_at) VALUES('old-hook','gitlab','1','r1','push','hash',?)`, old)
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,created_at) VALUES('old-job','r1','main','poll','completed',?)`, old)
	_, _ = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,created_at) VALUES('pending-job','r1','main','poll','pending',?)`, old)
	_, _ = db.DB.Exec(`INSERT INTO index_security_events(id,repository_id,ref_name,file_path,finding_type,action,occurred_at) VALUES('old-security','r1','main','secret.txt','secret','blocked',?)`, old)
	_, _ = db.DB.Exec(`INSERT INTO system_settings(category,version,value_encrypted,updated_by) VALUES('ui',2,X'00','u1')`)
	_, _ = db.DB.Exec(`INSERT INTO setting_versions(category,version,value_encrypted,changed_by,reason,created_at) VALUES('ui',1,X'00','u1','',?)`, old)
	_, _ = db.DB.Exec(`INSERT INTO setting_versions(category,version,value_encrypted,changed_by,reason,created_at) VALUES('ui',2,X'00','u1','',?)`, old)

	s := New(db, func(context.Context) time.Duration { return 30 * time.Minute })
	s.SetRetentionLoader(func(context.Context) RetentionPolicy {
		return RetentionPolicy{AuditLogDays: 1, MCPCallDays: 1, NotificationDays: 1, WebhookEventDays: 1, IndexJobDays: 1, SecurityEventDays: 1, SettingVersionDays: 1}
	})
	if err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{
		"audit_logs": "old-audit", "mcp_calls": "old-call", "notifications": "old-notice",
		"webhook_events": "old-hook", "index_jobs": "old-job", "index_security_events": "old-security",
	} {
		var count int
		if err = db.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s id=%s count=%d err=%v", table, id, count, err)
		}
	}
	var pending, oldVersion, currentVersion int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs WHERE id='pending-job'`).Scan(&pending)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM setting_versions WHERE category='ui' AND version=1`).Scan(&oldVersion)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM setting_versions WHERE category='ui' AND version=2`).Scan(&currentVersion)
	if pending != 1 || oldVersion != 0 || currentVersion != 1 {
		t.Fatalf("pending=%d oldVersion=%d currentVersion=%d", pending, oldVersion, currentVersion)
	}
}

func TestNotificationPolicyControlsExpiryAlerts(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:notification-policy?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','u1','alice','')`)
	_, _ = db.DB.Exec(`INSERT INTO api_keys(id,user_id,name,prefix,key_hash,scopes,expires_at) VALUES('k1','u1','soon','ABCDEF',X'00','query-docs',?)`, time.Now().Add(24*time.Hour))
	s := New(db, func(context.Context) time.Duration { return 30 * time.Minute })
	s.SetNotificationLoader(func(context.Context) NotificationPolicy {
		return NotificationPolicy{Enabled: false, APIKeyExpiryWarningDays: 7}
	})
	if err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&count)
	if count != 0 {
		t.Fatalf("disabled notification policy created %d alerts", count)
	}
	s.SetNotificationLoader(func(context.Context) NotificationPolicy {
		return NotificationPolicy{Enabled: true, APIKeyExpiryWarningDays: 2}
	})
	if err = s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&count)
	if count != 1 {
		t.Fatalf("enabled notification policy created %d alerts", count)
	}
}

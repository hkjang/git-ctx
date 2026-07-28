package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"git-ctx/internal/store"
)

type IntervalLoader func(context.Context) time.Duration
type RetentionLoader func(context.Context) RetentionPolicy
type NotificationLoader func(context.Context) NotificationPolicy

type RetentionPolicy struct {
	AuditLogDays       int
	MCPCallDays        int
	NotificationDays   int
	WebhookEventDays   int
	IndexJobDays       int
	SecurityEventDays  int
	SettingVersionDays int
}

type NotificationPolicy struct {
	Enabled                 bool
	APIKeyExpiryWarningDays int
}

type Scheduler struct {
	store         *store.Store
	interval      IntervalLoader
	retention     RetentionLoader
	notifications NotificationLoader
	tick          time.Duration
}

func New(s *store.Store, loader IntervalLoader) *Scheduler {
	return &Scheduler{store: s, interval: loader, tick: time.Minute}
}

func (s *Scheduler) SetRetentionLoader(loader RetentionLoader) {
	s.retention = loader
}

func (s *Scheduler) SetNotificationLoader(loader NotificationLoader) {
	s.notifications = loader
}
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		_ = s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Scheduler) RunOnce(ctx context.Context) error {
	now := time.Now().UTC()
	interval := s.interval(ctx)
	if interval < time.Minute {
		interval = 30 * time.Minute
	}
	threshold := now.Add(-interval)
	// Recover work abandoned by a terminated worker. Attempts are preserved.
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE index_jobs SET status='pending',next_run_at=?,error_message='worker lease expired' WHERE status='running' AND started_at<?`), now, now.Add(-15*time.Minute))
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM auth_flows WHERE expires_at<?`), now)
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM user_sessions WHERE expires_at<?`), now)
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM mcp_sessions WHERE expires_at<?`), now)
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM admin_recovery_tokens WHERE expires_at<?`), now)
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM document_chunks_staging WHERE indexed_at<?`), now.Add(-24*time.Hour))
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM api_key_usage_buckets WHERE bucket_start<?`), now.Add(-48*time.Hour))
	if s.retention != nil {
		s.applyRetention(ctx, now, s.retention(ctx))
	}
	notificationPolicy := NotificationPolicy{Enabled: true, APIKeyExpiryWarningDays: 7}
	if s.notifications != nil {
		notificationPolicy = s.notifications(ctx)
	}
	var expiring *sql.Rows
	var err error
	if notificationPolicy.Enabled && notificationPolicy.APIKeyExpiryWarningDays > 0 {
		expiring, err = s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT id,user_id,name,prefix,expires_at FROM api_keys WHERE revoked_at IS NULL AND disabled_at IS NULL AND expires_at>? AND expires_at<=?`), now, now.Add(time.Duration(notificationPolicy.APIKeyExpiryWarningDays)*24*time.Hour))
	}
	if err == nil {
		type expiryNotice struct{ id, userID, keyID, message string }
		var notices []expiryNotice
		for expiring != nil && expiring.Next() {
			var keyID, userID, name, prefix string
			var expires time.Time
			if expiring.Scan(&keyID, &userID, &name, &prefix, &expires) == nil {
				id := "key-expiry:" + keyID
				message := fmt.Sprintf("%s (%s) key expires at %s", name, prefix, expires.UTC().Format(time.RFC3339))
				notices = append(notices, expiryNotice{id, userID, keyID, message})
			}
		}
		if expiring != nil {
			expiring.Close()
		}
		for _, notice := range notices {
			_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES(?,?,'api_key_expiring',?,'MCP API key expires soon',?) ON CONFLICT(user_id,notification_type,resource_id) DO NOTHING`), notice.id, notice.userID, notice.keyID, notice.message)
		}
	}
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT id,default_branch FROM repositories r WHERE enabled=1 AND (indexed_at IS NULL OR indexed_at<?) AND NOT EXISTS(SELECT 1 FROM index_jobs j WHERE j.repository_id=r.id AND j.status IN ('pending','running'))`), threshold)
	if err != nil {
		return err
	}
	defer rows.Close()
	type target struct{ id, ref string }
	var targets []target
	for rows.Next() {
		var x target
		if err := rows.Scan(&x.id, &x.ref); err != nil {
			return err
		}
		targets = append(targets, x)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for n, x := range targets {
		id := fmt.Sprintf("poll:%d:%d", now.UnixNano(), n)
		if _, err := s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,next_run_at) VALUES(?,?,?,'poll','pending',?)`), id, x.id, x.ref, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) applyRetention(ctx context.Context, now time.Time, policy RetentionPolicy) {
	deleteBefore := func(days int, query string) {
		if days <= 0 {
			return
		}
		_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(query), now.Add(-time.Duration(days)*24*time.Hour))
	}
	deleteBefore(policy.AuditLogDays, `DELETE FROM audit_logs WHERE occurred_at<?`)
	deleteBefore(policy.MCPCallDays, `DELETE FROM mcp_calls WHERE occurred_at<?`)
	deleteBefore(policy.NotificationDays, `DELETE FROM notifications WHERE created_at<?`)
	deleteBefore(policy.WebhookEventDays, `DELETE FROM webhook_events WHERE received_at<?`)
	deleteBefore(policy.IndexJobDays, `DELETE FROM index_jobs WHERE status IN ('completed','failed') AND created_at<?`)
	deleteBefore(policy.SecurityEventDays, `DELETE FROM index_security_events WHERE occurred_at<?`)
	deleteBefore(policy.SettingVersionDays, `DELETE FROM setting_versions WHERE created_at<? AND NOT EXISTS (SELECT 1 FROM system_settings s WHERE s.category=setting_versions.category AND s.version=setting_versions.version)`)
}

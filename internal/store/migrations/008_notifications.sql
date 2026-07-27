CREATE TABLE IF NOT EXISTS notifications (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id),
  notification_type TEXT NOT NULL, resource_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL, message TEXT NOT NULL,
  read_at TIMESTAMP, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(user_id,notification_type,resource_id)
);
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id,created_at);

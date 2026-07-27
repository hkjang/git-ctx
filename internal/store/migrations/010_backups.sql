CREATE TABLE IF NOT EXISTS backup_records (
  id TEXT PRIMARY KEY,
  filename TEXT NOT NULL UNIQUE,
  schedule_slot TEXT UNIQUE,
  trigger_type TEXT NOT NULL,
  status TEXT NOT NULL,
  size_bytes BIGINT NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL,
  error_message TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_backup_records_created ON backup_records(created_at);

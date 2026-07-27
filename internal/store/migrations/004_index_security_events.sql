CREATE TABLE IF NOT EXISTS index_security_events (
  id TEXT PRIMARY KEY, repository_id TEXT NOT NULL, ref_name TEXT NOT NULL,
  file_path TEXT NOT NULL, finding_type TEXT NOT NULL, action TEXT NOT NULL,
  occurred_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_index_security_events_repo ON index_security_events(repository_id,occurred_at);

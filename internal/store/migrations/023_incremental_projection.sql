CREATE TABLE IF NOT EXISTS repository_ref_changes (
  repository_id TEXT NOT NULL REFERENCES repositories(id),
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  previous_commit_id TEXT NOT NULL DEFAULT '',
  file_path TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  deleted_chunk_ids TEXT NOT NULL DEFAULT '[]',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(repository_id, ref_name, commit_id, file_path, action)
);
CREATE INDEX IF NOT EXISTS idx_ref_changes_projection
  ON repository_ref_changes(repository_id, ref_name, commit_id);
CREATE TABLE IF NOT EXISTS search_projection_states (
  repository_id TEXT NOT NULL REFERENCES repositories(id),
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  acl_fingerprint TEXT NOT NULL DEFAULT '',
  projected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(repository_id, ref_name)
);

CREATE TABLE IF NOT EXISTS repository_ref_states (
  repository_id TEXT NOT NULL REFERENCES repositories(id),
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(repository_id, ref_name)
);
CREATE INDEX IF NOT EXISTS idx_ref_states_commit
  ON repository_ref_states(repository_id, commit_id);

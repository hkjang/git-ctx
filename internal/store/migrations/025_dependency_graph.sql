CREATE TABLE IF NOT EXISTS code_dependencies (
  id TEXT PRIMARY KEY,
  repository_id TEXT NOT NULL REFERENCES repositories(id),
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  from_symbol TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL,
  dependency_kind TEXT NOT NULL,
  line_number INTEGER NOT NULL,
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_dependencies_source
  ON code_dependencies(repository_id, ref_name, from_symbol, file_path);
CREATE INDEX IF NOT EXISTS idx_dependencies_target
  ON code_dependencies(repository_id, ref_name, target);
CREATE TABLE IF NOT EXISTS code_dependencies_staging (
  generation_id TEXT NOT NULL,
  id TEXT NOT NULL,
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  from_symbol TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL,
  dependency_kind TEXT NOT NULL,
  line_number INTEGER NOT NULL,
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(generation_id, id)
);
CREATE INDEX IF NOT EXISTS idx_dependencies_staging_generation
  ON code_dependencies_staging(generation_id);
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES
  ('trace-dependencies',1,15000,30),
  ('compare-refs',1,20000,15),
  ('get-change-impact',1,20000,15)
ON CONFLICT(name) DO NOTHING;

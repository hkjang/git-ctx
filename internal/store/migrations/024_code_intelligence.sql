CREATE TABLE IF NOT EXISTS code_symbols (
  id TEXT PRIMARY KEY,
  repository_id TEXT NOT NULL REFERENCES repositories(id),
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  name TEXT NOT NULL,
  qualified_name TEXT NOT NULL,
  symbol_kind TEXT NOT NULL,
  language TEXT NOT NULL,
  signature TEXT NOT NULL DEFAULT '',
  documentation TEXT NOT NULL DEFAULT '',
  line_start INTEGER NOT NULL,
  line_end INTEGER NOT NULL,
  content_hash TEXT NOT NULL,
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_symbols_lookup
  ON code_symbols(repository_id, ref_name, name, symbol_kind);
CREATE INDEX IF NOT EXISTS idx_symbols_path_line
  ON code_symbols(repository_id, ref_name, file_path, line_start);
CREATE TABLE IF NOT EXISTS code_symbols_staging (
  generation_id TEXT NOT NULL,
  id TEXT NOT NULL,
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  name TEXT NOT NULL,
  qualified_name TEXT NOT NULL,
  symbol_kind TEXT NOT NULL,
  language TEXT NOT NULL,
  signature TEXT NOT NULL DEFAULT '',
  documentation TEXT NOT NULL DEFAULT '',
  line_start INTEGER NOT NULL,
  line_end INTEGER NOT NULL,
  content_hash TEXT NOT NULL,
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(generation_id, id)
);
CREATE INDEX IF NOT EXISTS idx_symbols_staging_generation
  ON code_symbols_staging(generation_id);
CREATE TABLE IF NOT EXISTS repository_maps (
  repository_id TEXT NOT NULL REFERENCES repositories(id),
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  summary_json TEXT NOT NULL,
  generated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(repository_id, ref_name)
);
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES
  ('get-repository-map',1,10000,60),
  ('find-symbol',1,15000,30),
  ('get-symbol-context',1,15000,30)
ON CONFLICT(name) DO NOTHING;

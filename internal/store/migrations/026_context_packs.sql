CREATE TABLE IF NOT EXISTS context_packs (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_by TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS context_pack_items (
  pack_id TEXT NOT NULL REFERENCES context_packs(id) ON DELETE CASCADE,
  library_id TEXT NOT NULL,
  ref_name TEXT NOT NULL DEFAULT '',
  query_hint TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(pack_id,library_id,ref_name)
);
CREATE INDEX IF NOT EXISTS idx_context_pack_items_library
  ON context_pack_items(library_id,pack_id);
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES
  ('get-context-pack',1,30000,30),
  ('find-runbook',1,20000,30),
  ('export-context',1,30000,0)
ON CONFLICT(name) DO NOTHING;

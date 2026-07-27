INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES
  ('search-repositories',1,30000,30),
  ('search-source',1,30000,0),
  ('get-platform-status',1,10000,0),
  ('list-index-jobs',1,10000,0),
  ('reindex-repository',1,10000,0)
ON CONFLICT(name) DO NOTHING;

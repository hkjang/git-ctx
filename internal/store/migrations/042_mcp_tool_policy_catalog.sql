-- MCP tools are configurable operational resources, not registry-only names.
-- These tools were added after the last policy migration; without rows here
-- they are callable with server defaults, but an administrator's UPDATE has no
-- row to change and therefore appears to save while having no effect.
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds,max_response_bytes)
VALUES
  ('build-context',1,45000,30,40960),
  ('find-code-owner',1,45000,300,0),
  ('find-tests',1,30000,60,0),
  ('get-architecture-map',1,30000,60,40960),
  ('assess-change-risk',1,60000,30,40960),
  ('get-repository-health',1,30000,60,0)
ON CONFLICT(name) DO NOTHING;

-- Repository-health checks qualified cross-repository consumers by target
-- first. The original index starts with repository/ref and forces a broad scan
-- for that access pattern on large dependency graphs.
CREATE INDEX IF NOT EXISTS idx_dependencies_target_global
  ON code_dependencies(target,repository_id,ref_name);

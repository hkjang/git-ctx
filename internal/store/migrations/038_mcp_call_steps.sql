-- One MCP call walks several stages: resolve the caller's ACL, query the index,
-- fall back to the source server per repository, embed, rank, format. The call
-- record says the answer was empty; it cannot say which stage emptied it.
-- These rows are that X-ray: every stage with its duration, what it looked at
-- (candidates) and what it passed on (results). The gap between the two is
-- where the result was lost.
CREATE TABLE IF NOT EXISTS mcp_call_steps (
  call_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  stage TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  candidates INTEGER NOT NULL DEFAULT 0,
  results INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  offset_ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (call_id, sequence)
);

-- The trace summary is read with the call row itself, so it lives there.
ALTER TABLE mcp_calls ADD COLUMN trace_summary TEXT NOT NULL DEFAULT '';

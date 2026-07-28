-- MCP calls were recorded with who, which tool and how long, which answers
-- "was it used" but not the questions an operator actually has: which client
-- asked, what was asked, whether the answer came from the cache, why it was
-- empty, and which retrieval path produced it. Without those, tuning a timeout,
-- a budget or an index priority is guesswork, and a security review cannot
-- reconstruct a session.
ALTER TABLE mcp_calls ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_calls ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_calls ADD COLUMN client_name TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_calls ADD COLUMN client_version TEXT NOT NULL DEFAULT '';
-- A bounded, secret-masked rendering of the arguments. The hash groups repeated
-- questions without keeping the full text of every one of them.
ALTER TABLE mcp_calls ADD COLUMN arguments_preview TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_calls ADD COLUMN arguments_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_calls ADD COLUMN result_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mcp_calls ADD COLUMN cache_hit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mcp_calls ADD COLUMN error_code TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_calls ADD COLUMN retrieval_mode TEXT NOT NULL DEFAULT '';

-- Analytics always filters by time first, then by tool or user.
CREATE INDEX IF NOT EXISTS idx_mcp_calls_occurred ON mcp_calls(occurred_at);
CREATE INDEX IF NOT EXISTS idx_mcp_calls_tool_occurred ON mcp_calls(tool, occurred_at);
CREATE INDEX IF NOT EXISTS idx_mcp_calls_user_occurred ON mcp_calls(user_id, occurred_at);

-- The client identity is negotiated once per session at initialize, so it is
-- stored there and copied onto every call of that session.
ALTER TABLE mcp_sessions ADD COLUMN client_name TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_sessions ADD COLUMN client_version TEXT NOT NULL DEFAULT '';

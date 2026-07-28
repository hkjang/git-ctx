CREATE TABLE IF NOT EXISTS mcp_sessions (
  id_hash BLOB PRIMARY KEY,
  expires_at TIMESTAMP NOT NULL,
  last_seen_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_mcp_sessions_expiry ON mcp_sessions(expires_at);

CREATE TABLE IF NOT EXISTS admin_recovery_tokens (
  token_hash BLOB PRIMARY KEY,
  expires_at TIMESTAMP NOT NULL,
  used_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_admin_recovery_tokens_expires
  ON admin_recovery_tokens(expires_at);

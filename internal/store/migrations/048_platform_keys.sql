-- The keys this platform seals its own data with.
--
-- They used to be derived from the database DSN — sha256 over the connection
-- string — so they existed nowhere and were recomputed at every start. Any
-- textual change to that string produced different keys: moving the data
-- directory, writing an absolute path where a relative one had been, reordering
-- two query parameters. The settings then failed to decrypt and the platform
-- refused to start, reporting a cipher authentication failure, and every API
-- key stopped authenticating because its pepper had changed too.
--
-- Stored here, wrapped by a key derived from GIT_CTX_RECOVERY_KEY, they survive
-- a move. The recovery key is already required, already meant to be stable, and
-- already the operator's responsibility to keep.
CREATE TABLE IF NOT EXISTS platform_keys (
  id TEXT PRIMARY KEY,
  master_key_wrapped BLOB NOT NULL,
  api_key_pepper_wrapped BLOB NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

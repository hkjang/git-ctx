CREATE TABLE IF NOT EXISTS managed_secrets (
  name TEXT PRIMARY KEY,
  backend TEXT NOT NULL,
  value_encrypted BLOB,
  vault_path TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active',
  updated_by TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS managed_secret_versions (
  name TEXT NOT NULL,
  version INTEGER NOT NULL,
  backend TEXT NOT NULL,
  value_encrypted BLOB,
  vault_path TEXT NOT NULL DEFAULT '',
  changed_by TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(name, version)
);

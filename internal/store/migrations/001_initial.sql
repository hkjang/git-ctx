CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY, subject TEXT NOT NULL UNIQUE, username TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS user_identities (
  user_id TEXT PRIMARY KEY REFERENCES users(id),
  bitbucket_user_slug TEXT NOT NULL DEFAULT '',
  gitlab_user_id TEXT NOT NULL DEFAULT '',
  mapping_source TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS roles (
  code TEXT PRIMARY KEY, description TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS user_roles (
  user_id TEXT NOT NULL REFERENCES users(id), role_code TEXT NOT NULL REFERENCES roles(code),
  PRIMARY KEY(user_id, role_code)
);
CREATE TABLE IF NOT EXISTS auth_flows (
  state TEXT PRIMARY KEY, code_verifier TEXT NOT NULL,
  return_to TEXT NOT NULL DEFAULT '/',
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS user_sessions (
  id_hash BLOB PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id),
  expires_at TIMESTAMP NOT NULL, last_seen_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user ON user_sessions(user_id, expires_at);
CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id),
  name TEXT NOT NULL, prefix TEXT NOT NULL UNIQUE, key_hash BLOB NOT NULL,
  scopes TEXT NOT NULL, expires_at TIMESTAMP, disabled_at TIMESTAMP,
  revoked_at TIMESTAMP, last_used_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS repositories (
  id TEXT PRIMARY KEY, project_key TEXT NOT NULL, slug TEXT NOT NULL,
  name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  source_type TEXT NOT NULL DEFAULT 'bitbucket',
  source_external_id TEXT NOT NULL DEFAULT '',
  library_id TEXT NOT NULL UNIQUE, default_branch TEXT NOT NULL DEFAULT 'main',
  reputation TEXT NOT NULL DEFAULT 'Medium', enabled INTEGER NOT NULL DEFAULT 1,
  indexed_at TIMESTAMP, UNIQUE(project_key, slug)
);
CREATE TABLE IF NOT EXISTS repository_permissions (
  repository_id TEXT NOT NULL REFERENCES repositories(id), principal TEXT NOT NULL,
  permission TEXT NOT NULL, PRIMARY KEY(repository_id, principal)
);
CREATE TABLE IF NOT EXISTS document_chunks (
  id TEXT PRIMARY KEY, repository_id TEXT NOT NULL REFERENCES repositories(id),
  ref_name TEXT NOT NULL, commit_id TEXT NOT NULL, file_path TEXT NOT NULL,
  line_start INTEGER NOT NULL, line_end INTEGER NOT NULL, heading TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL, content TEXT NOT NULL, content_hash TEXT NOT NULL,
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_chunks_repo_ref ON document_chunks(repository_id, ref_name);
CREATE TABLE IF NOT EXISTS system_settings (
  category TEXT PRIMARY KEY, version INTEGER NOT NULL, value_encrypted BLOB NOT NULL,
  updated_by TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS setting_versions (
  category TEXT NOT NULL, version INTEGER NOT NULL, value_encrypted BLOB NOT NULL,
  changed_by TEXT NOT NULL, reason TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(category, version)
);
CREATE TABLE IF NOT EXISTS audit_logs (
  id TEXT PRIMARY KEY, occurred_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  actor_id TEXT NOT NULL, action TEXT NOT NULL, resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL, outcome TEXT NOT NULL, ip_address TEXT NOT NULL,
  metadata TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS mcp_calls (
  id TEXT PRIMARY KEY, occurred_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  user_id TEXT NOT NULL, api_key_prefix TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL,
  library_id TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL, duration_ms INTEGER NOT NULL,
  client_ip TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS index_jobs (
  id TEXT PRIMARY KEY, repository_id TEXT NOT NULL, ref_name TEXT NOT NULL,
  kind TEXT NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '', files_processed INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  next_run_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at TIMESTAMP, completed_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_index_jobs_status ON index_jobs(status, created_at);
CREATE TABLE IF NOT EXISTS webhook_events (
  id TEXT PRIMARY KEY, source_type TEXT NOT NULL, external_event_id TEXT NOT NULL,
  repository_id TEXT NOT NULL, event_type TEXT NOT NULL, payload_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'received',
  received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  processed_at TIMESTAMP,
  UNIQUE(source_type, external_event_id)
);
INSERT INTO roles(code,description) VALUES
 ('platform-admin','Full platform administration'),
 ('security-admin','Security, keys, secrets, and audit'),
 ('mcp-admin','MCP tools and transport administration'),
 ('source-admin','Source and index administration'),
 ('search-admin','Search and model administration'),
 ('auditor','Read audit records'),
 ('developer','Search documents and manage personal keys'),
 ('service-account','Approved automation'),
 ('readonly-operator','Read operational status')
ON CONFLICT(code) DO NOTHING;

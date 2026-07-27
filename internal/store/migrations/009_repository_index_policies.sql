CREATE TABLE IF NOT EXISTS repository_index_policies (
  repository_id TEXT PRIMARY KEY REFERENCES repositories(id),
  include_extensions TEXT NOT NULL DEFAULT '.md,.mdx,.rst,.txt,.adoc,.asciidoc,.yaml,.yml,.json,.go,.java,.ts,.tsx,.py',
  exclude_prefixes TEXT NOT NULL DEFAULT 'node_modules/,vendor/,dist/,.git/,secrets/',
  max_file_bytes INTEGER NOT NULL DEFAULT 1048576,
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

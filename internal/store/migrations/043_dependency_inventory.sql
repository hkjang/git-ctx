-- code_dependencies records imports and calls, which answers "who uses this
-- function". It cannot answer the question an operator asks during an advisory
-- or an upgrade: "which repositories depend on this library, at which version".
-- An import line names a package and never its version; only the manifest does.
CREATE TABLE IF NOT EXISTS repository_packages (
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  ecosystem TEXT NOT NULL,
  name TEXT NOT NULL,
  name_lower TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL DEFAULT 'direct',
  manifest_path TEXT NOT NULL,
  commit_id TEXT NOT NULL DEFAULT '',
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (repository_id, ref_name, ecosystem, name, manifest_path)
);
-- The lookup is by package name across every repository, so that column leads.
CREATE INDEX IF NOT EXISTS idx_repository_packages_name ON repository_packages(name_lower, ecosystem);
CREATE INDEX IF NOT EXISTS idx_repository_packages_repo ON repository_packages(repository_id, ref_name);

CREATE TABLE IF NOT EXISTS repository_packages_staging (
  generation_id TEXT NOT NULL,
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  ecosystem TEXT NOT NULL,
  name TEXT NOT NULL,
  name_lower TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL DEFAULT 'direct',
  manifest_path TEXT NOT NULL,
  commit_id TEXT NOT NULL DEFAULT '',
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (generation_id, repository_id, ref_name, ecosystem, name, manifest_path)
);
CREATE INDEX IF NOT EXISTS idx_repository_packages_staging_generation
  ON repository_packages_staging(generation_id);

INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds,max_response_bytes)
VALUES ('find-dependency-usage',1,30000,300,40960)
ON CONFLICT(name) DO NOTHING;

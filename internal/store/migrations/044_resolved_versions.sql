-- A manifest states intent ("^18.2.0") and a lock file states the result
-- ("18.3.1"). Only the second can be judged against an advisory, so lock files
-- are now inventoried alongside manifests.
--
-- That makes the version part of a package's identity: one lock file legitimately
-- carries the same package at two versions when a transitive copy is pinned
-- separately, and during an advisory the nested vulnerable copy is exactly the
-- one that must not be collapsed away. The previous primary key had no version
-- column, so the tables are rebuilt with one.
--
-- The rows are not migrated. The inventory is derived data that the next index
-- run regenerates, it has to be regenerated anyway to pick up lock files, and
-- the console states the coverage so an empty inventory is visible rather than
-- silently wrong.
DROP TABLE IF EXISTS repository_packages;
DROP TABLE IF EXISTS repository_packages_staging;

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
  PRIMARY KEY (repository_id, ref_name, ecosystem, name, version, manifest_path)
);
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
  PRIMARY KEY (generation_id, repository_id, ref_name, ecosystem, name, version, manifest_path)
);
CREATE INDEX IF NOT EXISTS idx_repository_packages_staging_generation
  ON repository_packages_staging(generation_id);

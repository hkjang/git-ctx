-- Filename search needs the complete file listing of a ref, not only the files
-- whose content passed the index policy. document_chunks holds text that was
-- chunked, so a lockfile, image or excluded source file is invisible there even
-- though "where is Dockerfile" is a normal question for a coding agent.
CREATE TABLE IF NOT EXISTS repository_files (
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  path TEXT NOT NULL,
  base_name TEXT NOT NULL,
  size_bytes BIGINT NOT NULL DEFAULT 0,
  content_indexed INTEGER NOT NULL DEFAULT 0,
  commit_id TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (repository_id, ref_name, path)
);
CREATE INDEX IF NOT EXISTS idx_repository_files_base ON repository_files(base_name);
CREATE INDEX IF NOT EXISTS idx_repository_files_repo_ref ON repository_files(repository_id, ref_name);

INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('find-file',1,30000,30)
ON CONFLICT(name) DO NOTHING;

CREATE TABLE IF NOT EXISTS document_chunks_staging (
  generation_id TEXT NOT NULL,
  id TEXT NOT NULL,
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  file_path TEXT NOT NULL,
  line_start INTEGER NOT NULL,
  line_end INTEGER NOT NULL,
  heading TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL,
  content TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  embedding BLOB,
  indexed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(generation_id, id)
);
CREATE INDEX IF NOT EXISTS idx_chunk_staging_generation
  ON document_chunks_staging(generation_id);
CREATE INDEX IF NOT EXISTS idx_repository_permissions_principal
  ON repository_permissions(principal, repository_id);
CREATE INDEX IF NOT EXISTS idx_chunks_repo_ref_path_line
  ON document_chunks(repository_id, ref_name, file_path, line_start);
CREATE INDEX IF NOT EXISTS idx_index_jobs_claim
  ON index_jobs(status, next_run_at, created_at);
CREATE INDEX IF NOT EXISTS idx_repositories_source_project_slug
  ON repositories(source_type, project_key, slug);

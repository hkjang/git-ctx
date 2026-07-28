ALTER TABLE document_chunks ADD COLUMN embedding_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE document_chunks ADD COLUMN embedding_model TEXT NOT NULL DEFAULT '';
ALTER TABLE document_chunks ADD COLUMN embedding_dimensions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE document_chunks ADD COLUMN embedding_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE document_chunks_staging ADD COLUMN embedding_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE document_chunks_staging ADD COLUMN embedding_model TEXT NOT NULL DEFAULT '';
ALTER TABLE document_chunks_staging ADD COLUMN embedding_dimensions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE document_chunks_staging ADD COLUMN embedding_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE repository_ref_states ADD COLUMN embedding_revision TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_chunks_embedding_identity
  ON document_chunks(repository_id, content_hash, embedding_provider, embedding_model, embedding_revision);

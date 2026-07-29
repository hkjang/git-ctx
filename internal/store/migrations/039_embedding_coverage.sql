ALTER TABLE repository_ref_states ADD COLUMN total_chunks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repository_ref_states ADD COLUMN embedded_chunks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repository_ref_states ADD COLUMN embedding_status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE repository_ref_states ADD COLUMN embedding_error TEXT NOT NULL DEFAULT '';

UPDATE repository_ref_states
SET total_chunks=(
      SELECT COUNT(*) FROM document_chunks c
      WHERE c.repository_id=repository_ref_states.repository_id
        AND c.ref_name=repository_ref_states.ref_name
    ),
    embedded_chunks=(
      SELECT COUNT(c.embedding) FROM document_chunks c
      WHERE c.repository_id=repository_ref_states.repository_id
        AND c.ref_name=repository_ref_states.ref_name
    );

UPDATE repository_ref_states
SET embedding_status=CASE
  WHEN total_chunks=0 THEN 'empty'
  WHEN embedding_revision='keyword-only' THEN 'disabled'
  WHEN embedded_chunks=total_chunks THEN 'ready'
  WHEN embedded_chunks=0 THEN 'unavailable'
  ELSE 'partial'
END;

CREATE INDEX IF NOT EXISTS idx_ref_states_embedding_status
  ON repository_ref_states(embedding_status, indexed_at);

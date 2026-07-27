DELETE FROM webhook_events WHERE id IN (
  SELECT id FROM (
    SELECT id,ROW_NUMBER() OVER(
      PARTITION BY source_type,repository_id,event_type,payload_hash
      ORDER BY received_at,id
    ) AS duplicate_number
    FROM webhook_events
  ) duplicates WHERE duplicate_number>1
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_webhook_events_payload_dedup
ON webhook_events(source_type,repository_id,event_type,payload_hash);

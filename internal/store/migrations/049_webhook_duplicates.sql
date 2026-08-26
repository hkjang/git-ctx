-- How often the same event came back.
--
-- Identical payloads for one repository and event type are deduplicated on
-- purpose: a source server retries a hook it thinks timed out, and indexing it
-- twice is waste. But the duplicate was then dropped without a trace — no row,
-- no counter, no status — so an operator asking why a push did not reach the
-- index found nothing at all. The rejection path already records what it
-- refuses for exactly that reason; this gives the duplicate path the same.
ALTER TABLE webhook_events ADD COLUMN duplicate_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE webhook_events ADD COLUMN last_duplicate_at TIMESTAMP;

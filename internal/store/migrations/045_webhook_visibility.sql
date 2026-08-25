-- Webhook events were recorded only when they were accepted, and never shown
-- anywhere. An operator asking "why is this repository not indexing" could not
-- tell the three cases apart: the hook is not configured, the hook fires but
-- names a repository this platform does not have, or the hook works and the
-- indexing failed later. The first two left no trace at all.
--
-- detail carries why an event was rejected, together with the identifier the
-- sender used, which is what makes a misdirected hook fixable.
ALTER TABLE webhook_events ADD COLUMN detail TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_webhook_events_received ON webhook_events(received_at);

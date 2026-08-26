-- What the search index needs the next start to know about the last one.
--
-- The full-text index is maintained by triggers, and a binary built without
-- SQLite's FTS5 module cannot run them. Such a binary drops the triggers so the
-- database stays writable, which leaves the index behind whatever was indexed
-- while it ran. Nothing in the index itself records that: an external-content
-- table reports the row count of the table it reads from, so a stale index and
-- a current one look identical. This note is how the next build that does have
-- the module learns it has to rebuild.
CREATE TABLE IF NOT EXISTS index_maintenance (
  name TEXT PRIMARY KEY,
  noted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

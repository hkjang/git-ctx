ALTER TABLE api_keys ADD COLUMN replaced_by TEXT;
CREATE TABLE IF NOT EXISTS api_key_restrictions (
  api_key_id TEXT PRIMARY KEY REFERENCES api_keys(id),
  allowed_cidrs TEXT NOT NULL DEFAULT '',
  allowed_repositories TEXT NOT NULL DEFAULT '',
  rate_per_minute INTEGER NOT NULL DEFAULT 0,
  rate_per_hour INTEGER NOT NULL DEFAULT 0,
  rate_per_day INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS api_key_usage_buckets (
  api_key_id TEXT NOT NULL REFERENCES api_keys(id),
  bucket_kind TEXT NOT NULL, bucket_start TIMESTAMP NOT NULL,
  call_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(api_key_id,bucket_kind,bucket_start)
);

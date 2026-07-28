-- "Why is this code like this" needs commit history, and orienting in an
-- unfamiliar repository needs a directory listing. Both are read-only views of
-- data git-ctx already has access to.
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('get-file-history',1,30000,60), ('list-directory',1,20000,60)
ON CONFLICT(name) DO NOTHING;

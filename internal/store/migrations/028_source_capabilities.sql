INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('search-code',1,30000,0)
ON CONFLICT(name) DO NOTHING;

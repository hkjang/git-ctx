INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('explain-search-result',1,15000,15)
ON CONFLICT(name) DO NOTHING;

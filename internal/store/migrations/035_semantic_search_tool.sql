-- Meaning-based search across repositories. It answers questions phrased the
-- way a person asks them, which keyword search cannot match.
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('search-semantic',1,45000,30)
ON CONFLICT(name) DO NOTHING;

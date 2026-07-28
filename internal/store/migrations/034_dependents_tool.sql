-- Changing shared code safely requires knowing who consumes it, across every
-- repository rather than inside one.
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('find-dependents',1,30000,60)
ON CONFLICT(name) DO NOTHING;

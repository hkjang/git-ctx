-- Finding a file is only half of the task; an agent also has to read it.
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('read-file',1,30000,30)
ON CONFLICT(name) DO NOTHING;

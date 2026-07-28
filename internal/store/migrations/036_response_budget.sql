-- An MCP answer is spent from the agent's context window, and until now nothing
-- bounded it: read-file on a 4,000 line file or export-context over 20
-- repositories could return hundreds of kilobytes, crowding out the code the
-- agent was about to write. A per-tool byte budget keeps every answer usable and
-- tells the caller how to narrow it instead of silently swallowing the rest.
ALTER TABLE mcp_tools ADD COLUMN max_response_bytes INTEGER NOT NULL DEFAULT 0;

-- 0 means "use the server default". These tools legitimately return more, so
-- they get a larger budget rather than being cut at the default.
UPDATE mcp_tools SET max_response_bytes=65536 WHERE name='export-context';
UPDATE mcp_tools SET max_response_bytes=49152 WHERE name='read-file';
UPDATE mcp_tools SET max_response_bytes=40960 WHERE name IN ('search-code', 'query-docs', 'get-context-pack');

-- Budgets should be tuned from evidence, so record what each answer actually
-- cost and whether it had to be cut.
ALTER TABLE mcp_calls ADD COLUMN response_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mcp_calls ADD COLUMN truncated INTEGER NOT NULL DEFAULT 0;

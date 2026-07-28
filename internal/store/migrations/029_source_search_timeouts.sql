-- Remote code search asks the source server once per candidate repository, so a
-- 30 second budget expired mid-search on real instances and returned repository
-- names without any file contents. Raise the budget for the search tools that
-- call remote APIs, leaving operator overrides untouched.
UPDATE mcp_tools SET timeout_ms=90000 WHERE name IN ('search-code','search-source') AND timeout_ms=30000;
UPDATE mcp_tools SET timeout_ms=60000 WHERE name='query-docs' AND timeout_ms=30000;

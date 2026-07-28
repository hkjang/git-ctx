-- Commits say what changed; the merge or pull request that carried them says
-- why. Agents need that rationale for design and rollout questions.
INSERT INTO mcp_tools(name,enabled,timeout_ms,cache_seconds)
VALUES ('search-merge-requests',1,45000,60)
ON CONFLICT(name) DO NOTHING;

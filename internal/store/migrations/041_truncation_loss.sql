-- The dispatcher measures how large an answer was before the budget cut it, and
-- then records only whether a cut happened. An operator could see that a tool
-- truncates often but not whether those calls lost five percent or eighty, which
-- is the difference between a budget that is fine and a tool that is unusable at
-- it -- and the number needed to judge whether compressing answers is worth
-- building at all.
ALTER TABLE mcp_calls ADD COLUMN produced_bytes INTEGER NOT NULL DEFAULT 0;

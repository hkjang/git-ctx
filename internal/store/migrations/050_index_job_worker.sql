-- Which instance is holding this job.
--
-- The deployment runs more than one replica and they share one queue. A job
-- that sits in 'running' says when it started and nothing about where, so an
-- operator with two pods and one stuck repository cannot tell which pod to look
-- at, and "worker lease expired" never says whose lease it was.
ALTER TABLE index_jobs ADD COLUMN claimed_by TEXT NOT NULL DEFAULT '';

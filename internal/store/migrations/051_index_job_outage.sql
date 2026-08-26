-- When this job's source first stopped answering.
--
-- A source outage deliberately does not spend the job's retry budget: the
-- server is expected back, and burning five attempts on a restart would mark a
-- healthy repository as failed. But that left no notion of an outage that has
-- stopped being transient. A repository whose API answers 500 forever, or whose
-- token was revoked — 401 counts as an outage too — was retried every thirty
-- seconds for as long as the platform ran, stayed 'pending', and never
-- notified anybody.
ALTER TABLE index_jobs ADD COLUMN outage_since TIMESTAMP;

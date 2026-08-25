-- A ref is re-read when its commit moves, and until now that was the only
-- reason. Changing the index policy — adding an extension, dropping an
-- exclusion — left the stored content built by the old policy, and a manual
-- reindex of an unchanged commit reported "completed, 0 files". The policy the
-- content was built with is recorded so the next run can tell the difference.
ALTER TABLE repository_ref_states ADD COLUMN policy_revision TEXT NOT NULL DEFAULT '';

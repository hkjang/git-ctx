-- A pack was a list of repositories to run one query against. That answers
-- "search these places" but not "here is how this project works", which is what
-- an agent joining a codebase actually needs first.
--
-- purpose says what the pack is for, entrypoints name the symbols worth
-- anchoring on, conventions pulls in the files that tell a contributor how the
-- project expects to be worked in, and the budget stops a large pack from
-- filling an agent's context with the first repository in the list.
ALTER TABLE context_packs ADD COLUMN purpose TEXT NOT NULL DEFAULT '';
ALTER TABLE context_packs ADD COLUMN token_budget INTEGER NOT NULL DEFAULT 0;
ALTER TABLE context_packs ADD COLUMN include_conventions INTEGER NOT NULL DEFAULT 1;

CREATE TABLE IF NOT EXISTS context_pack_entrypoints (
  pack_id TEXT NOT NULL REFERENCES context_packs(id) ON DELETE CASCADE,
  symbol TEXT NOT NULL,
  library_id TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(pack_id,symbol,library_id)
);
CREATE INDEX IF NOT EXISTS idx_context_pack_entrypoints_pack
  ON context_pack_entrypoints(pack_id,position);

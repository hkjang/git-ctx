package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Searching indexed content by scanning every chunk with LIKE is fine for a
// demonstration corpus and wrong for a real one. Measured on 400 repositories
// and 200,000 chunks, a term that matches almost nothing still costs a full
// scan — a quarter of a second there, seconds on an estate ten times the size —
// because there is nothing to look the term up in. A full-text index turns that
// into a lookup, and it is the difference between a search an agent can make
// freely and one it has to be careful about.
//
// The index is optional on purpose. SQLite exposes FTS5 only when the driver
// was built with it, and a binary without it must still run and still answer,
// so the capability is probed once and the search layer keeps its scanning path
// as the fallback.

// FullTextAvailable reports whether this store can answer a full-text lookup.
func (s *Store) FullTextAvailable() bool { return s.fullText }

// prepareFullText creates the search index when the driver supports one. Any
// failure leaves the store without the capability rather than refusing to open:
// a missing accelerator must never keep an on-premises platform from starting.
func (s *Store) prepareFullText(ctx context.Context) {
	switch s.driver {
	case "sqlite":
		s.fullText = s.prepareSQLiteFullText(ctx) == nil
	case "postgres":
		s.fullText = s.preparePostgresFullText(ctx) == nil
	default:
		s.fullText = false
	}
}

func (s *Store) prepareSQLiteFullText(ctx context.Context) error {
	// An external-content table stores only the index, reading the columns back
	// from document_chunks, so the corpus is never held twice. That also means
	// COUNT(*) on it counts the content table, not the index — the index cannot
	// be inspected for staleness, so a fresh one is filled at creation time and
	// kept current by triggers from then on.
	var existing int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_chunks_fts'`).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		// The table is in the schema, which says nothing about whether this
		// binary can read it: an index built by a binary with FTS5 outlives one
		// built without. Ask the index a question, because every statement that
		// only names the table — CREATE ... IF NOT EXISTS included — succeeds
		// without the module and would report a capability that is not there.
		var rowid int64
		err := s.DB.QueryRowContext(ctx, `SELECT rowid FROM document_chunks_fts WHERE document_chunks_fts MATCH ? LIMIT 1`, `"gitctxprobe"*`).Scan(&rowid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return s.disableSQLiteFullText(ctx, err)
		}
	}
	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS document_chunks_fts USING fts5(
			file_path, heading, content,
			content='document_chunks', content_rowid='rowid', tokenize='unicode61 remove_diacritics 2'
		)`,
		`CREATE TRIGGER IF NOT EXISTS document_chunks_fts_insert AFTER INSERT ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(rowid,file_path,heading,content) VALUES (new.rowid,new.file_path,new.heading,new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS document_chunks_fts_delete AFTER DELETE ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(document_chunks_fts,rowid,file_path,heading,content) VALUES ('delete',old.rowid,old.file_path,old.heading,old.content);
		END`,
		// Recreated rather than left alone: the first version of this trigger
		// reacted to every update, and a database carrying it would keep doing so
		// for as long as it lived because CREATE ... IF NOT EXISTS leaves what it
		// finds. Dropping a trigger loses no index state.
		`DROP TRIGGER IF EXISTS document_chunks_fts_update`,
		// Only the indexed columns are worth reacting to. Finishing a ref stamps
		// the new commit onto every chunk in it, including the ones the commit
		// did not touch, and re-indexing text that did not change costs the push
		// a second pass over the whole ref for nothing.
		`CREATE TRIGGER IF NOT EXISTS document_chunks_fts_update AFTER UPDATE OF file_path,heading,content ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(document_chunks_fts,rowid,file_path,heading,content) VALUES ('delete',old.rowid,old.file_path,old.heading,old.content);
			INSERT INTO document_chunks_fts(rowid,file_path,heading,content) VALUES (new.rowid,new.file_path,new.heading,new.content);
		END`,
	}
	for _, statement := range statements {
		if _, err := s.DB.ExecContext(ctx, statement); err != nil {
			return s.disableSQLiteFullText(ctx, err)
		}
	}
	// Either this is a new database, one upgraded from a build without FTS5, or
	// one written while the triggers were dropped. All three start with an index
	// that does not describe the table, and the only way back is a full pass.
	if existing == 0 || s.fullTextUnmaintained(ctx) {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO document_chunks_fts(document_chunks_fts) VALUES('rebuild')`); err != nil {
			return s.disableSQLiteFullText(ctx, err)
		}
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM index_maintenance WHERE name=?`, unmaintainedNote); err != nil {
			return err
		}
	}
	return nil
}

// unmaintainedNote records that the full-text index stopped being maintained
// while some binary without FTS5 was writing to this database.
const unmaintainedNote = "fulltext_unmaintained"

func (s *Store) fullTextUnmaintained(ctx context.Context) bool {
	var noted int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM index_maintenance WHERE name=?`, unmaintainedNote).Scan(&noted); err != nil {
		return false
	}
	return noted != 0
}

// disableSQLiteFullText gives up the index and, more importantly, gets out of
// the way of everything else.
//
// The triggers are part of the schema, so they survive the binary that created
// them. A binary without the module cannot run them, and a trigger that cannot
// run fails the statement that fired it — which is every insert, update and
// delete on document_chunks. Indexing stops completely, and the error names an
// SQLite module rather than anything an operator changed. Dropping the triggers
// needs no module and makes the database writable again; the index it leaves
// behind is stale, which is why that is written down for the next start.
func (s *Store) disableSQLiteFullText(ctx context.Context, cause error) error {
	for _, trigger := range []string{"document_chunks_fts_insert", "document_chunks_fts_delete", "document_chunks_fts_update"} {
		if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+trigger); err != nil {
			return cause
		}
	}
	// The virtual table itself cannot be dropped without the module, so it stays
	// in the schema, unreadable and unwritten, until a binary that has the module
	// rebuilds it.
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO index_maintenance(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, unmaintainedNote)
	return cause
}

// postgresSearchVector is the expression the generated column carries.
//
// The words have to be split the way SQLite splits them, because both are the
// same product. PostgreSQL's text-search parser recognises structure: it reads
// internal/settlement/Retry.go as one token of type "file" and indexes it
// whole, so a search for "settlement" — the ordinary way to narrow to a part of
// a repository — matched nothing on PostgreSQL while finding four files on
// SQLite, whose unicode61 tokenizer splits on every non-alphanumeric. Making
// the text alphanumeric-separated before it reaches the parser leaves the two
// tokenizers agreeing. URLs and paths stop being single tokens, which is what
// SQLite already does.
const postgresSearchVector = `to_tsvector('simple', regexp_replace(` +
	`coalesce(file_path,'') || ' ' || coalesce(heading,'') || ' ' || coalesce(content,''),` +
	` '[^[:alnum:]]+', ' ', 'g'))`

func (s *Store) preparePostgresFullText(ctx context.Context) error {
	// A stored generated column keeps the index in step with the row without a
	// trigger to maintain: PostgreSQL recomputes it on every write, including
	// the bulk insert that swaps a ref's content.
	//
	// A column already there may carry an older expression, and a generated
	// column cannot be redefined in place. Replacing it rewrites the table once,
	// which is the price of the rows it currently indexes wrongly.
	var expression string
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(generation_expression,'') FROM information_schema.columns WHERE table_name='document_chunks' AND column_name='search_vector'`).Scan(&expression)
	switch {
	case err == nil && expression != "" && !equivalentSearchVector(expression):
		if _, err = s.DB.ExecContext(ctx, `ALTER TABLE document_chunks DROP COLUMN search_vector`); err != nil {
			return err
		}
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	statements := []string{
		`ALTER TABLE document_chunks ADD COLUMN IF NOT EXISTS search_vector tsvector
		 GENERATED ALWAYS AS (` + postgresSearchVector + `) STORED`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunks_search_vector ON document_chunks USING GIN (search_vector)`,
	}
	for _, statement := range statements {
		if _, err := s.DB.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// equivalentSearchVector compares what the server reports against what this
// build wants. PostgreSQL re-renders the expression it stored — quoting and
// spacing are its own — so the two are compared with whitespace removed.
func equivalentSearchVector(stored string) bool {
	return strings.EqualFold(compactExpression(stored), compactExpression(postgresSearchVector))
}

func compactExpression(value string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, value)
}

// RebuildFullText refills the index from the chunk table. It exists for the
// case the triggers cannot cover — a database restored from a backup taken by
// a build without the index — and costs one pass over the corpus.
func (s *Store) RebuildFullText(ctx context.Context) error {
	if !s.fullText || s.driver != "sqlite" {
		// PostgreSQL maintains its column itself; there is nothing to rebuild.
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO document_chunks_fts(document_chunks_fts) VALUES('rebuild')`)
	return err
}

// FullTextQuery renders search terms as a query for the store's index. Terms
// are matched as prefixes, which keeps the behaviour close to the substring
// scan it replaces: someone searching "settle" still finds "settlement".
// It returns an empty string when there is nothing usable to look up.
func (s *Store) FullTextQuery(terms []string) string {
	if s.driver == "postgres" {
		return postgresTextQuery(terms)
	}
	return FullTextQuery(terms)
}

// postgresTextQuery renders the same terms as a tsquery: prefix matches joined
// by OR, so the caller scores whatever comes back rather than being handed only
// the rows that carry every term.
func postgresTextQuery(terms []string) string {
	cleaned := make([]string, 0, len(terms))
	for _, term := range terms {
		term = sanitizeSearchTerm(term)
		if len(term) < 2 {
			continue
		}
		cleaned = append(cleaned, term+":*")
	}
	if len(cleaned) == 0 {
		return ""
	}
	return strings.Join(cleaned, " | ")
}

// sanitizeSearchTerm strips the characters both query languages read as
// operators, so a term is always text.
func sanitizeSearchTerm(term string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\'', '*', '(', ')', ':', '^', '-', '+', '&', '|', '!', '<', '>', '\\':
			return -1
		}
		return r
	}, strings.TrimSpace(term))
}

func FullTextQuery(terms []string) string {
	cleaned := make([]string, 0, len(terms))
	for _, term := range terms {
		// The index tokenizes on non-alphanumerics, so a term carrying quotes or
		// operators would be read as query syntax rather than as text.
		term = sanitizeSearchTerm(term)
		if len(term) < 2 {
			continue
		}
		cleaned = append(cleaned, `"`+term+`"*`)
	}
	if len(cleaned) == 0 {
		return ""
	}
	// OR rather than AND: the caller scores the rows it gets back, and requiring
	// every term would drop the partial matches that scoring exists to rank.
	return strings.Join(cleaned, " OR ")
}

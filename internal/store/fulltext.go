package store

import (
	"context"
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
		`CREATE TRIGGER IF NOT EXISTS document_chunks_fts_update AFTER UPDATE ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(document_chunks_fts,rowid,file_path,heading,content) VALUES ('delete',old.rowid,old.file_path,old.heading,old.content);
			INSERT INTO document_chunks_fts(rowid,file_path,heading,content) VALUES (new.rowid,new.file_path,new.heading,new.content);
		END`,
	}
	for _, statement := range statements {
		if _, err := s.DB.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if existing == 0 {
		// Either this is a new database or one upgraded from a build without
		// FTS5. Both start with an empty index over a populated table.
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO document_chunks_fts(document_chunks_fts) VALUES('rebuild')`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) preparePostgresFullText(ctx context.Context) error {
	// A stored generated column keeps the index in step with the row without a
	// trigger to maintain: PostgreSQL recomputes it on every write, including
	// the bulk insert that swaps a ref's content.
	statements := []string{
		`ALTER TABLE document_chunks ADD COLUMN IF NOT EXISTS search_vector tsvector
		 GENERATED ALWAYS AS (to_tsvector('simple',
			coalesce(file_path,'') || ' ' || coalesce(heading,'') || ' ' || coalesce(content,''))) STORED`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunks_search_vector ON document_chunks USING GIN (search_vector)`,
	}
	for _, statement := range statements {
		if _, err := s.DB.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
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

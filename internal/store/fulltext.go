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
	default:
		// PostgreSQL keeps the scanning path for now; its tsvector index is a
		// separate migration with its own verification.
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

// RebuildFullText refills the index from the chunk table. It exists for the
// case the triggers cannot cover — a database restored from a backup taken by
// a build without the index — and costs one pass over the corpus.
func (s *Store) RebuildFullText(ctx context.Context) error {
	if !s.fullText {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO document_chunks_fts(document_chunks_fts) VALUES('rebuild')`)
	return err
}

// FullTextQuery renders search terms as a query for the store's index. Terms
// are matched as prefixes, which keeps the behaviour close to the substring
// scan it replaces: someone searching "settle" still finds "settlement".
// It returns an empty string when there is nothing usable to look up.
func FullTextQuery(terms []string) string {
	cleaned := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		// The index tokenizes on non-alphanumerics, so a term carrying quotes or
		// operators would be read as FTS syntax rather than as text.
		term = strings.Map(func(r rune) rune {
			switch r {
			case '"', '\'', '*', '(', ')', ':', '^', '-', '+':
				return -1
			}
			return r
		}, term)
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

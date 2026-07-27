package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	DB     *sql.DB
	driver string
}

func (s *Store) Driver() string { return s.driver }

func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	sqlDriver := map[string]string{"postgres": "pgx", "sqlite": "sqlite3"}[driver]
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db, driver: driver}
	if err := s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if s.driver == "postgres" {
		if _, err := s.DB.ExecContext(ctx, `SELECT pg_advisory_lock(1735358461)`); err != nil {
			return err
		}
		defer s.DB.ExecContext(context.Background(), `SELECT pg_advisory_unlock(1735358461)`)
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists int
		err := s.DB.QueryRowContext(ctx, s.Rebind(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`), name).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		sqlText := string(body)
		if s.driver == "postgres" {
			sqlText = strings.ReplaceAll(sqlText, "BLOB", "BYTEA")
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, sqlText); err == nil {
			_, err = tx.ExecContext(ctx, s.Rebind(`INSERT INTO schema_migrations(version) VALUES(?)`), name)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate %s: %w", name, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) Rebind(query string) string {
	if s.driver != "postgres" {
		return query
	}
	var n int
	for strings.Contains(query, "?") {
		n++
		query = strings.Replace(query, "?", fmt.Sprintf("$%d", n), 1)
	}
	return query
}

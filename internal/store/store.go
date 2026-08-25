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
	// fullText records whether this store has a usable full-text index. It is a
	// property of the build and the database, not of the query, so it is probed
	// once when the store opens.
	fullText bool
}

type migrationExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func (s *Store) Driver() string { return s.driver }

func DriverForDSN(dsn string) string {
	if strings.HasPrefix(strings.TrimSpace(dsn), "file:") || strings.TrimSpace(dsn) == ":memory:" {
		return "sqlite"
	}
	return "postgres"
}

// TestConnection verifies a DSN without creating or migrating application
// tables. Administrative connection tests must remain read-only.
func TestConnection(ctx context.Context, dsn string) (map[string]string, error) {
	driver := DriverForDSN(dsn)
	sqlDriver := map[string]string{"postgres": "pgx", "sqlite": "sqlite3"}[driver]
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		return nil, err
	}
	result := map[string]string{"driver": driver}
	if driver == "postgres" {
		var database, user, serverVersion string
		if err = db.QueryRowContext(ctx, `SELECT current_database(),current_user,current_setting('server_version')`).Scan(&database, &user, &serverVersion); err != nil {
			return nil, err
		}
		result["database"], result["user"], result["serverVersion"] = database, user, serverVersion
	}
	return result, nil
}

func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	sqlDriver := map[string]string{"postgres": "pgx", "sqlite": "sqlite3"}[driver]
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		// SQLite has a single writer. Serializing the application pool prevents
		// background workers and administrative setting transactions from racing
		// into SQLITE_BUSY while retaining PostgreSQL concurrency unchanged.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
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
	s.prepareFullText(ctx)
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	var executor migrationExecutor = s.DB
	if s.driver == "postgres" {
		conn, err := s.DB.Conn(ctx)
		if err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(1735358461)`); err != nil {
			conn.Close()
			return err
		}
		executor = conn
		defer func() {
			_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(1735358461)`)
			_ = conn.Close()
		}()
	}
	if _, err := executor.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
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
		err := executor.QueryRowContext(ctx, s.Rebind(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`), name).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		if name == "020_repository_source_uniqueness.sql" {
			if err := s.migrateRepositorySourceUniqueness(ctx, executor); err != nil {
				return fmt.Errorf("migrate %s: %w", name, err)
			}
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
		tx, err := executor.BeginTx(ctx, nil)
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

func (s *Store) migrateRepositorySourceUniqueness(ctx context.Context, executor migrationExecutor) error {
	if s.driver == "postgres" {
		tx, err := executor.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `ALTER TABLE repositories DROP CONSTRAINT IF EXISTS repositories_project_key_slug_key`); err == nil {
			_, err = tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS uq_repositories_source_project_slug ON repositories(source_type,project_key,slug)`)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, RebindForDriver(`INSERT INTO schema_migrations(version) VALUES(?)`, "postgres"), "020_repository_source_uniqueness.sql")
		}
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := executor.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer executor.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
	tx, err := executor.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	script := `
CREATE TABLE repositories_source_v2 (
  id TEXT PRIMARY KEY, project_key TEXT NOT NULL, slug TEXT NOT NULL,
  name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  source_type TEXT NOT NULL DEFAULT 'bitbucket',
  source_external_id TEXT NOT NULL DEFAULT '',
  library_id TEXT NOT NULL UNIQUE, default_branch TEXT NOT NULL DEFAULT 'main',
  reputation TEXT NOT NULL DEFAULT 'Medium', enabled INTEGER NOT NULL DEFAULT 1,
  indexed_at TIMESTAMP, UNIQUE(source_type, project_key, slug)
);
INSERT INTO repositories_source_v2 SELECT id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,reputation,enabled,indexed_at FROM repositories;
DROP TABLE repositories;
ALTER TABLE repositories_source_v2 RENAME TO repositories;
CREATE INDEX IF NOT EXISTS idx_repositories_source_project_slug ON repositories(source_type,project_key,slug);`
	if _, err = tx.ExecContext(ctx, script); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES(?)`, "020_repository_source_uniqueness.sql")
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func RebindForDriver(query, driver string) string {
	if driver != "postgres" {
		return query
	}
	var n int
	for strings.Contains(query, "?") {
		n++
		query = strings.Replace(query, "?", fmt.Sprintf("$%d", n), 1)
	}
	return query
}

func (s *Store) Rebind(query string) string {
	return RebindForDriver(query, s.driver)
}

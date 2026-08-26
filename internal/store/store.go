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

// sqliteDefaults supplies the connection parameters the schema depends on but
// the DSN is not required to carry.
//
// Foreign keys are the one that matters. SQLite disables them per connection
// unless the DSN says otherwise, and every ON DELETE CASCADE in this schema
// then does nothing: deleting a repository leaves its permissions, files,
// chunks and symbols; deleting a notification leaves its deliveries; a row
// naming a parent that does not exist inserts happily.
//
// What made it hard to see is that it was not consistently wrong. The migration
// that rebuilds the repositories table turns foreign keys off and back on, and
// that PRAGMA sticks to the single pooled connection — so an installation whose
// DSN omitted the parameter enforced its constraints on the boot that migrated
// and stopped enforcing them on the next restart. The README documents the full
// DSN; nothing required it, and nothing said which one was running.
//
// A parameter the operator did set is left exactly as they set it.
func sqliteDefaults(dsn string) string {
	if !strings.HasPrefix(dsn, "file:") {
		// A bare ":memory:" or a plain path takes no query parameters in this
		// form; rewriting it here would change which file is opened.
		return dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	for parameter, value := range map[string]string{"_foreign_keys": "on", "_busy_timeout": "5000"} {
		if strings.Contains(dsn, parameter+"=") {
			continue
		}
		dsn += separator + parameter + "=" + value
		separator = "&"
	}
	return dsn
}

func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	sqlDriver := map[string]string{"postgres": "pgx", "sqlite": "sqlite3"}[driver]
	if driver == "sqlite" {
		dsn = sqliteDefaults(dsn)
	}
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

// SortText renders a text expression so both databases sort it the same way.
//
// SQLite compares text byte by byte. PostgreSQL compares it by the locale the
// database was created with, and en_US.utf8 — the common default — ignores
// case and punctuation, so README.md sorts after package.json there and before
// it on SQLite. An agent that reads the first result therefore gets a different
// answer depending on the database, and two PostgreSQL installations created
// with different locales disagree with each other as well.
//
// "C" is byte order, which is what SQLite already does and what no locale can
// change. It is not more correct than the alternative; it is the same
// everywhere, which is what an ordering used to rank an answer has to be.
func (s *Store) SortText(expression string) string {
	if s.driver == "postgres" {
		return expression + ` COLLATE "C"`
	}
	return expression
}

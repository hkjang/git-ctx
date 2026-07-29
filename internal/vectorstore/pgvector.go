package vectorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var errExtensionNotEnabled = errors.New("pgvector is not enabled in the connected database")

// postgresStore keeps vectors in a pgvector column. It uses the pgx driver that
// the platform database already registers, and passes vectors as text literals,
// so no additional dependency is required for an offline build.
type postgresStore struct {
	db               *sql.DB
	table            string
	dimensions       int
	extensionSchema  string
	extensionVersion string
	timeout          func() (context.Context, context.CancelFunc)
}

func newPostgres(cfg Config, fallbackDSN string) (Store, error) {
	dsn := cfg.DSN
	if dsn == "" {
		dsn = fallbackDSN
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("pgvector needs a PostgreSQL DSN; set one in the vector setting or run the platform on PostgreSQL")
	}
	table, err := identifier(cfg.Collection)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// The vector store is a side channel; a small pool keeps it from competing
	// with the metadata database for connections.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	timeout := cfg.timeout()
	return &postgresStore{db: db, table: table, dimensions: cfg.Dimensions, timeout: func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), timeout)
	}}, nil
}

func (p *postgresStore) Name() string { return "pgvector" }

func (p *postgresStore) Ensure(ctx context.Context, dimensions int) error {
	if err := p.activateExtension(ctx); err != nil {
		return err
	}
	if dimensions <= 0 {
		dimensions = p.dimensions
	}
	if dimensions <= 0 {
		return errors.New("vector dimensions are unknown; index a repository first or set dimensions explicitly")
	}
	p.dimensions = dimensions
	vectorType := quoteIdentifier(p.extensionSchema) + ".vector"
	create := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
  id TEXT PRIMARY KEY,
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  library_id TEXT NOT NULL DEFAULT '',
  file_path TEXT NOT NULL DEFAULT '',
  embedding %s(%d) NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`, p.table, vectorType, dimensions)
	if _, err := p.db.ExecContext(ctx, create); err != nil {
		return err
	}
	if _, err := p.db.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_repo_ref ON %s (repository_id, ref_name)`, p.table, p.table)); err != nil {
		return err
	}
	// HNSW gives good recall without tuning a list count. Older pgvector builds
	// do not have it, and the table still works with an exact scan, so an index
	// failure is not fatal.
	_, _ = p.db.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_embedding ON %s USING hnsw (embedding %s.vector_cosine_ops)`, p.table, p.table, quoteIdentifier(p.extensionSchema)))
	return nil
}

func (p *postgresStore) Upsert(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if err := p.Ensure(ctx, len(chunks[0].Vector)); err != nil {
		return err
	}
	vectorType := quoteIdentifier(p.extensionSchema) + ".vector"
	for start := 0; start < len(chunks); start += upsertBatch {
		end := min(start+upsertBatch, len(chunks))
		batch := chunks[start:end]
		var values strings.Builder
		args := make([]any, 0, len(batch)*6)
		for index, chunk := range batch {
			if index > 0 {
				values.WriteByte(',')
			}
			base := index * 6
			fmt.Fprintf(&values, "($%d,$%d,$%d,$%d,$%d,$%d::%s)", base+1, base+2, base+3, base+4, base+5, base+6, vectorType)
			args = append(args, chunk.ID, chunk.RepositoryID, chunk.Ref, chunk.LibraryID, chunk.FilePath, literal(chunk.Vector))
		}
		statement := fmt.Sprintf(`INSERT INTO %s (id,repository_id,ref_name,library_id,file_path,embedding) VALUES %s
ON CONFLICT (id) DO UPDATE SET repository_id=excluded.repository_id,ref_name=excluded.ref_name,library_id=excluded.library_id,file_path=excluded.file_path,embedding=excluded.embedding,updated_at=now()`,
			p.table, values.String())
		if _, err := p.db.ExecContext(ctx, statement, args...); err != nil {
			return err
		}
	}
	return nil
}

func (p *postgresStore) DeleteRef(ctx context.Context, repositoryID, ref string) error {
	_, err := p.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE repository_id=$1 AND ref_name=$2`, p.table), repositoryID, ref)
	return err
}

func (p *postgresStore) Search(ctx context.Context, repositoryID, ref string, vector []float32, limit int) ([]Match, error) {
	if len(vector) == 0 {
		return nil, errors.New("query vector is empty")
	}
	if limit < 1 {
		limit = 20
	}
	if err := p.loadExtension(ctx); err != nil {
		return nil, err
	}
	schema, vectorType := quoteIdentifier(p.extensionSchema), quoteIdentifier(p.extensionSchema)+".vector"
	// `<=>` is cosine distance in pgvector, so similarity is 1 - distance.
	statement := fmt.Sprintf(`SELECT id, 1 - (embedding OPERATOR(%s.<=>) $1::%s) AS score FROM %s
WHERE repository_id=$2 AND ref_name=$3 ORDER BY embedding OPERATOR(%s.<=>) $1::%s LIMIT %d`, schema, vectorType, p.table, schema, vectorType, limit)
	rows, err := p.db.QueryContext(ctx, statement, literal(vector), repositoryID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Match
	for rows.Next() {
		var match Match
		if err = rows.Scan(&match.ID, &match.Score); err != nil {
			return nil, err
		}
		out = append(out, match)
	}
	return out, rows.Err()
}

func (p *postgresStore) SearchGlobal(ctx context.Context, vector []float32, limit int) ([]Match, error) {
	if len(vector) == 0 {
		return nil, errors.New("query vector is empty")
	}
	if limit < 1 {
		limit = 50
	}
	if err := p.loadExtension(ctx); err != nil {
		return nil, err
	}
	schema, vectorType := quoteIdentifier(p.extensionSchema), quoteIdentifier(p.extensionSchema)+".vector"
	statement := fmt.Sprintf(`SELECT id, 1 - (embedding OPERATOR(%s.<=>) $1::%s) AS score FROM %s ORDER BY embedding OPERATOR(%s.<=>) $1::%s LIMIT %d`, schema, vectorType, p.table, schema, vectorType, limit)
	rows, err := p.db.QueryContext(ctx, statement, literal(vector))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Match
	for rows.Next() {
		var match Match
		if err = rows.Scan(&match.ID, &match.Score); err != nil {
			return nil, err
		}
		out = append(out, match)
	}
	return out, rows.Err()
}

func (p *postgresStore) Status(ctx context.Context) (Status, error) {
	return p.statusContext(ctx, nil)
}

func (p *postgresStore) statusContext(ctx context.Context, cause error) (Status, error) {
	status := Status{Provider: "pgvector", Collection: p.table, Dimensions: p.dimensions}
	var version string
	if err := p.db.QueryRowContext(ctx, `SELECT current_setting('server_version'), current_database(), current_user`).Scan(&version, &status.Database, &status.User); err != nil {
		return status, err
	}
	status.Target = "PostgreSQL " + version
	if err := p.loadExtension(ctx); err != nil {
		if !errors.Is(err, errExtensionNotEnabled) {
			status.Detail = fmt.Sprintf("could not inspect pgvector in database %q as user %q", status.Database, status.User)
			return status, fmt.Errorf("%s: %w", status.Detail, err)
		}
		var available string
		availableErr := p.db.QueryRowContext(ctx, `SELECT default_version FROM pg_available_extensions WHERE name='vector'`).Scan(&available)
		if availableErr == nil {
			status.Detail = fmt.Sprintf("pgvector %s is available on the server but is not enabled in database %q for user %q", available, status.Database, status.User)
		} else {
			status.Detail = fmt.Sprintf("pgvector is not available to database %q on this PostgreSQL server", status.Database)
		}
		if cause != nil {
			return status, fmt.Errorf("%s: %w", status.Detail, cause)
		}
		return status, errors.New(status.Detail)
	}
	status.ExtensionSchema = p.extensionSchema
	status.ExtensionVersion = p.extensionVersion
	if cause != nil {
		status.Detail = cause.Error()
		return status, cause
	}
	// A missing table simply means nothing has been projected yet.
	if err := p.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, p.table)).Scan(&status.Vectors); err != nil {
		status.Detail = "pgvector is connected; collection is not created yet and will be created on the first projection"
		status.Ready = true
		return status, nil
	}
	status.Ready = true
	status.Detail = "connected"
	return status, nil
}

func (p *postgresStore) loadExtension(ctx context.Context) error {
	err := p.db.QueryRowContext(ctx, `SELECT n.nspname, e.extversion
FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
WHERE e.extname='vector'`).Scan(&p.extensionSchema, &p.extensionVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return errExtensionNotEnabled
	}
	return err
}

func (p *postgresStore) activateExtension(ctx context.Context) error {
	if err := p.loadExtension(ctx); err == nil {
		return nil
	} else if !errors.Is(err, errExtensionNotEnabled) {
		return fmt.Errorf("inspect pgvector extension: %w", err)
	}
	var available string
	if err := p.db.QueryRowContext(ctx, `SELECT default_version FROM pg_available_extensions WHERE name='vector'`).Scan(&available); err != nil {
		return errors.New("pgvector server package is not available to this PostgreSQL instance")
	}
	if _, err := p.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		var database, user string
		_ = p.db.QueryRowContext(ctx, `SELECT current_database(), current_user`).Scan(&database, &user)
		return fmt.Errorf("pgvector %s is installed on the server but could not be enabled in database %q as user %q (connect to the intended database and grant extension creation permission): %w", available, database, user, err)
	}
	if err := p.loadExtension(ctx); err != nil {
		return fmt.Errorf("pgvector activation completed but the extension is still not visible in the connected database: %w", err)
	}
	return nil
}

func (p *postgresStore) Close() error { return p.db.Close() }

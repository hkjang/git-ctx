package vectorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// postgresStore keeps vectors in a pgvector column. It uses the pgx driver that
// the platform database already registers, and passes vectors as text literals,
// so no additional dependency is required for an offline build.
type postgresStore struct {
	db         *sql.DB
	table      string
	dimensions int
	timeout    func() (context.Context, context.CancelFunc)
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
	if dimensions <= 0 {
		dimensions = p.dimensions
	}
	if dimensions <= 0 {
		return errors.New("vector dimensions are unknown; index a repository first or set dimensions explicitly")
	}
	p.dimensions = dimensions
	if _, err := p.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return fmt.Errorf("the pgvector extension is unavailable: %w", err)
	}
	create := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
  id TEXT PRIMARY KEY,
  repository_id TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  library_id TEXT NOT NULL DEFAULT '',
  file_path TEXT NOT NULL DEFAULT '',
  embedding vector(%d) NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`, p.table, dimensions)
	if _, err := p.db.ExecContext(ctx, create); err != nil {
		return err
	}
	if _, err := p.db.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_repo_ref ON %s (repository_id, ref_name)`, p.table, p.table)); err != nil {
		return err
	}
	// HNSW gives good recall without tuning a list count. Older pgvector builds
	// do not have it, and the table still works with an exact scan, so an index
	// failure is not fatal.
	_, _ = p.db.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_embedding ON %s USING hnsw (embedding vector_cosine_ops)`, p.table, p.table))
	return nil
}

func (p *postgresStore) Upsert(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if err := p.Ensure(ctx, len(chunks[0].Vector)); err != nil {
		return err
	}
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
			fmt.Fprintf(&values, "($%d,$%d,$%d,$%d,$%d,$%d::vector)", base+1, base+2, base+3, base+4, base+5, base+6)
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
	// `<=>` is cosine distance in pgvector, so similarity is 1 - distance.
	statement := fmt.Sprintf(`SELECT id, 1 - (embedding <=> $1::vector) AS score FROM %s
WHERE repository_id=$2 AND ref_name=$3 ORDER BY embedding <=> $1::vector LIMIT %d`, p.table, limit)
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
	statement := fmt.Sprintf(`SELECT id, 1 - (embedding <=> $1::vector) AS score FROM %s ORDER BY embedding <=> $1::vector LIMIT %d`, p.table, limit)
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
	status := Status{Provider: "pgvector", Collection: p.table, Dimensions: p.dimensions}
	var version string
	if err := p.db.QueryRowContext(ctx, `SELECT current_setting('server_version')`).Scan(&version); err != nil {
		return status, err
	}
	status.Target = "PostgreSQL " + version
	var installed int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_extension WHERE extname='vector'`).Scan(&installed); err != nil {
		return status, err
	}
	if installed == 0 {
		status.Detail = "the pgvector extension is not installed in this database"
		return status, errors.New(status.Detail)
	}
	// A missing table simply means nothing has been projected yet.
	if err := p.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, p.table)).Scan(&status.Vectors); err != nil {
		status.Detail = "collection is not created yet; it is created on the first projection"
		status.Ready = true
		return status, nil
	}
	status.Ready = true
	status.Detail = "connected"
	return status, nil
}

func (p *postgresStore) Close() error { return p.db.Close() }

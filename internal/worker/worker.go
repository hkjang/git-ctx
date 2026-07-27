package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git-ctx/internal/embedding"
	"git-ctx/internal/indexer"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type SourceFactory func(context.Context, string) (source.RepositorySource, error)
type EmbeddingFactory func(context.Context) (embedding.Provider, error)
type Projection func(context.Context, string, string) error
type Worker struct {
	store            *store.Store
	indexer          *indexer.Indexer
	factory          SourceFactory
	poll             time.Duration
	maxAttempts      int
	embeddingFactory EmbeddingFactory
	projection       Projection
}

func (w *Worker) SetEmbeddingFactory(factory EmbeddingFactory) { w.embeddingFactory = factory }
func (w *Worker) SetProjection(projection Projection)          { w.projection = projection }

func New(s *store.Store, idx *indexer.Indexer, f SourceFactory) *Worker {
	return &Worker{store: s, indexer: idx, factory: f, poll: 2 * time.Second, maxAttempts: 5}
}

type job struct {
	ID, RepositoryID, RefName string
	Attempts                  int
}
type repository struct {
	source.Repository
	SourceType string
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		_, _ = w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	j, ok, err := w.claim(ctx)
	if err != nil || !ok {
		return ok, err
	}
	err = w.execute(ctx, j)
	if err == nil {
		_, err = w.store.DB.ExecContext(ctx, w.store.Rebind(`UPDATE index_jobs SET status='completed',error_message='',completed_at=? WHERE id=?`), time.Now().UTC(), j.ID)
		return true, err
	}
	delay := time.Duration(1<<min(j.Attempts, 8)) * time.Second
	status := "pending"
	var completed any
	if j.Attempts >= w.maxAttempts {
		status = "failed"
		completed = time.Now().UTC()
	}
	_, updateErr := w.store.DB.ExecContext(ctx, w.store.Rebind(`UPDATE index_jobs SET status=?,error_message=?,next_run_at=?,completed_at=? WHERE id=?`), status, truncate(err.Error(), 1000), time.Now().UTC().Add(delay), completed, j.ID)
	if updateErr != nil {
		return true, updateErr
	}
	return true, err
}
func (w *Worker) claim(ctx context.Context) (job, bool, error) {
	var j job
	if w.store.Driver() == "postgres" {
		tx, err := w.store.DB.BeginTx(ctx, nil)
		if err != nil {
			return job{}, false, err
		}
		defer tx.Rollback()
		err = tx.QueryRowContext(ctx, `SELECT id,repository_id,ref_name,attempts FROM index_jobs WHERE status='pending' AND next_run_at<=CURRENT_TIMESTAMP ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&j.ID, &j.RepositoryID, &j.RefName, &j.Attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return job{}, false, nil
		}
		if err != nil {
			return job{}, false, err
		}
		j.Attempts++
		result, err := tx.ExecContext(ctx, `UPDATE index_jobs SET status='running',attempts=$1,started_at=CURRENT_TIMESTAMP WHERE id=$2 AND status='pending'`, j.Attempts, j.ID)
		if err != nil {
			return job{}, false, err
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return job{}, false, errors.New("index job claim lost")
		}
		if err = tx.Commit(); err != nil {
			return job{}, false, err
		}
		return j, true, nil
	}
	err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`UPDATE index_jobs SET status='running',attempts=attempts+1,started_at=? WHERE id=(SELECT id FROM index_jobs WHERE status='pending' AND next_run_at<=? ORDER BY created_at LIMIT 1) RETURNING id,repository_id,ref_name,attempts`), time.Now().UTC(), time.Now().UTC()).Scan(&j.ID, &j.RepositoryID, &j.RefName, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return job{}, false, nil
	}
	return j, err == nil, err
}
func (w *Worker) execute(ctx context.Context, j job) (err error) {
	ctx, span := otel.Tracer("git-ctx/indexer").Start(ctx, "index.job",
		oteltrace.WithAttributes(attribute.String("git_ctx.index_job.id", j.ID), attribute.String("git_ctx.repository.id", j.RepositoryID), attribute.String("git_ctx.ref.name", j.RefName), attribute.Int("git_ctx.index_job.attempt", j.Attempts)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "index job failed")
		}
		span.End()
	}()
	var r repository
	var external string
	err = w.store.DB.QueryRowContext(ctx, w.store.Rebind(`SELECT source_type,source_external_id,project_key,slug,name,description,default_branch FROM repositories WHERE id=? AND enabled=1`), j.RepositoryID).Scan(&r.SourceType, &external, &r.ProjectKey, &r.Slug, &r.Name, &r.Description, &r.DefaultBranch)
	if err != nil {
		return fmt.Errorf("load repository: %w", err)
	}
	r.ID, _ = strconv.ParseInt(external, 10, 64)
	adapter, err := w.factory(ctx, r.SourceType)
	if err != nil {
		return err
	}
	refName := j.RefName
	if refName == "" {
		refName = r.DefaultBranch
	}
	refs, err := adapter.ListBranches(ctx, source.RepositoryRef{ProjectKey: r.ProjectKey, Slug: r.Slug})
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}
	tags, tagErr := adapter.ListTags(ctx, source.RepositoryRef{ProjectKey: r.ProjectKey, Slug: r.Slug})
	if tagErr == nil {
		refs = append(refs, tags...)
	}
	var selected *source.Reference
	for n := range refs {
		if refs[n].Name == refName {
			selected = &refs[n]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("source ref %q does not exist", refName)
	}
	activeIndexer := w.indexer
	policy := indexer.DefaultPolicy()
	var extensions, excludes string
	var maxBytes int64
	if err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`SELECT include_extensions,exclude_prefixes,max_file_bytes FROM repository_index_policies WHERE repository_id=?`), j.RepositoryID).Scan(&extensions, &excludes, &maxBytes); err == nil {
		policy = indexer.Policy{IncludeExtensions: split(extensions), ExcludePrefixes: split(excludes), MaxFileBytes: maxBytes}
		activeIndexer = indexer.New(w.store, policy)
	}
	if w.embeddingFactory != nil {
		provider, err := w.embeddingFactory(ctx)
		if err != nil {
			return err
		}
		activeIndexer = indexer.NewWithEmbedder(w.store, policy, provider)
	}
	if err := activeIndexer.ApplyPendingJob(ctx, adapter, r.SourceType, r.Repository, []source.Reference{*selected}); err != nil {
		return err
	}
	if w.projection != nil {
		if err := w.projection(ctx, j.RepositoryID, selected.Name); err != nil {
			return fmt.Errorf("search projection: %w", err)
		}
	}
	return nil
}
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
func split(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

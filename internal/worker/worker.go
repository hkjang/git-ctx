package worker

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"git-ctx/internal/embedding"
	"git-ctx/internal/indexer"
	"git-ctx/internal/search"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type SourceFactory func(context.Context, string) (source.RepositorySource, error)

// SourceHealth is the circuit breaker the search path already uses. Indexing
// consults the same state: hammering a source server that is down turns a ten
// minute outage into hundreds of permanently failed jobs that an administrator
// then has to retry by hand.
type SourceHealth interface {
	Allow(sourceType string) (bool, string)
	Report(sourceType string, err error)
}
type EmbeddingFactory func(context.Context) (embedding.Provider, error)
type RetrievalModeLoader func(context.Context) string
type Projection func(context.Context, string, string) error
type Worker struct {
	store            *store.Store
	indexer          *indexer.Indexer
	factory          SourceFactory
	poll             time.Duration
	maxAttempts      int
	embeddingFactory EmbeddingFactory
	retrievalMode    RetrievalModeLoader
	projection       Projection
	lease            time.Duration
	timeout          time.Duration
	health           SourceHealth
	// identity names this instance in the job it claims. Replicas share one
	// queue, and a job stuck in 'running' used to say when it started and
	// nothing about where.
	identity string
}

// SetSourceHealth installs the shared circuit breaker registry.
func (w *Worker) SetSourceHealth(health SourceHealth) { w.health = health }

func (w *Worker) SetEmbeddingFactory(factory EmbeddingFactory) { w.embeddingFactory = factory }
func (w *Worker) SetRetrievalModeLoader(loader RetrievalModeLoader) {
	w.retrievalMode = loader
}
func (w *Worker) SetProjection(projection Projection) { w.projection = projection }

func New(s *store.Store, idx *indexer.Indexer, f SourceFactory) *Worker {
	return &Worker{store: s, indexer: idx, factory: f, poll: 2 * time.Second, maxAttempts: 5,
		lease: JobLeaseDuration, timeout: jobTimeout, identity: instanceIdentity()}
}

// instanceIdentity is what one running copy of this platform calls itself. The
// host names the pod and the pid separates two copies on one machine; a random
// suffix keeps two pods with the same name in a restarted deployment apart.
func instanceIdentity() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Sprintf("%s/%d", host, os.Getpid())
	}
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(suffix))
}

// SetIdentity overrides the name this worker claims jobs under.
func (w *Worker) SetIdentity(name string) {
	if strings.TrimSpace(name) != "" {
		w.identity = name
	}
}

const (
	// jobTimeout bounds one index run so a single unresponsive source server
	// cannot block every other repository behind it.
	jobTimeout = 30 * time.Minute
	// JobLeaseDuration is deliberately longer than jobTimeout. With multiple
	// replicas a shorter lease lets a second worker claim a healthy long-running
	// job while the first worker is still writing its result.
	JobLeaseDuration = jobTimeout + 5*time.Minute
)

type job struct {
	ID, RepositoryID, RefName string
	Attempts                  int
	StartedAt                 time.Time
}
type repository struct {
	source.Repository
	SourceType string
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		_, _ = w.RecoverStaleJobs(ctx)
		_, _ = w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RecoverStaleJobs requeues jobs whose lease expired. It reports how many rows
// were recovered so operators can see that indexing resumed by itself.
func (w *Worker) RecoverStaleJobs(ctx context.Context) (int64, error) {
	lease := w.lease
	if lease <= 0 {
		lease = JobLeaseDuration
	}
	result, err := w.store.DB.ExecContext(ctx, w.store.Rebind(
		`UPDATE index_jobs SET status='pending',next_run_at=?,error_message=? WHERE status='running' AND started_at IS NOT NULL AND started_at<?`),
		time.Now().UTC(), "Requeued automatically: the previous run stopped without finishing (service restart or stalled source call).", time.Now().UTC().Add(-lease))
	if err != nil {
		return 0, err
	}
	recovered, _ := result.RowsAffected()
	return recovered, nil
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	j, ok, err := w.claim(ctx)
	if err != nil || !ok {
		return ok, err
	}
	// A paused source is an outage, not a broken repository: the job goes back to
	// the queue with its attempt returned, so a long outage costs waiting time
	// instead of the whole retry budget of every repository.
	sourceType, hasSource := w.repositorySource(ctx, j.RepositoryID)
	if hasSource && w.health != nil {
		if allowed, reason := w.health.Allow(sourceType); !allowed {
			return true, w.requeuePaused(ctx, j, reason)
		}
	}
	timeout := w.timeout
	if timeout <= 0 {
		timeout = jobTimeout
	}
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	err = w.execute(jobCtx, j)
	cancel()
	// A shutdown cancels the worker context before this claimed job can record
	// its outcome. Use a short detached context for the state transition so the
	// job is not stranded as `running` until the lease expires after restart.
	stateCtx := ctx
	stateCancel := func() {}
	if ctx.Err() != nil {
		stateCtx, stateCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	}
	defer stateCancel()
	// Indexing is the heaviest user of the source API, so its outcome is the best
	// early signal for the breaker that the search path also reads. Only outages
	// count: a missing repository or a policy that indexes nothing says nothing
	// about the server.
	if hasSource && w.health != nil {
		w.health.Report(sourceType, outageOnly(err))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("index job exceeded the %s limit; check source server responsiveness, repository size and the embedding endpoint: %w", timeout, err)
	}
	if err == nil {
		// error_message keeps the indexer's skip warning; only the status changes.
		// outage_since is cleared: whatever was wrong with the source is over.
		result, updateErr := w.store.DB.ExecContext(stateCtx, w.store.Rebind(`UPDATE index_jobs SET status='completed',completed_at=?,outage_since=NULL WHERE id=? AND status='running' AND started_at=?`), time.Now().UTC(), j.ID, j.StartedAt)
		if updateErr != nil {
			return true, updateErr
		}
		return true, requireOwnedLease(result)
	}
	delay := time.Duration(1<<min(j.Attempts, 8)) * time.Second
	status := "pending"
	var completed any
	// An outage is not this repository's fault. It is retried on a longer delay
	// without spending an attempt, so the queue survives a source restart, while
	// a genuine indexing error still exhausts its budget and surfaces as failed.
	outage := sourceOutage(err)
	if outage {
		if j.Attempts > 0 {
			j.Attempts--
		}
		if errors.Is(err, context.Canceled) {
			delay = 0
		} else if delay < 30*time.Second {
			delay = 30 * time.Second
		}
	}
	if !outage && j.Attempts >= w.maxAttempts {
		status = "failed"
		completed = time.Now().UTC()
	}
	// An outage that has been going on for hours is not the transient one this
	// exemption was written for. The retry budget is still not spent — giving up
	// on a repository because its server is down would be worse — but the job
	// records when the trouble started, and once it has lasted long enough
	// somebody is told.
	outageSince := "outage_since"
	if !outage {
		outageSince = "NULL"
	} else {
		outageSince = "COALESCE(outage_since,?)"
	}
	arguments := []any{status, j.Attempts, truncate(err.Error(), 1000), time.Now().UTC().Add(delay), completed}
	if outage {
		arguments = append(arguments, time.Now().UTC())
	}
	arguments = append(arguments, j.ID, j.StartedAt)
	result, updateErr := w.store.DB.ExecContext(stateCtx, w.store.Rebind(
		`UPDATE index_jobs SET status=?,attempts=?,error_message=?,next_run_at=?,completed_at=?,outage_since=`+outageSince+
			` WHERE id=? AND status='running' AND started_at=?`), arguments...)
	if updateErr != nil {
		return true, updateErr
	}
	if leaseErr := requireOwnedLease(result); leaseErr != nil {
		return true, leaseErr
	}
	if status == "failed" {
		w.notifyFailure(stateCtx, j, err)
	} else if outage {
		w.notifyPersistentOutage(stateCtx, j, err)
	}
	return true, err
}

// OutageReportedAfter is how long a source has to keep failing before the
// silence is broken. The retries are cheap and a restart takes minutes; an hour
// of them is a repository that has stopped updating and nobody has been told.
const OutageReportedAfter = time.Hour

// notifyPersistentOutage tells the operators once about a job whose source has
// been failing for longer than a restart takes. The notification is keyed on
// the repository and ref, so a job retrying every thirty seconds produces one
// message rather than a hundred and twenty an hour.
func (w *Worker) notifyPersistentOutage(ctx context.Context, j job, cause error) {
	var since sql.NullTime
	if err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`SELECT outage_since FROM index_jobs WHERE id=?`), j.ID).Scan(&since); err != nil {
		return
	}
	if !since.Valid || time.Since(since.Time) < OutageReportedAfter {
		return
	}
	var libraryID string
	if err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`SELECT library_id FROM repositories WHERE id=?`), j.RepositoryID).Scan(&libraryID); err != nil {
		libraryID = j.RepositoryID
	}
	title := "Repository has not indexed for hours"
	message := fmt.Sprintf("%s (%s) has been retrying since %s because its source keeps failing: %s. Indexing is not giving up, but the answers about this repository are as old as that.",
		libraryID, j.RefName, since.Time.UTC().Format(time.RFC3339), truncate(cause.Error(), 300))
	_, _ = w.store.DB.ExecContext(ctx, w.store.Rebind(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message)
SELECT ? || '-' || u.id,u.id,'index_source_outage',?,?,? FROM users u JOIN user_roles r ON r.user_id=u.id
WHERE u.status='active' AND r.role_code IN ('platform-admin','source-admin')
ON CONFLICT(user_id,notification_type,resource_id) DO UPDATE SET title=excluded.title,message=excluded.message,read_at=NULL,created_at=CURRENT_TIMESTAMP`),
		fmt.Sprintf("%d", time.Now().UnixNano()), j.RepositoryID+":"+j.RefName, title, message)
}

// outageOnly keeps repository-specific failures out of the breaker: a policy
// that indexes nothing or a missing ref says nothing about the server.
func outageOnly(err error) error {
	if sourceOutage(err) {
		return err
	}
	return nil
}

// repositorySource returns the source type of a repository.
func (w *Worker) repositorySource(ctx context.Context, repositoryID string) (string, bool) {
	var sourceType string
	if err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`SELECT source_type FROM repositories WHERE id=?`), repositoryID).Scan(&sourceType); err != nil {
		return "", false
	}
	return sourceType, sourceType != ""
}

// requeuePaused puts a job back without consuming an attempt and records why it
// is waiting, so the operations screen shows "waiting for the connector" rather
// than an unexplained idle queue.
func (w *Worker) requeuePaused(ctx context.Context, j job, reason string) error {
	attempts := j.Attempts
	if attempts > 0 {
		attempts--
	}
	result, err := w.store.DB.ExecContext(ctx, w.store.Rebind(`UPDATE index_jobs SET status='pending',attempts=?,error_message=?,next_run_at=? WHERE id=? AND status='running' AND started_at=?`),
		attempts, truncate("소스 연동 일시 중단으로 대기 중: "+reason, 1000), time.Now().UTC().Add(pausedRequeueDelay), j.ID, j.StartedAt)
	if err != nil {
		return err
	}
	return requireOwnedLease(result)
}

// pausedRequeueDelay is how long a job waits while its source is paused. It is
// longer than the breaker window so the queue does not spin.
const pausedRequeueDelay = 45 * time.Second

var errJobLeaseLost = errors.New("index job lease is no longer owned by this worker")

func requireOwnedLease(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errJobLeaseLost
	}
	return nil
}

// sourceOutage reports whether the failure is the source server being
// unavailable rather than a problem with this repository.
func sourceOutage(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, source.ErrNotConfigured) {
		return false
	}
	if errors.Is(err, context.Canceled) || source.StatusOf(err) == http.StatusUnauthorized {
		return true
	}
	if status := source.StatusOf(err); status > 0 {
		return source.RetryableStatus(status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"connection refused", "no such host", "connection reset", "i/o timeout", "eof", "tls handshake"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// notifyFailure tells the administrators that a repository stopped indexing.
// The operations screen shows it too, but nobody watches a screen all day and a
// silently failed repository is exactly what makes searches look broken.
func (w *Worker) notifyFailure(ctx context.Context, j job, cause error) {
	var libraryID string
	if err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`SELECT library_id FROM repositories WHERE id=?`), j.RepositoryID).Scan(&libraryID); err != nil {
		libraryID = j.RepositoryID
	}
	title := "Repository indexing failed"
	message := fmt.Sprintf("%s (%s) stopped after %d attempts: %s", libraryID, j.RefName, j.Attempts, truncate(cause.Error(), 400))
	// One notification per repository and ref; a later success or a manual retry
	// creates a new job id, so operators are not spammed by the retry loop.
	_, _ = w.store.DB.ExecContext(ctx, w.store.Rebind(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message)
SELECT ? || '-' || u.id,u.id,'index_job_failed',?,?,? FROM users u JOIN user_roles r ON r.user_id=u.id
WHERE u.status='active' AND r.role_code IN ('platform-admin','source-admin')
ON CONFLICT(user_id,notification_type,resource_id) DO UPDATE SET title=excluded.title,message=excluded.message,read_at=NULL,created_at=CURRENT_TIMESTAMP`),
		fmt.Sprintf("%d", time.Now().UnixNano()), j.RepositoryID+":"+j.RefName, title, message)
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
		err = tx.QueryRowContext(ctx, `UPDATE index_jobs SET status='running',attempts=$1,started_at=CURRENT_TIMESTAMP,claimed_by=$2 WHERE id=$3 AND status='pending' RETURNING started_at`, j.Attempts, w.identity, j.ID).Scan(&j.StartedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return job{}, false, errors.New("index job claim lost")
		}
		if err != nil {
			return job{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return job{}, false, err
		}
		return j, true, nil
	}
	claimedAt := time.Now().UTC()
	err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`UPDATE index_jobs SET status='running',attempts=attempts+1,started_at=?,claimed_by=? WHERE id=(SELECT id FROM index_jobs WHERE status='pending' AND next_run_at<=? ORDER BY created_at LIMIT 1) RETURNING id,repository_id,ref_name,attempts,started_at`), claimedAt, w.identity, claimedAt).Scan(&j.ID, &j.RepositoryID, &j.RefName, &j.Attempts, &j.StartedAt)
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
		return fmt.Errorf("%s connector: %w (check the %s setting)", r.SourceType, err, r.SourceType)
	}
	refName := j.RefName
	if refName == "" {
		refName = r.DefaultBranch
	}
	refs, err := adapter.ListBranches(ctx, source.RepositoryRef{ProjectKey: r.ProjectKey, Slug: r.Slug})
	if err != nil {
		return fmt.Errorf("list branches for %s/%s: %w", r.ProjectKey, r.Slug, err)
	}
	var selected *source.Reference
	for n := range refs {
		if refs[n].Name == refName {
			selected = &refs[n]
			break
		}
	}
	// Most jobs target a branch. Only query the tag endpoint when the ref was
	// not found among branches: this removes one remote request from the common
	// path and, for tag jobs, keeps a tag API outage from being misreported as a
	// permanently missing ref.
	if selected == nil {
		tags, tagErr := adapter.ListTags(ctx, source.RepositoryRef{ProjectKey: r.ProjectKey, Slug: r.Slug})
		if tagErr != nil {
			return fmt.Errorf("list tags for %s/%s: %w", r.ProjectKey, r.Slug, tagErr)
		}
		refs = append(refs, tags...)
		for n := range tags {
			if tags[n].Name == refName {
				selected = &tags[n]
				break
			}
		}
	}
	if selected == nil {
		available := make([]string, 0, min(len(refs), 8))
		for index := range refs {
			if index == 8 {
				break
			}
			available = append(available, refs[index].Name)
		}
		return fmt.Errorf("source ref %q does not exist in %s/%s; available refs include %s", refName, r.ProjectKey, r.Slug, strings.Join(available, ", "))
	}
	policy := indexer.DefaultPolicy()
	var extensions, excludes string
	var maxBytes int64
	if err := w.store.DB.QueryRowContext(ctx, w.store.Rebind(`SELECT include_extensions,exclude_prefixes,max_file_bytes FROM repository_index_policies WHERE repository_id=?`), j.RepositoryID).Scan(&extensions, &excludes, &maxBytes); err == nil {
		policy = indexer.Policy{IncludeExtensions: split(extensions), ExcludePrefixes: split(excludes), MaxFileBytes: maxBytes}
	}
	mode := search.RetrievalHybridRequired
	if w.retrievalMode != nil {
		mode = search.NormalizeRetrievalMode(w.retrievalMode(ctx))
	} else if w.embeddingFactory == nil {
		// Preserve the standalone worker's historical local-vector behaviour.
		mode = search.RetrievalHybridFallback
	}
	activeIndexer := indexer.NewWithoutEmbeddings(w.store, policy)
	if w.retrievalMode == nil && w.embeddingFactory == nil && w.indexer != nil {
		activeIndexer = w.indexer
	}
	embeddingWarning := ""
	if mode != search.RetrievalKeywordOnly && w.embeddingFactory != nil {
		provider, err := w.embeddingFactory(ctx)
		if err != nil {
			if mode == search.RetrievalHybridRequired {
				return fmt.Errorf("embedding provider: %w (check the model setting)", err)
			}
			embeddingWarning = "Embedding provider unavailable; completed as keyword-only: " + truncate(err.Error(), 240)
		} else {
			// Probe the model before downloading anything. In fallback mode a bad
			// endpoint is an operational warning, not a reason to lose the
			// repository's lexical index.
			probeCtx, cancelProbe := context.WithTimeout(ctx, 30*time.Second)
			_, probeErr := provider.Embed(probeCtx, "git-ctx embedding probe")
			cancelProbe()
			if probeErr != nil {
				if mode == search.RetrievalHybridRequired {
					return fmt.Errorf("embedding endpoint rejected a probe request: %w (model setting: run the connection test)", probeErr)
				}
				embeddingWarning = "Embedding probe failed; completed as keyword-only: " + truncate(probeErr.Error(), 240)
			} else {
				activeIndexer = indexer.NewWithOptionalEmbedder(w.store, policy, provider)
			}
		}
	}
	if err := activeIndexer.ApplyPendingJob(ctx, adapter, r.SourceType, r.Repository, []source.Reference{*selected}, indexer.JobLease{ID: j.ID, StartedAt: j.StartedAt}); err != nil {
		return fmt.Errorf("index %s@%s: %w", r.ProjectKey+"/"+r.Slug, selected.Name, err)
	}
	if embeddingWarning != "" {
		if err := w.recordEmbeddingWarning(ctx, j, selected.Name, embeddingWarning); err != nil {
			return err
		}
	}
	if w.projection != nil {
		if err := w.projection(ctx, j.RepositoryID, selected.Name); err != nil {
			return fmt.Errorf("search projection: %w", err)
		}
	}
	return nil
}

func (w *Worker) recordEmbeddingWarning(ctx context.Context, j job, refName, warning string) error {
	tx, err := w.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, w.store.Rebind(`SELECT COALESCE(error_message,'') FROM index_jobs WHERE id=? AND status='running' AND started_at=?`), j.ID, j.StartedAt).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return errJobLeaseLost
	}
	if err != nil {
		return err
	}
	if current != "" {
		current += "; "
	}
	result, err := tx.ExecContext(ctx, w.store.Rebind(`UPDATE index_jobs SET error_message=? WHERE id=? AND status='running' AND started_at=?`), truncate(current+warning, 1000), j.ID, j.StartedAt)
	if err != nil {
		return err
	}
	if err = requireOwnedLease(result); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, w.store.Rebind(`UPDATE repository_ref_states SET embedding_status='degraded',embedding_error=? WHERE repository_id=? AND ref_name=?`),
		truncate(warning, 1000), j.RepositoryID, refName); err != nil {
		return err
	}
	return tx.Commit()
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

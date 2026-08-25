package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git-ctx/internal/store"
)

type Service struct{ store *store.Store }

func New(s *store.Store) *Service { return &Service{store: s} }

type Result struct {
	EventID   string `json:"eventId"`
	Duplicate bool   `json:"duplicate"`
	Jobs      int    `json:"jobs"`
}

// The two ways an event can be refused are told apart, because the sender acts
// on the status code. A body this receiver cannot read is the sender's mistake
// and will fail again on every retry; an event for a repository this platform
// does not have is a routing mistake an operator has to fix. Anything else —
// a database that is down, for instance — must not be reported as either, or
// the source server stops retrying an event that would have succeeded.
var (
	// ErrPayloadUnreadable is a body this receiver cannot parse.
	ErrPayloadUnreadable = errors.New("webhook payload is unreadable")
	// ErrRepositoryUnknown is an event for a repository that is not registered
	// here, or is disabled.
	ErrRepositoryUnknown = errors.New("webhook repository is not registered")
	// ErrSourceUnsupported is an event for a source type this build has no
	// receiver for.
	ErrSourceUnsupported = errors.New("unsupported webhook source")
)

func (s *Service) Enqueue(ctx context.Context, sourceType, eventID, eventType string, payload []byte) (Result, error) {
	if sourceType != "bitbucket" && sourceType != "gitlab" {
		return Result{}, ErrSourceUnsupported
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	if eventID == "" {
		eventID = payloadHash
	}
	externalRepo, refs, err := parse(sourceType, payload)
	if err != nil {
		// A rejected event is recorded before the error is returned. Silently
		// dropping it is what made a misdirected hook invisible: the sender sees
		// an error, the operator sees nothing at all.
		s.reject(ctx, sourceType, eventID, eventType, payloadHash, "", err.Error())
		return Result{}, err
	}
	var repoID string
	err = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id FROM repositories WHERE source_type=? AND source_external_id=? AND enabled=1`), sourceType, externalRepo).Scan(&repoID)
	if err != nil {
		s.reject(ctx, sourceType, eventID, eventType, payloadHash, externalRepo,
			"this platform has no enabled repository with source id "+externalRepo)
		return Result{}, ErrRepositoryUnknown
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	id := sourceType + ":" + eventID
	res, err := tx.ExecContext(ctx, s.store.Rebind(`INSERT INTO webhook_events(id,source_type,external_event_id,repository_id,event_type,payload_hash,status) VALUES(?,?,?,?,?,?,'received') ON CONFLICT DO NOTHING`), id, sourceType, eventID, repoID, eventType, payloadHash)
	if err != nil {
		return Result{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return Result{EventID: eventID, Duplicate: true}, nil
	}
	if len(refs) == 0 {
		refs = []string{""}
	}
	jobs := 0
	for n, ref := range refs {
		jobID := fmt.Sprintf("%s:%d", id, n)
		if _, err = tx.ExecContext(ctx, s.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES(?,?,?,'webhook','pending')`), jobID, repoID, ref); err != nil {
			return Result{}, err
		}
		jobs++
	}
	if _, err = tx.ExecContext(ctx, s.store.Rebind(`UPDATE webhook_events SET status='queued',processed_at=? WHERE id=?`), time.Now().UTC(), id); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	return Result{EventID: eventID, Jobs: jobs}, nil
}

// reject records an event that could not be turned into indexing work, so the
// operations screen can show that hooks are arriving and why they go nowhere.
// It is best effort: failing to record a rejection must not change what the
// sender is told.
func (s *Service) reject(ctx context.Context, sourceType, eventID, eventType, payloadHash, externalRepo, detail string) {
	if eventID == "" {
		eventID = payloadHash
	}
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO webhook_events(id,source_type,external_event_id,repository_id,event_type,payload_hash,status,detail,processed_at)
VALUES(?,?,?,?,?,?,'rejected',?,?) ON CONFLICT DO NOTHING`),
		sourceType+":"+eventID, sourceType, eventID, externalRepo, eventType, payloadHash, truncate(detail, 500), time.Now().UTC())
}

// truncate bounds a stored reason; a webhook body can put an arbitrary string
// into an error message.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && value[cut]&0xC0 == 0x80 {
		cut--
	}
	return value[:cut] + "…"
}

func parse(sourceType string, payload []byte) (string, []string, error) {
	if sourceType == "bitbucket" {
		var p struct {
			Repository struct{ ID int64 }
			Changes    []struct {
				Ref struct {
					DisplayID string `json:"displayId"`
				}
				Type string
			}
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return "", nil, fmt.Errorf("%w: invalid Bitbucket webhook JSON", ErrPayloadUnreadable)
		}
		if p.Repository.ID == 0 {
			return "", nil, fmt.Errorf("%w: Bitbucket repository ID is missing", ErrPayloadUnreadable)
		}
		var refs []string
		for _, c := range p.Changes {
			if c.Ref.DisplayID != "" {
				refs = appendUnique(refs, c.Ref.DisplayID)
			}
		}
		return fmt.Sprint(p.Repository.ID), refs, nil
	}
	var p struct {
		Project struct{ ID int64 }
		Ref     string
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", nil, fmt.Errorf("%w: invalid GitLab webhook JSON", ErrPayloadUnreadable)
	}
	if p.Project.ID == 0 {
		return "", nil, fmt.Errorf("%w: GitLab project ID is missing", ErrPayloadUnreadable)
	}
	ref := strings.TrimPrefix(strings.TrimPrefix(p.Ref, "refs/heads/"), "refs/tags/")
	var refs []string
	if ref != "" {
		refs = []string{ref}
	}
	return fmt.Sprint(p.Project.ID), refs, nil
}
func appendUnique(values []string, value string) []string {
	for _, x := range values {
		if x == value {
			return values
		}
	}
	return append(values, value)
}

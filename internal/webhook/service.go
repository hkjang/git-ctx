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

func (s *Service) Enqueue(ctx context.Context, sourceType, eventID, eventType string, payload []byte) (Result, error) {
	if sourceType != "bitbucket" && sourceType != "gitlab" {
		return Result{}, errors.New("unsupported webhook source")
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	if eventID == "" {
		eventID = payloadHash
	}
	externalRepo, refs, err := parse(sourceType, payload)
	if err != nil {
		return Result{}, err
	}
	var repoID string
	err = s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id FROM repositories WHERE source_type=? AND source_external_id=? AND enabled=1`), sourceType, externalRepo).Scan(&repoID)
	if err != nil {
		return Result{}, errors.New("webhook repository is not registered")
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
			return "", nil, errors.New("invalid Bitbucket webhook JSON")
		}
		if p.Repository.ID == 0 {
			return "", nil, errors.New("Bitbucket repository ID is missing")
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
		return "", nil, errors.New("invalid GitLab webhook JSON")
	}
	if p.Project.ID == 0 {
		return "", nil, errors.New("GitLab project ID is missing")
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

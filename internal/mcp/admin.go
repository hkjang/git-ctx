package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/version"
)

// Administrative tools: platform status, index jobs and reindex requests.

func (s *Server) platformStatus(ctx context.Context) (string, error) {
	if err := s.store.DB.PingContext(ctx); err != nil {
		return "", fmt.Errorf("metadata database unavailable: %w", err)
	}
	var repositories, bitbucket, gitlab, pending, running, failed int
	err := s.store.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN source_type='bitbucket' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN source_type='gitlab' THEN 1 ELSE 0 END),0) FROM repositories WHERE enabled=1`).Scan(&repositories, &bitbucket, &gitlab)
	if err != nil {
		return "", err
	}
	err = s.store.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0) FROM index_jobs`).Scan(&pending, &running, &failed)
	if err != nil {
		return "", err
	}
	// Which retrieval path the searches on this instance take is an operational
	// fact, not an implementation detail: without the index a large catalogue is
	// searched by scanning, and that is what an operator sees in the latency.
	lexical := "scan (no full-text index)"
	if s.store.FullTextAvailable() {
		lexical = "full-text index"
	}
	// Undelivered alerts are the failure an operator is least likely to notice,
	// because the thing that would have told them is the thing that broke. The
	// counts are read here so asking an agent for platform status surfaces them.
	var notificationsFailed, notificationsDead int
	_ = s.store.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='dead' THEN 1 ELSE 0 END),0) FROM notification_deliveries`).Scan(&notificationsFailed, &notificationsDead)
	status := fmt.Sprintf("## git-ctx Platform Status\n\n- Version: %s\n- Metadata Database: connected\n- Indexed Content Search: %s\n- Enabled Repositories: %d\n- Bitbucket Repositories: %d\n- GitLab Repositories: %d\n- Index Jobs Pending: %d\n- Index Jobs Running: %d\n- Index Jobs Failed: %d\n", version.Full(), lexical, repositories, bitbucket, gitlab, pending, running, failed)
	switch {
	case notificationsDead > 0:
		status += fmt.Sprintf("- Notifications: %d delivery(ies) gave up after their retries and %d are still retrying. Alerts are not reaching their destination; check the notification settings and retry them from the operations screen.\n", notificationsDead, notificationsFailed)
	case notificationsFailed > 0:
		status += fmt.Sprintf("- Notifications: %d delivery(ies) failed and are being retried.\n", notificationsFailed)
	default:
		status += "- Notifications: no failed deliveries\n"
	}
	// Connector health belongs in the status an operator asks an agent for: a
	// paused source is the difference between "nothing matched" and "we are not
	// currently able to look".
	if s.health != nil {
		if states := s.health(); len(states) > 0 {
			status += "\n### Source Connectors\n"
			for _, state := range states {
				status += fmt.Sprintf("- %s: %s", state.Source, state.State)
				if state.LastError != "" {
					status += " — " + state.LastError
				}
				if !state.RetryAt.IsZero() {
					status += fmt.Sprintf(" (retry at %s)", state.RetryAt.UTC().Format(time.RFC3339))
				}
				status += "\n"
			}
		}
	}
	if s.embeddingHealth != nil {
		if embeddingStatus := strings.TrimSpace(s.embeddingHealth(ctx)); embeddingStatus != "" {
			status += "\n### Embedding Retrieval\n" + embeddingStatus + "\n"
		}
	}
	return status, nil
}

func (s *Server) indexJobs(ctx context.Context, p auth.Principal, status string, limit int) (string, error) {
	status = strings.TrimSpace(status)
	if status != "" && status != "pending" && status != "running" && status != "completed" && status != "failed" {
		return "", errors.New("status must be pending, running, completed, or failed")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	statement := `SELECT j.id,r.library_id,j.ref_name,j.kind,j.status,j.attempts,j.files_processed,j.error_message,j.created_at,j.claimed_by
FROM index_jobs j JOIN repositories r ON r.id=j.repository_id`
	var args []any
	if status != "" {
		statement += ` WHERE j.status=?`
		args = append(args, status)
	}
	statement += ` ORDER BY j.created_at DESC LIMIT ?`
	args = append(args, min(limit*5, 500))
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("## Recent Index Jobs\n")
	count := 0
	for rows.Next() {
		var id, libraryID, ref, kind, state, message, claimedBy string
		var attempts, files int
		var created time.Time
		if err = rows.Scan(&id, &libraryID, &ref, &kind, &state, &attempts, &files, &message, &created, &claimedBy); err != nil {
			return "", err
		}
		if !libraryAllowed(libraryID, p.AllowedRepositories) {
			continue
		}
		fmt.Fprintf(&b, "\n- Job: %s\n  Library ID: %s\n  Ref: %s\n  Kind: %s\n  Status: %s\n  Attempts: %d\n  Files: %d\n  Created: %s\n", id, libraryID, ref, kind, state, attempts, files, created.UTC().Format(time.RFC3339))
		// Replicas share this queue, so a job that is running or that stopped
		// says which instance was holding it.
		if claimedBy != "" {
			fmt.Fprintf(&b, "  Claimed by: %s\n", claimedBy)
		}
		if message != "" {
			fmt.Fprintf(&b, "  Error: %s\n", truncate(message, 300))
		}
		count++
		if count == limit {
			break
		}
	}
	if count == 0 {
		return "No index jobs matched the requested scope.", rows.Err()
	}
	return b.String(), rows.Err()
}

func (s *Server) reindexRepository(ctx context.Context, p auth.Principal, libraryID, ref string) (string, error) {
	if !libraryAllowed(libraryID, p.AllowedRepositories) {
		return "", errors.New("library is unavailable or access is denied")
	}
	base := baseLibraryID(libraryID)
	if base == "" {
		return "", errors.New("libraryId must use /organization/project[/version]")
	}
	var repositoryID, defaultRef string
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id,default_branch FROM repositories WHERE library_id=? AND enabled=1`), base).Scan(&repositoryID, &defaultRef); err != nil {
		return "", errors.New("library is unavailable or access is denied")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = defaultRef
	}
	var existing string
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id FROM index_jobs WHERE repository_id=? AND ref_name=? AND status IN ('pending','running') ORDER BY created_at DESC LIMIT 1`), repositoryID, ref).Scan(&existing)
	if err == nil {
		return fmt.Sprintf("Reindex is already queued or running.\n\n- Job: %s\n- Library ID: %s\n- Ref: %s\n", existing, base, ref), nil
	}
	raw := make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return "", err
	}
	jobID := "mcp:" + hex.EncodeToString(raw)
	if _, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,next_run_at) VALUES(?,?,?,'manual','pending',?)`), jobID, repositoryID, ref, time.Now().UTC()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reindex queued.\n\n- Job: %s\n- Library ID: %s\n- Ref: %s\n", jobID, base, ref), nil
}

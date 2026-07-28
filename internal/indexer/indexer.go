package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"git-ctx/internal/embedding"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

type Policy struct {
	IncludeExtensions []string `json:"includeExtensions"`
	ExcludePrefixes   []string `json:"excludePrefixes"`
	MaxFileBytes      int64    `json:"maxFileBytes"`
}

func DefaultPolicy() Policy {
	return Policy{IncludeExtensions: []string{".md", ".mdx", ".rst", ".txt", ".adoc", ".asciidoc", ".yaml", ".yml", ".json", ".go", ".java", ".ts", ".tsx", ".py"}, ExcludePrefixes: []string{"node_modules/", "vendor/", "dist/", ".git/", "secrets/"}, MaxFileBytes: 1 << 20}
}

type Indexer struct {
	store    *store.Store
	policy   Policy
	embedder embedding.Provider
}

func New(s *store.Store, p Policy) *Indexer {
	if len(p.IncludeExtensions) == 0 {
		p = DefaultPolicy()
	}
	return &Indexer{store: s, policy: p, embedder: embedding.Local{}}
}
func NewWithEmbedder(s *store.Store, p Policy, provider embedding.Provider) *Indexer {
	index := New(s, p)
	if provider != nil {
		index.embedder = provider
	}
	return index
}
func LibraryID(projectKey, slug string) string {
	return "/" + normalize(projectKey) + "/" + normalize(slug)
}

func LibraryIDForSource(sourceType, projectKey, slug string) string {
	project := normalize(projectKey)
	if sourceType != "" && sourceType != "bitbucket" {
		project = normalize(sourceType) + "~" + project
	}
	return "/" + project + "/" + normalize(slug)
}

func (i *Indexer) SyncRepository(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference) error {
	return i.syncRepository(ctx, adapter, sourceType, repo, refs, true)
}

// ApplyPendingJob indexes content for a job that is already tracked by the worker.
func (i *Indexer) ApplyPendingJob(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference) error {
	return i.syncRepository(ctx, adapter, sourceType, repo, refs, false)
}

func (i *Indexer) syncRepository(ctx context.Context, adapter source.RepositorySource, sourceType string, repo source.Repository, refs []source.Reference, trackJobs bool) error {
	if sourceType != "bitbucket" && sourceType != "gitlab" {
		return errors.New("unsupported source type")
	}
	repoID := sourceType + ":" + fmt.Sprint(repo.ID)
	libraryID := LibraryIDForSource(sourceType, repo.ProjectKey, repo.Slug)
	_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES(?,?,?,?,?,?,?,?,?,1) ON CONFLICT(id) DO UPDATE SET project_key=excluded.project_key,slug=excluded.slug,name=excluded.name,description=excluded.description,default_branch=excluded.default_branch,enabled=1`), repoID, repo.ProjectKey, repo.Slug, repo.Name, repo.Description, sourceType, fmt.Sprint(repo.ID), libraryID, repo.DefaultBranch)
	if err != nil {
		return err
	}
	ref := source.RepositoryRef{ProjectKey: repo.ProjectKey, Slug: repo.Slug}
	perms, err := adapter.GetPermissions(ctx, ref)
	if err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	tx, err := i.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM repository_permissions WHERE repository_id=?`), repoID); err != nil {
		return err
	}
	for _, p := range perms {
		if !readable(p.Permission) {
			continue
		}
		principal := p.Principal
		if p.Kind == "group" {
			principal = "group:" + strings.TrimPrefix(principal, "/")
		}
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,?,?) ON CONFLICT(repository_id,principal) DO UPDATE SET permission=excluded.permission`), repoID, principal, p.Permission); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, r := range refs {
		if err := i.syncRef(ctx, adapter, repoID, ref, r, trackJobs); err != nil {
			return err
		}
	}
	_, _ = i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE repositories SET indexed_at=? WHERE id=?`), time.Now().UTC(), repoID)
	return nil
}
func (i *Indexer) syncRef(ctx context.Context, adapter source.RepositorySource, repoID string, repo source.RepositoryRef, ref source.Reference, trackJob bool) error {
	jobID := fmt.Sprintf("%d", time.Now().UnixNano())
	generationID := repoID + ":" + ref.Name + ":" + jobID
	if trackJob {
		_, err := i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status,started_at,attempts) VALUES(?,?,?,'sync','running',?,1)`), jobID, repoID, ref.Name, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	fail := func(e error) error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`DELETE FROM document_chunks_staging WHERE generation_id=?`), generationID)
		if trackJob {
			_, _ = i.store.DB.ExecContext(cleanupCtx, i.store.Rebind(`UPDATE index_jobs SET status='failed',error_message=?,completed_at=? WHERE id=?`), truncate(e.Error(), 1000), time.Now().UTC(), jobID)
		}
		return e
	}
	files, err := adapter.ListFiles(ctx, repo, ref.Name)
	if err != nil {
		return fail(err)
	}
	type securityEvent struct {
		id, path, finding, action string
	}
	var securityEvents []securityEvent
	processed := 0
	for _, file := range files {
		if !i.allowed(file) {
			continue
		}
		raw, e := adapter.GetFile(ctx, repo, ref.Name, file.Path)
		if e != nil {
			return fail(fmt.Errorf("%s: %w", file.Path, e))
		}
		if int64(len(raw)) > i.policy.MaxFileBytes {
			continue
		}
		safeContent, finding := sanitize(string(raw))
		if finding != "" {
			action := "redacted"
			if safeContent == "" {
				action = "blocked"
			}
			eventID := hash(repoID + "\x00" + ref.Name + "\x00" + file.Path + "\x00" + finding)
			securityEvents = append(securityEvents, securityEvent{id: eventID, path: file.Path, finding: finding, action: action})
			if safeContent == "" {
				continue
			}
		}
		for _, chunk := range parse(file.Path, safeContent) {
			id := hash(repoID + "\x00" + ref.Name + "\x00" + file.Path + "\x00" + fmt.Sprint(chunk.Start) + "\x00" + chunk.Content)
			embedded, embedErr := i.embedder.Embed(ctx, chunk.Heading+"\n"+chunk.Content)
			if embedErr != nil {
				return fail(fmt.Errorf("embedding %s: %w", file.Path, embedErr))
			}
			vector := embedding.Encode(embedded)
			_, e = i.store.DB.ExecContext(ctx, i.store.Rebind(`INSERT INTO document_chunks_staging(generation_id,id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`), generationID, id, repoID, ref.Name, ref.LatestCommit, file.Path, chunk.Start, chunk.End, chunk.Heading, contentType(file.Path), chunk.Content, hash(chunk.Content), vector)
			if e != nil {
				return fail(e)
			}
		}
		processed++
	}
	tx, err := i.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM document_chunks WHERE repository_id=? AND ref_name=?`), repoID, ref.Name); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding,indexed_at)
SELECT id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding,indexed_at
FROM document_chunks_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	for _, event := range securityEvents {
		if _, err = tx.ExecContext(ctx, i.store.Rebind(`INSERT INTO index_security_events(id,repository_id,ref_name,file_path,finding_type,action) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), event.id, repoID, ref.Name, event.path, event.finding, event.action); err != nil {
			_ = tx.Rollback()
			return fail(err)
		}
	}
	if _, err = tx.ExecContext(ctx, i.store.Rebind(`DELETE FROM document_chunks_staging WHERE generation_id=?`), generationID); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if trackJob {
		_, err = i.store.DB.ExecContext(ctx, i.store.Rebind(`UPDATE index_jobs SET status='completed',files_processed=?,completed_at=? WHERE id=?`), processed, time.Now().UTC(), jobID)
		return err
	}
	return nil
}
func (i *Indexer) allowed(f source.File) bool {
	p := strings.TrimPrefix(filepath.ToSlash(f.Path), "./")
	for _, x := range i.policy.ExcludePrefixes {
		if strings.HasPrefix(p, x) {
			return false
		}
	}
	ext := strings.ToLower(filepath.Ext(p))
	for _, x := range i.policy.IncludeExtensions {
		if ext == x {
			return f.Size == 0 || f.Size <= i.policy.MaxFileBytes
		}
	}
	return false
}

type chunk struct {
	Heading, Content string
	Start, End       int
}

var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
var secretAssignmentRE = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|client[_-]?secret|password|passwd)\s*[:=]\s*["']?([A-Za-z0-9_./+=-]{8,})`)
var awsKeyRE = regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`)

func parse(path, content string) []chunk {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	heading := path
	start := 1
	var body []string
	var out []chunk
	flush := func(end int) {
		text := strings.TrimSpace(strings.Join(body, "\n"))
		if text != "" {
			out = append(out, chunk{Heading: heading, Content: text, Start: start, End: end})
		}
		body = nil
	}
	for n, line := range lines {
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flush(n)
			heading = m[2]
			start = n + 1
			continue
		}
		body = append(body, line)
	}
	flush(len(lines))
	return out
}
func sanitize(content string) (string, string) {
	if strings.Contains(content, "-----BEGIN PRIVATE KEY-----") || strings.Contains(content, "-----BEGIN RSA PRIVATE KEY-----") || strings.Contains(content, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		return "", "private_key"
	}
	finding := ""
	masked := secretAssignmentRE.ReplaceAllStringFunc(content, func(value string) string {
		finding = "credential_assignment"
		at := strings.IndexAny(value, ":=")
		if at < 0 {
			return "[REDACTED]"
		}
		return value[:at+1] + " [REDACTED]"
	})
	masked = awsKeyRE.ReplaceAllStringFunc(masked, func(string) string { finding = "cloud_access_key"; return "[REDACTED]" })
	return masked, finding
}
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func readable(p string) bool {
	p = strings.ToLower(p)
	switch p {
	case "read", "write", "admin",
		"repo_read", "repo_write", "repo_admin",
		"project_read", "project_write", "project_admin",
		"sys_admin", "reporter", "developer", "maintainer", "owner":
		return true
	default:
		return strings.Contains(p, "read")
	}
}
func contentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdx", ".rst", ".txt", ".adoc", ".asciidoc":
		return "document"
	case ".yaml", ".yml", ".json":
		return "configuration"
	default:
		return "code"
	}
}
func hash(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

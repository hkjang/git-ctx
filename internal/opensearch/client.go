package opensearch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git-ctx/internal/netclient"
	"git-ctx/internal/store"
)

type Config struct {
	Enabled       bool
	BaseURL       string
	Index         string
	Username      string
	Password      string
	APIKey        string
	Timeout       time.Duration
	TLSVerify     *bool
	CACertificate string
	ProxyURL      string
}

type Candidate struct {
	ID    string
	Score float64
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.Index == "" {
		cfg.Index = "git-ctx-chunks"
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("opensearch.baseUrl is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("opensearch.baseUrl must be an absolute HTTP(S) URL")
	}
	if strings.ContainsAny(cfg.Index, "\\/*?,# :") {
		return nil, errors.New("opensearch.index contains unsupported characters")
	}
	h, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, http: h}, nil
}

func (c *Client) request(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.cfg.APIKey)
	} else if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	return c.http.Do(req)
}

func statusError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return netclient.NewHTTPStatusError(resp.StatusCode,
		fmt.Errorf("opensearch returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
}

func (c *Client) Validate(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodGet, "/", "", nil)
	if err != nil {
		return fmt.Errorf("opensearch connection test: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	resp.Body.Close()
	return c.EnsureIndex(ctx)
}

func (c *Client) EnsureIndex(ctx context.Context) error {
	path := "/" + url.PathEscape(c.cfg.Index)
	resp, err := c.request(ctx, http.MethodHead, path, "", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Body.Close()
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return statusError(resp)
	}
	resp.Body.Close()
	mapping := map[string]any{"mappings": map[string]any{"properties": map[string]any{
		"repository_id": map[string]string{"type": "keyword"}, "ref_name": map[string]string{"type": "keyword"},
		"commit_id": map[string]string{"type": "keyword"}, "file_path": map[string]string{"type": "text"},
		"heading": map[string]string{"type": "text"}, "content": map[string]string{"type": "text"},
		"content_type": map[string]string{"type": "keyword"}, "principals": map[string]string{"type": "keyword"},
		"line_start": map[string]string{"type": "integer"}, "line_end": map[string]string{"type": "integer"},
	}}}
	raw, _ := json.Marshal(mapping)
	resp, err = c.request(ctx, http.MethodPut, path, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	resp.Body.Close()
	return nil
}

func (c *Client) SyncRef(ctx context.Context, s *store.Store, repositoryID, ref string) error {
	if err := c.EnsureIndex(ctx); err != nil {
		return err
	}
	principals, err := loadPrincipals(ctx, s, repositoryID)
	if err != nil {
		return err
	}
	if applied, err := c.syncIncremental(ctx, s, repositoryID, ref, principals); err != nil || applied {
		return err
	}
	deleteBody, _ := json.Marshal(map[string]any{"query": map[string]any{"bool": map[string]any{"filter": []any{
		map[string]any{"term": map[string]string{"repository_id": repositoryID}}, map[string]any{"term": map[string]string{"ref_name": ref}},
	}}}})
	path := "/" + url.PathEscape(c.cfg.Index) + "/_delete_by_query?conflicts=proceed&refresh=true"
	resp, err := c.request(ctx, http.MethodPost, path, "application/json", bytes.NewReader(deleteBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	resp.Body.Close()

	rows, err := s.DB.QueryContext(ctx, s.Rebind(`SELECT id,commit_id,file_path,line_start,line_end,heading,content_type,content FROM document_chunks WHERE repository_id=? AND ref_name=? ORDER BY id`), repositoryID, ref)
	if err != nil {
		return err
	}
	defer rows.Close()
	var bulk bytes.Buffer
	w := bufio.NewWriter(&bulk)
	count := 0
	for rows.Next() {
		var id, commit, file, heading, contentType, content string
		var start, end int
		if err := rows.Scan(&id, &commit, &file, &start, &end, &heading, &contentType, &content); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"index": map[string]string{"_index": c.cfg.Index, "_id": id}})
		doc, _ := json.Marshal(map[string]any{"repository_id": repositoryID, "ref_name": ref, "commit_id": commit, "file_path": file, "line_start": start, "line_end": end, "heading": heading, "content_type": contentType, "content": content, "principals": principals})
		fmt.Fprintln(w, string(meta))
		fmt.Fprintln(w, string(doc))
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w.Flush()
	if count == 0 {
		return markProjection(ctx, s, repositoryID, ref, principals)
	}
	resp, err = c.request(ctx, http.MethodPost, "/_bulk?refresh=wait_for", "application/x-ndjson", &bulk)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	defer resp.Body.Close()
	var result struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int             `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if result.Errors {
		failed := 0
		for _, item := range result.Items {
			for action, status := range item {
				if action == "delete" && status.Status == http.StatusNotFound {
					continue
				}
				if status.Status < 200 || status.Status >= 300 || len(status.Error) > 0 {
					failed++
				}
			}
		}
		if failed > 0 || len(result.Items) == 0 {
			return fmt.Errorf("opensearch bulk indexing reported %d item failures", failed)
		}
	}
	return markProjection(ctx, s, repositoryID, ref, principals)
}

func (c *Client) syncIncremental(ctx context.Context, s *store.Store, repositoryID, ref string, principals []string) (bool, error) {
	var currentCommit, projectedCommit, projectedACL string
	if err := s.DB.QueryRowContext(ctx, s.Rebind(`SELECT commit_id FROM repository_ref_states WHERE repository_id=? AND ref_name=?`), repositoryID, ref).Scan(&currentCommit); err != nil {
		return false, nil
	}
	if err := s.DB.QueryRowContext(ctx, s.Rebind(`SELECT commit_id,acl_fingerprint FROM search_projection_states WHERE repository_id=? AND ref_name=?`), repositoryID, ref).Scan(&projectedCommit, &projectedACL); err != nil || projectedCommit == "" {
		return false, nil
	}
	currentACL := aclFingerprint(principals)
	if projectedCommit == currentCommit {
		if projectedACL == currentACL {
			return true, nil
		}
		return false, nil
	}
	rows, err := s.DB.QueryContext(ctx, s.Rebind(`SELECT previous_commit_id,file_path,action,deleted_chunk_ids FROM repository_ref_changes WHERE repository_id=? AND ref_name=? AND commit_id=? ORDER BY action,file_path`), repositoryID, ref, currentCommit)
	if err != nil {
		return false, err
	}
	type change struct {
		path, action string
		deletedIDs   []string
	}
	var changes []change
	upsertPaths := map[string]bool{}
	for rows.Next() {
		var previous, path, action, deletedJSON string
		if err = rows.Scan(&previous, &path, &action, &deletedJSON); err != nil {
			rows.Close()
			return false, err
		}
		if previous != projectedCommit || action == "full" {
			rows.Close()
			return false, nil
		}
		item := change{path: path, action: action}
		_ = json.Unmarshal([]byte(deletedJSON), &item.deletedIDs)
		changes = append(changes, item)
		if action == "upsert" && path != "" {
			upsertPaths[path] = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if len(changes) == 0 {
		return false, nil
	}

	var bulk bytes.Buffer
	writer := bufio.NewWriter(&bulk)
	actionCount := 0
	for _, item := range changes {
		if item.action != "delete" {
			continue
		}
		for _, id := range item.deletedIDs {
			meta, _ := json.Marshal(map[string]any{"delete": map[string]string{"_index": c.cfg.Index, "_id": id}})
			fmt.Fprintln(writer, string(meta))
			actionCount++
		}
	}
	if len(upsertPaths) > 0 {
		placeholders := make([]string, 0, len(upsertPaths))
		args := []any{repositoryID, ref}
		for path := range upsertPaths {
			placeholders = append(placeholders, "?")
			args = append(args, path)
		}
		chunkRows, queryErr := s.DB.QueryContext(ctx, s.Rebind(`SELECT id,commit_id,file_path,line_start,line_end,heading,content_type,content FROM document_chunks WHERE repository_id=? AND ref_name=? AND file_path IN (`+strings.Join(placeholders, ",")+`) ORDER BY id`), args...)
		if queryErr != nil {
			return false, queryErr
		}
		for chunkRows.Next() {
			var id, commit, file, heading, contentType, content string
			var start, end int
			if err = chunkRows.Scan(&id, &commit, &file, &start, &end, &heading, &contentType, &content); err != nil {
				chunkRows.Close()
				return false, err
			}
			meta, _ := json.Marshal(map[string]any{"index": map[string]string{"_index": c.cfg.Index, "_id": id}})
			doc, _ := json.Marshal(map[string]any{"repository_id": repositoryID, "ref_name": ref, "commit_id": commit, "file_path": file, "line_start": start, "line_end": end, "heading": heading, "content_type": contentType, "content": content, "principals": principals})
			fmt.Fprintln(writer, string(meta))
			fmt.Fprintln(writer, string(doc))
			actionCount++
		}
		if err = chunkRows.Err(); err != nil {
			chunkRows.Close()
			return false, err
		}
		chunkRows.Close()
	}
	writer.Flush()
	if actionCount > 0 {
		if err = c.sendBulk(ctx, &bulk); err != nil {
			return true, err
		}
	}
	return true, markProjection(ctx, s, repositoryID, ref, principals)
}

func (c *Client) sendBulk(ctx context.Context, bulk *bytes.Buffer) error {
	resp, err := c.request(ctx, http.MethodPost, "/_bulk?refresh=wait_for", "application/x-ndjson", bulk)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	defer resp.Body.Close()
	var result struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int             `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if result.Errors {
		failed := 0
		for _, item := range result.Items {
			for action, status := range item {
				if action == "delete" && status.Status == http.StatusNotFound {
					continue
				}
				if status.Status < 200 || status.Status >= 300 || len(status.Error) > 0 {
					failed++
				}
			}
		}
		if failed > 0 || len(result.Items) == 0 {
			return fmt.Errorf("opensearch bulk indexing reported %d item failures", failed)
		}
	}
	return nil
}

func markProjection(ctx context.Context, s *store.Store, repositoryID, ref string, principals []string) error {
	_, err := s.DB.ExecContext(ctx, s.Rebind(`INSERT INTO search_projection_states(repository_id,ref_name,commit_id,acl_fingerprint,projected_at)
SELECT repository_id,ref_name,commit_id,?,? FROM repository_ref_states WHERE repository_id=? AND ref_name=?
ON CONFLICT(repository_id,ref_name) DO UPDATE SET commit_id=excluded.commit_id,acl_fingerprint=excluded.acl_fingerprint,projected_at=excluded.projected_at`), aclFingerprint(principals), time.Now().UTC(), repositoryID, ref)
	return err
}

func aclFingerprint(principals []string) string {
	sum := sha256.Sum256([]byte(strings.Join(principals, "\x00")))
	return fmt.Sprintf("%x", sum[:])
}

func loadPrincipals(ctx context.Context, s *store.Store, repositoryID string) ([]string, error) {
	// The indexer stores only permissions accepted by its readable() policy. Source
	// adapters retain their native names (for example REPO_READ or developer), so
	// filtering on a synthetic "read" value here would silently remove valid ACLs.
	rows, err := s.DB.QueryContext(ctx, s.Rebind(`SELECT principal FROM repository_permissions WHERE repository_id=? ORDER BY principal`), repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (c *Client) Search(ctx context.Context, repositoryID, ref string, principals []string, query string, limit int) ([]Candidate, error) {
	if len(principals) == 0 {
		return nil, nil
	}
	if limit < 1 || limit > 500 {
		limit = 500
	}
	allowed := append(append([]string{}, principals...), "*")
	body, _ := json.Marshal(map[string]any{"size": limit, "_source": false, "query": map[string]any{"bool": map[string]any{
		"must":   []any{map[string]any{"multi_match": map[string]any{"query": query, "fields": []string{"heading^3", "file_path^2", "content"}}}},
		"filter": []any{map[string]any{"term": map[string]string{"repository_id": repositoryID}}, map[string]any{"term": map[string]string{"ref_name": ref}}, map[string]any{"terms": map[string]any{"principals": allowed}}},
	}}})
	resp, err := c.request(ctx, http.MethodPost, "/"+url.PathEscape(c.cfg.Index)+"/_search", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	defer resp.Body.Close()
	var result struct {
		Hits struct {
			Hits []struct {
				ID    string  `json:"_id"`
				Score float64 `json:"_score"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	out := make([]Candidate, len(result.Hits.Hits))
	for i, h := range result.Hits.Hits {
		out[i] = Candidate{ID: h.ID, Score: h.Score}
	}
	return out, nil
}

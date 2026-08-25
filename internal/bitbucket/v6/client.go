package v6

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git-ctx/internal/netclient"
	"git-ctx/internal/source"
)

type Config struct {
	BaseURL       string
	APIPrefix     string
	SearchAPIPath string
	Token         string
	Username      string
	Password      string
	Timeout       time.Duration
	TLSVerify     *bool
	CACertificate string
	ProxyURL      string
}
type Client struct {
	base       *url.URL
	prefix     string
	searchPath string
	cfg        Config
	http       *http.Client
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("valid Bitbucket base URL is required")
	}
	if cfg.APIPrefix == "" {
		cfg.APIPrefix = "/rest/api/1.0"
	}
	if strings.ContainsAny(cfg.APIPrefix, "?#") {
		return nil, errors.New("Bitbucket API prefix must not contain a query or fragment")
	}
	if cfg.SearchAPIPath == "" {
		cfg.SearchAPIPath = "/rest/search/latest/search"
	}
	if !strings.HasPrefix(cfg.SearchAPIPath, "/") || strings.ContainsAny(cfg.SearchAPIPath, "?#") {
		return nil, errors.New("Bitbucket search API path must be an absolute path without query or fragment")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	httpClient, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	prefix := (&url.URL{Path: "/" + strings.Trim(cfg.APIPrefix, "/")}).EscapedPath()
	searchPath := (&url.URL{Path: "/" + strings.Trim(cfg.SearchAPIPath, "/")}).EscapedPath()
	return &Client{base: u, prefix: prefix, searchPath: searchPath, cfg: cfg, http: httpClient}, nil
}

type page[T any] struct {
	Values        []T  `json:"values"`
	IsLastPage    bool `json:"isLastPage"`
	NextPageStart int  `json:"nextPageStart"`
}

// HTTPError is the typed non 2xx response. It is source.APIError so that the
// search layer can tell an expired token from a deleted repository without
// importing this package.
type HTTPError = source.APIError

// maxPages bounds one paginated read at 1000 items per page, so a single
// adapter call on a very large instance stays bounded instead of loading the
// whole server into memory.
const maxPages = 20
const maxFileBytes int64 = 10 << 20

func pageAll[T any](ctx context.Context, c *Client, endpoint string) ([]T, error) {
	return pageAllQuery[T](ctx, c, endpoint, nil)
}

func pageAllQuery[T any](ctx context.Context, c *Client, endpoint string, baseQuery url.Values) ([]T, error) {
	return pageItems[T](ctx, c, endpoint, baseQuery, 0)
}

func pageUpTo[T any](ctx context.Context, c *Client, endpoint string, baseQuery url.Values, itemLimit int) ([]T, error) {
	return pageItems[T](ctx, c, endpoint, baseQuery, itemLimit)
}

func pageItems[T any](ctx context.Context, c *Client, endpoint string, baseQuery url.Values, itemLimit int) ([]T, error) {
	start := 0
	var all []T
	for pages := 0; pages < maxPages; pages++ {
		requestLimit := 1000
		if itemLimit > 0 {
			remaining := itemLimit - len(all)
			if remaining <= 0 {
				return all, nil
			}
			requestLimit = min(requestLimit, remaining)
		}
		q := cloneValues(baseQuery)
		q.Set("limit", fmt.Sprint(requestLimit))
		q.Set("start", fmt.Sprint(start))
		var p page[T]
		if err := c.json(ctx, http.MethodGet, endpoint, q, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Values...)
		if itemLimit > 0 && len(all) >= itemLimit {
			return all[:itemLimit], nil
		}
		if p.IsLastPage || len(p.Values) == 0 {
			return all, nil
		}
		if p.NextPageStart <= start {
			return nil, fmt.Errorf("bitbucket API pagination did not advance for %s: start=%d nextPageStart=%d", endpoint, start, p.NextPageStart)
		}
		start = p.NextPageStart
	}
	return nil, fmt.Errorf("bitbucket API pagination incomplete for %s: exceeded %d pages after %d items; nextPageStart=%d", endpoint, maxPages, len(all), start)
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values)+2)
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

type branchName string

func (branch *branchName) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*branch = ""
		return nil
	}
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*branch = branchName(normalizeBranch(name))
		return nil
	}
	var value struct {
		DisplayID string `json:"displayId"`
		ID        string `json:"id"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode Bitbucket default branch: %w", err)
	}
	name = value.DisplayID
	if name == "" {
		name = value.Name
	}
	if name == "" {
		name = value.ID
	}
	*branch = branchName(normalizeBranch(name))
	return nil
}

func normalizeBranch(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "refs/heads/")
}
func (c *Client) ListProjects(ctx context.Context) ([]source.Project, error) {
	type item struct{ Key, Name, Description string }
	items, e := pageAll[item](ctx, c, "/projects")
	if e != nil {
		return nil, e
	}
	out := make([]source.Project, len(items))
	for i, x := range items {
		out[i] = source.Project{Key: x.Key, Name: x.Name, Description: x.Description}
	}
	return out, nil
}
func (c *Client) ListRepositories(ctx context.Context, project string) ([]source.Repository, error) {
	type item struct {
		ID                      int64
		Slug, Name, Description string
		Archived                bool
		Project                 struct{ Key string }
		DefaultBranch           branchName `json:"defaultBranch"`
	}
	items, e := pageAll[item](ctx, c, "/projects/"+escape(project)+"/repos")
	if e != nil {
		return nil, e
	}
	out := make([]source.Repository, len(items))
	for i, x := range items {
		out[i] = source.Repository{ID: x.ID, ProjectKey: x.Project.Key, Slug: x.Slug, Name: x.Name, Description: x.Description, DefaultBranch: string(x.DefaultBranch), Archived: x.Archived}
	}
	return out, nil
}
func (c *Client) ListBranches(ctx context.Context, r source.RepositoryRef) ([]source.Reference, error) {
	return c.refs(ctx, r, "branches")
}
func (c *Client) ListTags(ctx context.Context, r source.RepositoryRef) ([]source.Reference, error) {
	return c.refs(ctx, r, "tags")
}
func (c *Client) refs(ctx context.Context, r source.RepositoryRef, kind string) ([]source.Reference, error) {
	type item struct {
		ID, DisplayID, LatestCommit string
		Default                     bool `json:"isDefault"`
	}
	items, e := pageAll[item](ctx, c, c.repo(r)+"/"+kind)
	if e != nil {
		return nil, e
	}
	out := make([]source.Reference, len(items))
	for i, x := range items {
		out[i] = source.Reference{ID: x.ID, Name: x.DisplayID, LatestCommit: x.LatestCommit, Default: x.Default}
	}
	return out, nil
}
func (c *Client) GetCommit(ctx context.Context, r source.RepositoryRef, id string) (source.Commit, error) {
	var x struct {
		ID, DisplayID, Message string
		Author                 struct{ Name string }
	}
	e := c.json(ctx, http.MethodGet, c.repo(r)+"/commits/"+escape(id), nil, nil, &x)
	return source.Commit{ID: x.ID, DisplayID: x.DisplayID, Message: x.Message, Author: x.Author.Name}, e
}

// SearchChangeRequests lists pull requests and filters them locally. Bitbucket
// Server 6.x has no text search on pull requests, so the title and description
// are matched here instead of pretending the feature does not exist.
func (c *Client) SearchChangeRequests(ctx context.Context, r source.RepositoryRef, query, state string, limit int) ([]source.ChangeRequest, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	remoteState := "ALL"
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open", "opened":
		remoteState = "OPEN"
	case "merged":
		remoteState = "MERGED"
	case "closed", "declined":
		remoteState = "DECLINED"
	}
	type item struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		State       string `json:"state"`
		CreatedDate int64  `json:"createdDate"`
		UpdatedDate int64  `json:"updatedDate"`
		FromRef     struct {
			DisplayID string `json:"displayId"`
		} `json:"fromRef"`
		ToRef struct {
			DisplayID string `json:"displayId"`
		} `json:"toRef"`
		Author struct {
			User struct {
				DisplayName string `json:"displayName"`
			} `json:"user"`
		} `json:"author"`
		Links struct {
			Self []struct {
				Href string `json:"href"`
			} `json:"self"`
		} `json:"links"`
	}
	var page struct {
		Values []item `json:"values"`
	}
	q := url.Values{"state": []string{remoteState}, "limit": []string{fmt.Sprint(max(limit, 25))}, "order": []string{"NEWEST"}}
	if err := c.json(ctx, http.MethodGet, c.repo(r)+"/pull-requests", q, nil, &page); err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	out := make([]source.ChangeRequest, 0, len(page.Values))
	for _, value := range page.Values {
		if needle != "" && !strings.Contains(strings.ToLower(value.Title+" "+value.Description), needle) {
			continue
		}
		request := source.ChangeRequest{
			ID: fmt.Sprintf("#%d", value.ID), Number: value.ID, Title: value.Title, Description: value.Description,
			State: strings.ToLower(value.State), Author: value.Author.User.DisplayName,
			SourceRef: value.FromRef.DisplayID, TargetRef: value.ToRef.DisplayID,
		}
		if len(value.Links.Self) > 0 {
			request.URL = value.Links.Self[0].Href
		}
		if value.CreatedDate > 0 {
			request.CreatedAt = time.UnixMilli(value.CreatedDate).UTC()
		}
		if value.UpdatedDate > 0 {
			request.UpdatedAt = time.UnixMilli(value.UpdatedDate).UTC()
		}
		out = append(out, request)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// ListCommits returns the commits that touched a path, newest first.
func (c *Client) ListCommits(ctx context.Context, r source.RepositoryRef, refName, filePath string, limit int) ([]source.Commit, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	q := url.Values{"limit": []string{fmt.Sprint(limit)}}
	if refName != "" {
		q.Set("until", refName)
	}
	if filePath != "" {
		q.Set("path", filePath)
	}
	var page struct {
		Values []struct {
			ID              string `json:"id"`
			DisplayID       string `json:"displayId"`
			Message         string `json:"message"`
			AuthorTimestamp int64  `json:"authorTimestamp"`
			Author          struct {
				Name         string `json:"name"`
				EmailAddress string `json:"emailAddress"`
			} `json:"author"`
		} `json:"values"`
	}
	if err := c.json(ctx, http.MethodGet, c.repo(r)+"/commits", q, nil, &page); err != nil {
		return nil, err
	}
	out := make([]source.Commit, 0, len(page.Values))
	for _, item := range page.Values {
		commit := source.Commit{ID: item.ID, DisplayID: item.DisplayID, Message: item.Message, Author: item.Author.Name, AuthorEmail: item.Author.EmailAddress}
		if item.AuthorTimestamp > 0 {
			commit.AuthoredAt = time.UnixMilli(item.AuthorTimestamp).UTC()
		}
		out = append(out, commit)
	}
	return out, nil
}

func (c *Client) ListFiles(ctx context.Context, r source.RepositoryRef, ref string) ([]source.File, error) {
	q := url.Values{"at": []string{ref}}
	// The files endpoint is recursively paged by Bitbucket and returns canonical paths.
	paths, err := pageAllQuery[string](ctx, c, c.repo(r)+"/files", q)
	if err != nil {
		return nil, err
	}
	out := make([]source.File, len(paths))
	for i, p := range paths {
		out[i] = source.File{Path: p}
	}
	return out, nil
}
func (c *Client) GetFile(ctx context.Context, r source.RepositoryRef, ref, filePath string) ([]byte, error) {
	escapedPath, err := escapePath(filePath)
	if err != nil {
		return nil, err
	}
	q := url.Values{"at": []string{ref}}
	return c.bytes(ctx, c.repo(r)+"/raw/"+escapedPath, q)
}
func (c *Client) Changes(ctx context.Context, r source.RepositoryRef, from, to string) ([]source.Change, error) {
	type item struct {
		Type string
		Path struct {
			ToString string `json:"toString"`
		}
		SrcPath *struct {
			ToString string `json:"toString"`
		} `json:"srcPath"`
	}
	q := url.Values{"since": []string{from}, "until": []string{to}}
	items, err := pageAllQuery[item](ctx, c, c.repo(r)+"/changes", q)
	if err != nil {
		return nil, err
	}
	out := make([]source.Change, 0, len(items))
	for _, item := range items {
		change := source.Change{Path: item.Path.ToString, Type: strings.ToLower(item.Type)}
		if item.SrcPath != nil {
			change.OldPath = item.SrcPath.ToString
		}
		if change.Path != "" || change.OldPath != "" {
			out = append(out, change)
		}
	}
	return out, nil
}
func (c *Client) SearchQuery(ctx context.Context, r source.RepositoryRef, ref, query string, limit int) ([]source.QueryResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	hits, err := c.searchCode(ctx, fmt.Sprintf("project:%s repo:%s %s", r.ProjectKey, r.Slug, query), limit)
	if err != nil {
		return nil, err
	}
	out := make([]source.QueryResult, 0, len(hits))
	requestedRef := normalizeBranch(ref)
	mismatchedRef := ""
	for _, hit := range hits {
		if hit.ProjectKey != "" && !strings.EqualFold(hit.ProjectKey, r.ProjectKey) {
			continue
		}
		if hit.Slug != "" && !strings.EqualFold(hit.Slug, r.Slug) {
			continue
		}
		actualRef := normalizeBranch(hit.DefaultBranch)
		if actualRef == "" {
			actualRef = normalizeBranch(hit.Ref)
		}
		// Bitbucket Server/Data Center indexes only the repository default
		// branch. Never relabel those snippets as a requested tag or branch.
		if requestedRef != "" && actualRef != "" && requestedRef != actualRef {
			mismatchedRef = actualRef
			continue
		}
		result := hit.QueryResult
		result.CommitID = actualRef
		if result.CommitID == "" {
			// Older search plugins omitted repository metadata. Preserve the
			// caller's ref only when the server gave us nothing better.
			result.CommitID = requestedRef
		}
		out = append(out, result)
	}
	if mismatchedRef != "" {
		return nil, fmt.Errorf("%w: Bitbucket returned default branch %q for requested ref %q", source.ErrCodeSearchRefUnsupported, mismatchedRef, requestedRef)
	}
	return out, nil
}

// SearchGlobalQuery uses the same Bitbucket code search API without a project
// or repository filter, which is what the Bitbucket search screen itself does.
func (c *Client) SearchGlobalQuery(ctx context.Context, query string, limit int) ([]source.GlobalQueryResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	hits, err := c.searchCode(ctx, query, limit)
	if source.StatusOf(err) == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %w", source.ErrGlobalSearchUnsupported, err)
	}
	return hits, err
}

// SearchRepositories matches the term against repository names across every
// project instead of paging through the full project list.
func (c *Client) SearchRepositories(ctx context.Context, query string, limit int) ([]source.Repository, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	type item struct {
		ID                      int64
		Slug, Name, Description string
		Archived                bool
		Project                 struct{ Key string }
		DefaultBranch           branchName `json:"defaultBranch"`
	}
	items, err := pageUpTo[item](ctx, c, "/repos", url.Values{"name": []string{query}}, limit)
	if err != nil {
		return nil, err
	}
	out := make([]source.Repository, 0, len(items))
	for _, x := range items {
		out = append(out, source.Repository{ID: x.ID, ProjectKey: x.Project.Key, Slug: x.Slug, Name: x.Name, Description: x.Description, DefaultBranch: string(x.DefaultBranch), Archived: x.Archived})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

type codeSearchLine struct {
	Line     int    `json:"line"`
	Text     string `json:"text"`
	Segments []struct {
		Text string `json:"text"`
	} `json:"segments"`
}

type codeSearchHit struct {
	File       string `json:"file"`
	Repository struct {
		ID            int64                `json:"id"`
		Slug          string               `json:"slug"`
		Name          string               `json:"name"`
		Project       struct{ Key string } `json:"project"`
		DefaultBranch branchName           `json:"defaultBranch"`
	} `json:"repository"`
	// Bitbucket 6.x returns grouped hitContexts. Lines is retained for newer
	// or customized search plugins that expose a flattened representation.
	HitContexts [][]codeSearchLine `json:"hitContexts"`
	Lines       []codeSearchLine   `json:"lines"`
}

type codeSearchPayload struct {
	Errors []struct {
		Message       string `json:"message"`
		ExceptionName string `json:"exceptionName"`
	} `json:"errors"`
	Code struct {
		Values        []codeSearchHit `json:"values"`
		IsLastPage    bool            `json:"isLastPage"`
		NextStart     *int            `json:"nextStart"`
		NextPageStart *int            `json:"nextPageStart"`
	} `json:"code"`
}

func (c *Client) searchCode(ctx context.Context, query string, limit int) ([]source.GlobalQueryResult, error) {
	if limit < 1 || limit > 50 {
		limit = 8
	}
	start := 0
	out := make([]source.GlobalQueryResult, 0, limit)
	for pages := 0; pages < maxPages && len(out) < limit; pages++ {
		pageLimit := limit - len(out)
		body := map[string]any{
			"query":    query,
			"entities": map[string]any{"code": map[string]int{"start": start, "limit": pageLimit}},
			"limits":   map[string]int{"primary": pageLimit, "secondary": pageLimit},
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		resp, err := c.searchRequest(ctx, raw)
		if err != nil {
			return nil, err
		}
		var payload codeSearchPayload
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&payload)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeFailure("code search", decodeErr)
		}
		if len(payload.Errors) > 0 {
			messages := make([]string, 0, len(payload.Errors))
			for _, item := range payload.Errors {
				message := strings.TrimSpace(item.Message)
				if message == "" {
					message = strings.TrimSpace(item.ExceptionName)
				}
				if message != "" {
					messages = append(messages, message)
				}
			}
			if len(messages) == 0 {
				messages = append(messages, "unknown search error")
			}
			return nil, fmt.Errorf("bitbucket search API: %s", strings.Join(messages, "; "))
		}
		for _, hit := range payload.Code.Values {
			if result, ok := convertCodeSearchHit(hit); ok {
				out = append(out, result)
				if len(out) == limit {
					return out, nil
				}
			}
		}
		if payload.Code.IsLastPage || len(payload.Code.Values) == 0 {
			return out, nil
		}
		var next *int
		if payload.Code.NextStart != nil {
			next = payload.Code.NextStart
		} else {
			next = payload.Code.NextPageStart
		}
		// Some search plugin versions omit all paging metadata when the first
		// page contains every result.
		if next == nil {
			return out, nil
		}
		if *next <= start {
			return nil, fmt.Errorf("bitbucket search pagination did not advance: start=%d nextStart=%d", start, *next)
		}
		start = *next
	}
	return nil, fmt.Errorf("bitbucket search pagination incomplete: exceeded %d pages after %d results; nextStart=%d", maxPages, len(out), start)
}

func convertCodeSearchHit(hit codeSearchHit) (source.GlobalQueryResult, bool) {
	if hit.File == "" {
		return source.GlobalQueryResult{}, false
	}
	lines := hit.Lines
	grouped := len(hit.HitContexts) > 0
	if grouped {
		lines = nil
		for _, contextLines := range hit.HitContexts {
			lines = append(lines, contextLines...)
		}
	}
	snippet := make([]string, 0, len(lines))
	seen := make(map[int]bool, len(lines))
	start, end := 0, 0
	for _, line := range lines {
		// RestHitContext uses non-positive lines for separators between
		// disjoint matches. They are not source lines and must not skew bounds.
		if grouped && line.Line < 1 {
			continue
		}
		if line.Line > 0 && seen[line.Line] {
			continue
		}
		text := line.Text
		if text == "" {
			for _, segment := range line.Segments {
				text += segment.Text
			}
		}
		snippet = append(snippet, plainSearchText(text))
		if line.Line > 0 {
			seen[line.Line] = true
			if start == 0 || line.Line < start {
				start = line.Line
			}
			if line.Line > end {
				end = line.Line
			}
		}
	}
	if start < 1 {
		start = 1
	}
	if end < start {
		end = start
	}
	branch := string(hit.Repository.DefaultBranch)
	return source.GlobalQueryResult{
		ID: hit.Repository.ID, ProjectKey: hit.Repository.Project.Key, Slug: hit.Repository.Slug,
		Name: hit.Repository.Name, Ref: branch, DefaultBranch: branch,
		QueryResult: source.QueryResult{Path: hit.File, Snippet: strings.Join(snippet, "\n"), LineStart: start, LineEnd: end},
	}, true
}

func plainSearchText(value string) string {
	value = strings.ReplaceAll(value, "<em>", "")
	value = strings.ReplaceAll(value, "</em>", "")
	return html.UnescapeString(value)
}

func (c *Client) searchRequest(ctx context.Context, raw []byte) (*http.Response, error) {
	u, err := c.endpointURL(c.searchPath, nil)
	if err != nil {
		return nil, err
	}
	var lastErr error
	var wait time.Duration
	for attempt := 0; attempt < source.MaxAttempts; attempt++ {
		if wait > 0 {
			if err := source.Sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		c.authorize(req)
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || attempt == source.MaxAttempts-1 {
				return nil, err
			}
			wait = source.RetryDelay(nil, attempt)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			apiErr := &HTTPError{
				Source: "bitbucket", StatusCode: resp.StatusCode, Status: resp.Status,
				Body: string(limited), RetryAfter: source.RetryDelay(resp, attempt),
			}
			if source.RetryableStatus(resp.StatusCode) && attempt < source.MaxAttempts-1 {
				lastErr, wait = apiErr, apiErr.RetryAfter
				continue
			}
			return nil, apiErr
		}
		return resp, nil
	}
	return nil, lastErr
}
func (c *Client) GetPermissions(ctx context.Context, r source.RepositoryRef) ([]source.Permission, error) {
	type userItem struct {
		Permission string
		User       struct {
			Name   string
			Slug   string
			Active bool
		}
	}
	type groupItem struct {
		Permission string
		Group      struct{ Name string }
	}
	type permissionSet struct {
		users  []userItem
		groups []groupItem
	}
	load := func(prefix string, optional bool) (permissionSet, error) {
		users, err := pageAll[userItem](ctx, c, prefix+"/permissions/users")
		if err != nil {
			var statusErr *HTTPError
			if optional && errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden) {
				return permissionSet{}, nil
			}
			return permissionSet{}, err
		}
		groups, err := pageAll[groupItem](ctx, c, prefix+"/permissions/groups")
		if err != nil {
			var statusErr *HTTPError
			if optional && errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden) {
				return permissionSet{users: users}, nil
			}
			return permissionSet{}, err
		}
		return permissionSet{users: users, groups: groups}, nil
	}
	sets := make([]permissionSet, 0, 3)
	for _, item := range []struct {
		prefix   string
		optional bool
	}{
		{c.repo(r), false},
		{"/projects/" + escape(r.ProjectKey), false},
		{"/admin", true},
	} {
		set, err := load(item.prefix, item.optional)
		if err != nil {
			return nil, err
		}
		sets = append(sets, set)
	}
	permissions := map[string]source.Permission{}
	for index, set := range sets {
		for _, item := range set.users {
			if index == 2 && item.Permission != "ADMIN" && item.Permission != "SYS_ADMIN" {
				continue
			}
			if !item.User.Active {
				continue
			}
			principal := item.User.Slug
			if principal == "" {
				principal = item.User.Name
			}
			if principal != "" {
				permissions["user:"+principal] = source.Permission{Principal: principal, Kind: "user", Permission: item.Permission}
			}
		}
		for _, item := range set.groups {
			if index == 2 && item.Permission != "ADMIN" && item.Permission != "SYS_ADMIN" {
				continue
			}
			if item.Group.Name != "" {
				permissions["group:"+item.Group.Name] = source.Permission{Principal: item.Group.Name, Kind: "group", Permission: item.Permission}
			}
		}
	}
	for _, permission := range []string{"PROJECT_READ", "PROJECT_WRITE", "PROJECT_ADMIN"} {
		var result struct {
			Permitted bool `json:"permitted"`
		}
		if err := c.json(ctx, http.MethodGet, "/projects/"+escape(r.ProjectKey)+"/permissions/"+permission+"/all", nil, nil, &result); err != nil {
			return nil, err
		}
		if result.Permitted {
			permissions["all:bitbucket:licensed"] = source.Permission{Principal: "bitbucket:licensed", Kind: "all", Permission: permission}
			break
		}
	}
	out := make([]source.Permission, 0, len(permissions))
	for _, permission := range permissions {
		out = append(out, permission)
	}
	return out, nil
}
func (c *Client) RegisterWebhook(ctx context.Context, r source.RepositoryRef, target, secret string) error {
	body := map[string]any{"name": "git-ctx index webhook", "url": target, "active": true, "events": []string{"repo:refs_changed", "repo:modified"}, "configuration": map[string]string{"secret": secret}}
	return c.json(ctx, http.MethodPost, c.repo(r)+"/webhooks", nil, body, nil)
}

// CloseIdleConnections releases the pooled connections of this client when the
// adapter is replaced after a setting change.
func (c *Client) CloseIdleConnections() {
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func (c *Client) repo(r source.RepositoryRef) string {
	return "/projects/" + escape(r.ProjectKey) + "/repos/" + escape(r.Slug)
}
func escape(s string) string { return url.PathEscape(s) }
func escapePath(s string) (string, error) {
	if s == "" {
		return "", errors.New("Bitbucket file path is required")
	}
	if strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("invalid Bitbucket file path %q: absolute paths are not allowed", s)
	}
	parts := strings.Split(s, "/")
	for i := range parts {
		if parts[i] == "" || parts[i] == "." || parts[i] == ".." {
			return "", fmt.Errorf("invalid Bitbucket file path %q: empty and dot segments are not allowed", s)
		}
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/"), nil
}

// endpointURL appends an already escaped API path to the configured base URL.
// Assigning those paths directly to url.URL.Path makes '%' become '%25', so a
// project such as "Core Team" was sent as Core%2520Team.
func (c *Client) endpointURL(escapedSuffix string, q url.Values) (*url.URL, error) {
	u := *c.base
	rawPath := strings.TrimSuffix(c.base.EscapedPath(), "/") + escapedSuffix
	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, fmt.Errorf("build Bitbucket API URL: %w", err)
	}
	u.Path = decodedPath
	u.RawPath = rawPath
	u.RawQuery = q.Encode()
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return &u, nil
}

func (c *Client) authorize(req *http.Request) {
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
}

func (c *Client) request(ctx context.Context, method, endpoint string, q url.Values, body io.Reader) (*http.Response, error) {
	u, err := c.endpointURL(c.prefix+endpoint, q)
	if err != nil {
		return nil, err
	}
	// Only a request without a body can be replayed. Every read call qualifies,
	// and the one write call (webhook registration) is left to the caller.
	attempts := source.MaxAttempts
	if body != nil {
		attempts = 1
	}
	var lastErr error
	var wait time.Duration
	for attempt := 0; attempt < attempts; attempt++ {
		if wait > 0 {
			if err := source.Sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
		req, e := http.NewRequestWithContext(ctx, method, u.String(), body)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Accept", "application/json")
		c.authorize(req)
		resp, e := c.http.Do(req)
		if e != nil {
			// A connection reset or a closed keep-alive is worth one more try; a
			// cancelled context is not.
			lastErr = e
			if ctx.Err() != nil || attempt == attempts-1 {
				return nil, e
			}
			wait = source.RetryDelay(nil, attempt)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			apiErr := &HTTPError{Source: "bitbucket", StatusCode: resp.StatusCode, Status: resp.Status, Body: string(limited),
				RetryAfter: source.RetryDelay(resp, attempt)}
			if source.RetryableStatus(resp.StatusCode) && attempt < attempts-1 {
				lastErr, wait = apiErr, apiErr.RetryAfter
				continue
			}
			return nil, apiErr
		}
		return resp, nil
	}
	return nil, lastErr
}
func (c *Client) json(ctx context.Context, method, endpoint string, q url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		// Marshalling up front costs one small buffer and removes a goroutine
		// per call, and it keeps the body replayable should this client ever
		// retry a write. It is also how the GitLab client builds its bodies.
		raw, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(raw)
	}
	resp, e := c.request(ctx, method, endpoint, q, body)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if output == nil {
		_, e = io.Copy(io.Discard, resp.Body)
		return e
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(output); err != nil {
		return decodeFailure(endpoint, err)
	}
	return nil
}

// decodeFailure explains a body this client could not read. The standard
// library names the Go type it was decoding into, which reaches an MCP client
// as a struct definition and helps nobody: the causes worth checking are a base
// URL pointing at a proxy or login page, an unsupported server version, or a
// gateway that wrapped the response.
func decodeFailure(endpoint string, err error) error {
	return netclient.DecodeFailure("Bitbucket", "Bitbucket REST API", endpoint, err)
}
func (c *Client) bytes(ctx context.Context, endpoint string, q url.Values) ([]byte, error) {
	resp, e := c.request(ctx, http.MethodGet, endpoint, q, nil)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxFileBytes {
		return nil, fmt.Errorf("bitbucket file exceeds %d-byte limit: Content-Length is %d", maxFileBytes, resp.ContentLength)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxFileBytes {
		return nil, fmt.Errorf("bitbucket file exceeds %d-byte limit", maxFileBytes)
	}
	return content, nil
}

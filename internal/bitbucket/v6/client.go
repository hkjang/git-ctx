package v6

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
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
	base   *url.URL
	prefix string
	cfg    Config
	http   *http.Client
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("valid Bitbucket base URL is required")
	}
	if cfg.APIPrefix == "" {
		cfg.APIPrefix = "/rest/api/1.0"
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
	return &Client{base: u, prefix: "/" + strings.Trim(cfg.APIPrefix, "/"), cfg: cfg, http: httpClient}, nil
}

type page[T any] struct {
	Values        []T  `json:"values"`
	IsLastPage    bool `json:"isLastPage"`
	NextPageStart int  `json:"nextPageStart"`
}

type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("bitbucket API %s: %s", e.Status, e.Body)
}

func pageAll[T any](ctx context.Context, c *Client, endpoint string) ([]T, error) {
	start := 0
	var all []T
	for {
		q := url.Values{"limit": []string{"1000"}, "start": []string{fmt.Sprint(start)}}
		var p page[T]
		if err := c.json(ctx, http.MethodGet, endpoint, q, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Values...)
		if p.IsLastPage || len(p.Values) == 0 {
			return all, nil
		}
		start = p.NextPageStart
	}
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
		DefaultBranch           *struct {
			DisplayID string `json:"displayId"`
		} `json:"defaultBranch"`
	}
	items, e := pageAll[item](ctx, c, "/projects/"+escape(project)+"/repos")
	if e != nil {
		return nil, e
	}
	out := make([]source.Repository, len(items))
	for i, x := range items {
		branch := ""
		if x.DefaultBranch != nil {
			branch = x.DefaultBranch.DisplayID
		}
		out[i] = source.Repository{ID: x.ID, ProjectKey: x.Project.Key, Slug: x.Slug, Name: x.Name, Description: x.Description, DefaultBranch: branch, Archived: x.Archived}
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
	var p struct {
		Lines    []struct{ Text string } `json:"lines"`
		Children struct {
			Values []struct {
				Path struct {
					ToString string `json:"toString"`
				}
				Size int64
			} `json:"values"`
		} `json:"children"`
	}
	// The files endpoint is recursively paged by Bitbucket and returns canonical paths.
	var paths []string
	start := 0
	for {
		q.Set("start", fmt.Sprint(start))
		var x page[string]
		if e := c.json(ctx, http.MethodGet, c.repo(r)+"/files", q, nil, &x); e != nil {
			return nil, e
		}
		paths = append(paths, x.Values...)
		if x.IsLastPage || len(x.Values) == 0 {
			break
		}
		start = x.NextPageStart
	}
	out := make([]source.File, len(paths))
	for i, p := range paths {
		out[i] = source.File{Path: p}
	}
	_ = p
	return out, nil
}
func (c *Client) GetFile(ctx context.Context, r source.RepositoryRef, ref, filePath string) ([]byte, error) {
	q := url.Values{"at": []string{ref}}
	return c.bytes(ctx, c.repo(r)+"/raw/"+escapePath(filePath), q)
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
	var items []item
	start := 0
	for {
		q := url.Values{"since": []string{from}, "until": []string{to}, "limit": []string{"1000"}, "start": []string{fmt.Sprint(start)}}
		var current page[item]
		if err := c.json(ctx, http.MethodGet, c.repo(r)+"/changes", q, nil, &current); err != nil {
			return nil, err
		}
		items = append(items, current.Values...)
		if current.IsLastPage || len(current.Values) == 0 {
			break
		}
		start = current.NextPageStart
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
	for _, hit := range hits {
		result := hit.QueryResult
		result.CommitID = ref
		out = append(out, result)
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
	return c.searchCode(ctx, query, limit)
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
		DefaultBranch           *struct {
			DisplayID string `json:"displayId"`
		} `json:"defaultBranch"`
	}
	items, err := pageAll[item](ctx, c, "/repos?name="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	out := make([]source.Repository, 0, len(items))
	for _, x := range items {
		branch := ""
		if x.DefaultBranch != nil {
			branch = x.DefaultBranch.DisplayID
		}
		out = append(out, source.Repository{ID: x.ID, ProjectKey: x.Project.Key, Slug: x.Slug, Name: x.Name, Description: x.Description, DefaultBranch: branch, Archived: x.Archived})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (c *Client) searchCode(ctx context.Context, query string, limit int) ([]source.GlobalQueryResult, error) {
	if limit < 1 || limit > 50 {
		limit = 8
	}
	body := map[string]any{
		"query":    query,
		"entities": map[string]any{"code": map[string]int{"start": 0, "limit": limit}},
		"limits":   map[string]int{"primary": limit, "secondary": limit},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = strings.TrimSuffix(c.base.Path, "/") + "/" + strings.Trim(c.cfg.SearchAPIPath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("bitbucket search API %s: %s", resp.Status, string(limited))
	}
	var payload struct {
		Code struct {
			Values []struct {
				File       string `json:"file"`
				Repository struct {
					ID            int64                `json:"id"`
					Slug          string               `json:"slug"`
					Name          string               `json:"name"`
					Project       struct{ Key string } `json:"project"`
					DefaultBranch string               `json:"defaultBranch"`
				} `json:"repository"`
				Lines []struct {
					Line     int    `json:"line"`
					Text     string `json:"text"`
					Segments []struct {
						Text string `json:"text"`
					} `json:"segments"`
				} `json:"lines"`
			} `json:"values"`
		} `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]source.GlobalQueryResult, 0, len(payload.Code.Values))
	for _, hit := range payload.Code.Values {
		if hit.File == "" {
			continue
		}
		var lines []string
		start, end := 0, 0
		for _, line := range hit.Lines {
			text := line.Text
			if text == "" {
				for _, segment := range line.Segments {
					text += segment.Text
				}
			}
			lines = append(lines, text)
			if start == 0 || line.Line < start {
				start = line.Line
			}
			if line.Line > end {
				end = line.Line
			}
		}
		if start < 1 {
			start = 1
		}
		if end < start {
			end = start
		}
		out = append(out, source.GlobalQueryResult{
			ID: hit.Repository.ID, ProjectKey: hit.Repository.Project.Key, Slug: hit.Repository.Slug,
			Name: hit.Repository.Name, Ref: hit.Repository.DefaultBranch, DefaultBranch: hit.Repository.DefaultBranch,
			QueryResult: source.QueryResult{Path: hit.File, Snippet: strings.Join(lines, "\n"), LineStart: start, LineEnd: end},
		})
	}
	return out, nil
}
func (c *Client) GetPermissions(ctx context.Context, r source.RepositoryRef) ([]source.Permission, error) {
	type userItem struct {
		Permission string
		User       struct{ Name, Slug string }
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
func (c *Client) repo(r source.RepositoryRef) string {
	return "/projects/" + escape(r.ProjectKey) + "/repos/" + escape(r.Slug)
}
func escape(s string) string { return url.PathEscape(s) }
func escapePath(s string) string {
	parts := strings.Split(strings.TrimPrefix(s, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return path.Join(parts...)
}
func (c *Client) request(ctx context.Context, method, endpoint string, q url.Values, body io.Reader) (*http.Response, error) {
	u := *c.base
	u.Path = strings.TrimSuffix(c.base.Path, "/") + c.prefix + endpoint
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, e := http.NewRequestWithContext(ctx, method, u.String(), body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(limited)}
	}
	return resp, nil
}
func (c *Client) json(ctx context.Context, method, endpoint string, q url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		r, w := io.Pipe()
		go func() { e := json.NewEncoder(w).Encode(input); _ = w.CloseWithError(e) }()
		body = r
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
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(output)
}
func (c *Client) bytes(ctx context.Context, endpoint string, q url.Values) ([]byte, error) {
	resp, e := c.request(ctx, http.MethodGet, endpoint, q, nil)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

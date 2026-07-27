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
func (c *Client) SearchQuery(ctx context.Context, r source.RepositoryRef, ref, query string, limit int) ([]source.QueryResult, error) {
	if limit < 1 || limit > 50 {
		limit = 8
	}
	body := map[string]any{
		"query":    fmt.Sprintf("project:%s repo:%s %s", r.ProjectKey, r.Slug, query),
		"entities": map[string]any{"code": map[string]int{"start": 0, "limit": limit}},
		"limits":   map[string]int{"primary": limit, "secondary": limit},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u := *c.base
	u.Path = strings.TrimSuffix(c.base.Path, "/") + "/rest/search/latest/search"
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
				File  string `json:"file"`
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
	out := make([]source.QueryResult, 0, len(payload.Code.Values))
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
		out = append(out, source.QueryResult{Path: hit.File, Snippet: strings.Join(lines, "\n"), CommitID: ref, LineStart: start, LineEnd: end})
	}
	return out, nil
}
func (c *Client) GetPermissions(ctx context.Context, r source.RepositoryRef) ([]source.Permission, error) {
	type userItem struct {
		Permission string
		User       struct{ Name, Slug string }
	}
	users, e := pageAll[userItem](ctx, c, c.repo(r)+"/permissions/users")
	if e != nil {
		return nil, e
	}
	type groupItem struct{ Permission, Group string }
	groups, e := pageAll[groupItem](ctx, c, c.repo(r)+"/permissions/groups")
	if e != nil {
		return nil, e
	}
	out := make([]source.Permission, 0, len(users)+len(groups))
	for _, x := range users {
		p := x.User.Slug
		if p == "" {
			p = x.User.Name
		}
		out = append(out, source.Permission{Principal: p, Kind: "user", Permission: x.Permission})
	}
	for _, x := range groups {
		out = append(out, source.Permission{Principal: x.Group, Kind: "group", Permission: x.Permission})
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
		return nil, fmt.Errorf("bitbucket API %s: %s", resp.Status, string(limited))
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

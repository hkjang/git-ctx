package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git-ctx/internal/netclient"
	"git-ctx/internal/source"
)

type Config struct {
	BaseURL       string
	Token         string
	Timeout       time.Duration
	TLSVerify     *bool
	CACertificate string
	ProxyURL      string
}
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("valid GitLab base URL is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	httpClient, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	return &Client{base: u, token: cfg.Token, http: httpClient}, nil
}

type group struct {
	ID                      int64
	Path, Name, Description string
}
type project struct {
	ID                                                        int64
	Path, Name, Description, DefaultBranch, PathWithNamespace string `json:"-"`
	Namespace                                                 struct{ Path string }
}

func (p *project) UnmarshalJSON(data []byte) error {
	type alias project
	var raw struct {
		ID                int64  `json:"id"`
		Path              string `json:"path"`
		Name              string `json:"name"`
		Description       string `json:"description"`
		DefaultBranch     string `json:"default_branch"`
		PathWithNamespace string `json:"path_with_namespace"`
		Namespace         struct {
			Path string `json:"path"`
		} `json:"namespace"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.ID = raw.ID
	p.Path = raw.Path
	p.Name = raw.Name
	p.Description = raw.Description
	p.DefaultBranch = raw.DefaultBranch
	p.PathWithNamespace = raw.PathWithNamespace
	p.Namespace.Path = raw.Namespace.Path
	return nil
}

func (c *Client) ListProjects(ctx context.Context) ([]source.Project, error) {
	var groups []group
	if err := c.pages(ctx, "/groups", nil, &groups); err != nil {
		return nil, err
	}
	out := make([]source.Project, len(groups))
	for i, g := range groups {
		out[i] = source.Project{Key: strconv.FormatInt(g.ID, 10), Name: g.Name, Description: g.Description}
	}
	return out, nil
}
func (c *Client) ListRepositories(ctx context.Context, groupID string) ([]source.Repository, error) {
	var items []project
	if err := c.pages(ctx, "/groups/"+escape(groupID)+"/projects", url.Values{"include_subgroups": []string{"true"}}, &items); err != nil {
		return nil, err
	}
	out := make([]source.Repository, len(items))
	for i, p := range items {
		out[i] = source.Repository{ID: p.ID, ProjectKey: p.Namespace.Path, Slug: p.Path, Name: p.Name, Description: p.Description, DefaultBranch: p.DefaultBranch}
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
	var items []struct {
		Name    string
		Commit  struct{ ID string }
		Default bool
	}
	if err := c.pages(ctx, c.repo(r)+"/repository/"+kind, nil, &items); err != nil {
		return nil, err
	}
	out := make([]source.Reference, len(items))
	for i, x := range items {
		out[i] = source.Reference{ID: x.Name, Name: x.Name, LatestCommit: x.Commit.ID, Default: x.Default}
	}
	return out, nil
}
func (c *Client) GetCommit(ctx context.Context, r source.RepositoryRef, id string) (source.Commit, error) {
	var x struct {
		ID         string `json:"id"`
		ShortID    string `json:"short_id"`
		Title      string `json:"title"`
		Message    string `json:"message"`
		AuthorName string `json:"author_name"`
	}
	if err := c.json(ctx, http.MethodGet, c.repo(r)+"/repository/commits/"+escape(id), nil, nil, &x); err != nil {
		return source.Commit{}, err
	}
	return source.Commit{ID: x.ID, DisplayID: x.ShortID, Message: x.Message, Author: x.AuthorName}, nil
}
func (c *Client) ListFiles(ctx context.Context, r source.RepositoryRef, ref string) ([]source.File, error) {
	var items []struct{ Path, Type string }
	q := url.Values{"ref": []string{ref}, "recursive": []string{"true"}}
	if err := c.pages(ctx, c.repo(r)+"/repository/tree", q, &items); err != nil {
		return nil, err
	}
	var out []source.File
	for _, x := range items {
		if x.Type == "blob" {
			out = append(out, source.File{Path: x.Path})
		}
	}
	return out, nil
}
func (c *Client) GetFile(ctx context.Context, r source.RepositoryRef, ref, filePath string) ([]byte, error) {
	q := url.Values{"ref": []string{ref}}
	return c.bytes(ctx, c.repo(r)+"/repository/files/"+escape(filePath)+"/raw", q)
}
func (c *Client) GetPermissions(ctx context.Context, r source.RepositoryRef) ([]source.Permission, error) {
	var members []struct {
		ID          int64
		Username    string
		AccessLevel int `json:"access_level"`
	}
	if err := c.pages(ctx, c.repo(r)+"/members/all", nil, &members); err != nil {
		return nil, err
	}
	out := make([]source.Permission, len(members))
	for i, m := range members {
		out[i] = source.Permission{Principal: "gitlab:" + strconv.FormatInt(m.ID, 10), Kind: "user", Permission: accessName(m.AccessLevel)}
	}
	return out, nil
}
func (c *Client) RegisterWebhook(ctx context.Context, r source.RepositoryRef, target, secret string) error {
	body := map[string]any{"url": target, "token": secret, "push_events": true, "tag_push_events": true, "enable_ssl_verification": true}
	return c.json(ctx, http.MethodPost, c.repo(r)+"/hooks", nil, body, nil)
}
func (c *Client) repo(r source.RepositoryRef) string {
	return "/projects/" + escape(r.ProjectKey+"/"+r.Slug)
}
func escape(s string) string { return url.PathEscape(s) }
func accessName(level int) string {
	switch {
	case level >= 50:
		return "owner"
	case level >= 40:
		return "maintainer"
	case level >= 30:
		return "developer"
	case level >= 20:
		return "reporter"
	default:
		return "guest"
	}
}
func (c *Client) endpoint(p string, q url.Values) string {
	u := *c.base
	rawPath := strings.TrimSuffix(c.base.EscapedPath(), "/") + "/api/v4" + p
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		decoded = rawPath
		rawPath = ""
	}
	u.Path = decoded
	u.RawPath = rawPath
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String()
}
func (c *Client) request(ctx context.Context, method, p string, q url.Values, input any) (*http.Response, error) {
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.endpoint(p, q), body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("gitlab API %s: %s", resp.Status, string(raw))
	}
	return resp, nil
}
func (c *Client) json(ctx context.Context, method, p string, q url.Values, input, output any) error {
	resp, e := c.request(ctx, method, p, q, input)
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
func (c *Client) pages(ctx context.Context, p string, q url.Values, output any) error {
	if q == nil {
		q = url.Values{}
	}
	page := 1
	// Decode each page into RawMessage first so callers retain strong typing.
	var all []json.RawMessage
	for {
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		resp, e := c.request(ctx, http.MethodGet, p, q, nil)
		if e != nil {
			return e
		}
		var current []json.RawMessage
		e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&current)
		next := resp.Header.Get("X-Next-Page")
		resp.Body.Close()
		if e != nil {
			return e
		}
		all = append(all, current...)
		if next == "" {
			break
		}
		page, _ = strconv.Atoi(next)
		if page <= 0 {
			return errors.New("invalid GitLab pagination header")
		}
	}
	raw, _ := json.Marshal(all)
	return json.Unmarshal(raw, output)
}
func (c *Client) bytes(ctx context.Context, p string, q url.Values) ([]byte, error) {
	resp, e := c.request(ctx, http.MethodGet, p, q, nil)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

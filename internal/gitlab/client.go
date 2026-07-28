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
	"sync"
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

	// projectCache keeps the id to namespace mapping used when advanced search
	// returns blob hits that only carry a numeric project id.
	projectMu    sync.Mutex
	projectCache map[int64]source.Repository
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
	return &Client{base: u, token: cfg.Token, http: httpClient, projectCache: map[int64]source.Repository{}}, nil
}

type group struct {
	ID                      int64
	Path, Name, Description string
}
type project struct {
	ID                                                        int64
	Path, Name, Description, DefaultBranch, PathWithNamespace string
	Archived                                                  bool
	Namespace                                                 struct{ Path, FullPath string }
}

func (p *project) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID                int64  `json:"id"`
		Path              string `json:"path"`
		Name              string `json:"name"`
		Description       string `json:"description"`
		DefaultBranch     string `json:"default_branch"`
		PathWithNamespace string `json:"path_with_namespace"`
		Archived          bool   `json:"archived"`
		Namespace         struct {
			Path     string `json:"path"`
			FullPath string `json:"full_path"`
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
	p.Archived = raw.Archived
	p.Namespace.Path = raw.Namespace.Path
	p.Namespace.FullPath = raw.Namespace.FullPath
	return nil
}

// repository converts a GitLab project into the catalog model. GitLab nests
// projects in subgroups, so the namespace full path is required: using only the
// direct parent path builds URL-encoded project IDs such as `sub%2Fproject`
// that every later repository, search or ACL call resolves to 404.
func (p *project) repository() source.Repository {
	namespace := strings.TrimSpace(p.Namespace.FullPath)
	if namespace == "" {
		if trimmed := strings.TrimSuffix(p.PathWithNamespace, "/"+p.Path); trimmed != p.PathWithNamespace {
			namespace = trimmed
		} else {
			namespace = p.Namespace.Path
		}
	}
	return source.Repository{
		ID: p.ID, ProjectKey: namespace, Slug: p.Path, Name: p.Name,
		Description: p.Description, DefaultBranch: p.DefaultBranch, Archived: p.Archived,
	}
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
		out[i] = p.repository()
		c.cacheProject(p)
	}
	return out, nil
}

// SearchRepositories asks GitLab to match the term against project and
// namespace names instead of downloading every group and project page, so
// discovery stays responsive on large instances.
func (c *Client) SearchRepositories(ctx context.Context, query string, limit int) ([]source.Repository, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var items []project
	params := url.Values{
		"search":            []string{query},
		"search_namespaces": []string{"true"},
		"order_by":          []string{"last_activity_at"},
		"simple":            []string{"false"},
		"per_page":          []string{strconv.Itoa(limit)},
	}
	if err := c.json(ctx, http.MethodGet, "/projects", params, nil, &items); err != nil {
		return nil, err
	}
	out := make([]source.Repository, 0, len(items))
	for _, p := range items {
		c.cacheProject(p)
		out = append(out, p.repository())
	}
	return out, nil
}

// SearchGlobalQuery runs GitLab advanced search across every project the token
// can read. Instances without advanced search reject the blobs scope, which is
// reported as source.ErrGlobalSearchUnsupported so the caller can fall back to
// per repository search instead of failing the whole request.
func (c *Client) SearchGlobalQuery(ctx context.Context, query string, limit int) ([]source.GlobalQueryResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var items []blobHit
	params := url.Values{"scope": []string{"blobs"}, "search": []string{query}, "per_page": []string{strconv.Itoa(limit)}}
	if err := c.json(ctx, http.MethodGet, "/search", params, nil, &items); err != nil {
		if globalSearchUnsupported(err) {
			return nil, fmt.Errorf("%w: %v", source.ErrGlobalSearchUnsupported, err)
		}
		return nil, err
	}
	out := make([]source.GlobalQueryResult, 0, len(items))
	for _, item := range items {
		if item.Path == "" || item.ProjectID == 0 {
			continue
		}
		repository, err := c.project(ctx, item.ProjectID)
		if err != nil {
			continue
		}
		ref := item.Ref
		if ref == "" {
			ref = repository.DefaultBranch
		}
		start := max(1, item.StartLine)
		out = append(out, source.GlobalQueryResult{
			ProjectKey: repository.ProjectKey, Slug: repository.Slug, Name: repository.Name,
			Description: repository.Description, Ref: ref, DefaultBranch: repository.DefaultBranch, ID: repository.ID,
			QueryResult: source.QueryResult{Path: item.Path, Snippet: item.Data, LineStart: start, LineEnd: start + max(0, strings.Count(item.Data, "\n"))},
		})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

type blobHit struct {
	Path      string `json:"path"`
	Data      string `json:"data"`
	Ref       string `json:"ref"`
	StartLine int    `json:"startline"`
	ProjectID int64  `json:"project_id"`
}

// globalSearchUnsupported detects the responses returned by GitLab instances
// that run basic search only. Those reject the blobs scope with 400 or 403
// instead of returning an empty result set.
func globalSearchUnsupported(err error) bool {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "scope") && (strings.Contains(message, "not supported") || strings.Contains(message, "does not have a valid value")) {
		return true
	}
	return strings.Contains(message, "400 bad request") || strings.Contains(message, "403 forbidden") || strings.Contains(message, "501 not implemented")
}

func (c *Client) cacheProject(p project) {
	if p.ID == 0 {
		return
	}
	c.projectMu.Lock()
	defer c.projectMu.Unlock()
	c.projectCache[p.ID] = p.repository()
}

func (c *Client) project(ctx context.Context, id int64) (source.Repository, error) {
	c.projectMu.Lock()
	cached, ok := c.projectCache[id]
	c.projectMu.Unlock()
	if ok {
		return cached, nil
	}
	var p project
	if err := c.json(ctx, http.MethodGet, "/projects/"+strconv.FormatInt(id, 10), nil, nil, &p); err != nil {
		return source.Repository{}, err
	}
	c.cacheProject(p)
	return p.repository(), nil
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
func (c *Client) Changes(ctx context.Context, r source.RepositoryRef, from, to string) ([]source.Change, error) {
	var result struct {
		CompareTimeout bool `json:"compare_timeout"`
		Diffs          []struct {
			OldPath     string `json:"old_path"`
			NewPath     string `json:"new_path"`
			NewFile     bool   `json:"new_file"`
			DeletedFile bool   `json:"deleted_file"`
			RenamedFile bool   `json:"renamed_file"`
		} `json:"diffs"`
	}
	q := url.Values{"from": []string{from}, "to": []string{to}, "straight": []string{"true"}}
	if err := c.json(ctx, http.MethodGet, c.repo(r)+"/repository/compare", q, nil, &result); err != nil {
		return nil, err
	}
	if result.CompareTimeout {
		return nil, errors.New("gitlab compare timed out")
	}
	out := make([]source.Change, 0, len(result.Diffs))
	for _, item := range result.Diffs {
		change := source.Change{Path: item.NewPath, OldPath: item.OldPath, Type: "modified"}
		switch {
		case item.DeletedFile:
			change.Type, change.Path = "deleted", item.OldPath
		case item.NewFile:
			change.Type, change.OldPath = "added", ""
		case item.RenamedFile:
			change.Type = "renamed"
		}
		if change.Path != "" || change.OldPath != "" {
			out = append(out, change)
		}
	}
	return out, nil
}
func (c *Client) SearchQuery(ctx context.Context, r source.RepositoryRef, ref, query string, limit int) ([]source.QueryResult, error) {
	if limit < 1 || limit > 50 {
		limit = 8
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	var items []blobHit
	params := url.Values{"scope": []string{"blobs"}, "search": []string{query}, "per_page": []string{strconv.Itoa(limit)}}
	if ref = strings.TrimSpace(ref); ref != "" {
		params.Set("ref", ref)
	}
	err := c.json(ctx, http.MethodGet, c.repo(r)+"/search", params, nil, &items)
	if err != nil && ref != "" {
		// GitLab rejects the whole search when the ref is missing on the remote
		// side, for example after a default branch rename. Retrying on the
		// default branch keeps the search useful instead of returning nothing.
		params.Del("ref")
		items = nil
		err = c.json(ctx, http.MethodGet, c.repo(r)+"/search", params, nil, &items)
	}
	if err != nil {
		return nil, err
	}
	out := make([]source.QueryResult, 0, min(limit, len(items)))
	seen := map[string]bool{}
	for _, item := range items {
		if item.Path == "" || seen[item.Path] {
			continue
		}
		seen[item.Path] = true
		start := max(1, item.StartLine)
		out = append(out, source.QueryResult{Path: item.Path, Snippet: item.Data, CommitID: item.Ref, LineStart: start, LineEnd: start + max(0, strings.Count(item.Data, "\n"))})
		if len(out) == limit {
			break
		}
	}
	return out, nil
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
	var project struct {
		Visibility string `json:"visibility"`
	}
	if err := c.json(ctx, http.MethodGet, c.repo(r), nil, nil, &project); err != nil {
		return nil, err
	}
	// Every git-ctx query is authenticated. GitLab public and internal projects
	// are therefore readable by all platform users without an explicit member row.
	if project.Visibility == "public" {
		out = append(out, source.Permission{Principal: "*", Kind: "all", Permission: "read"})
	} else if project.Visibility == "internal" {
		out = append(out, source.Permission{Principal: "gitlab:authenticated", Kind: "all", Permission: "read"})
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

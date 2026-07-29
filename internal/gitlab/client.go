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
	params := url.Values{"scope": []string{"blobs"}, "search": []string{query}}
	out := make([]source.GlobalQueryResult, 0, limit)
	seen := map[string]bool{}
	var projectLookupErr error
	err := c.searchBlobs(ctx, "/search", params, limit, func(item blobHit) bool {
		path := item.filePath()
		if path == "" || item.ProjectID == 0 {
			return true
		}
		key := strconv.FormatInt(item.ProjectID, 10) + "\x00" + path
		if seen[key] {
			return true
		}
		seen[key] = true
		repository, projectErr := c.project(ctx, item.ProjectID)
		if projectErr != nil {
			// A project can disappear between the search and metadata calls.
			// Authentication, transport and server failures are different: if
			// they are swallowed, a broken credential looks like a successful
			// search with zero hits and the caller never runs its fallback.
			if source.IsNotFound(projectErr) {
				return true
			}
			projectLookupErr = fmt.Errorf("resolve GitLab project %d: %w", item.ProjectID, projectErr)
			return false
		}
		ref := item.Ref
		if ref == "" {
			ref = repository.DefaultBranch
		}
		start := max(1, item.StartLine)
		out = append(out, source.GlobalQueryResult{
			ProjectKey: repository.ProjectKey, Slug: repository.Slug, Name: repository.Name,
			Description: repository.Description, Ref: ref, DefaultBranch: repository.DefaultBranch, ID: repository.ID,
			QueryResult: source.QueryResult{Path: path, Snippet: item.Data, CommitID: ref, LineStart: start, LineEnd: start + max(0, strings.Count(item.Data, "\n"))},
		})
		return len(out) < limit
	})
	if projectLookupErr != nil {
		return nil, projectLookupErr
	}
	if err != nil {
		if globalSearchUnsupported(err) {
			// Preserve both classifications: callers need the fallback sentinel,
			// while diagnostics and health reporting still need the HTTP status.
			return nil, fmt.Errorf("%w: %w", source.ErrGlobalSearchUnsupported, err)
		}
		return nil, err
	}
	return out, nil
}

type blobHit struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Data      string `json:"data"`
	Ref       string `json:"ref"`
	StartLine int    `json:"startline"`
	ProjectID int64  `json:"project_id"`
}

func (h blobHit) filePath() string {
	if h.Path != "" {
		return h.Path
	}
	// GitLab kept filename as the full repository-relative path before adding
	// path. It is deprecated but still makes responses from older self-managed
	// versions usable.
	return h.Filename
}

// globalSearchUnsupported detects the responses returned by GitLab instances
// that run basic search only. Those reject the blobs scope with 400 or 403
// instead of returning an empty result set.
func globalSearchUnsupported(err error) bool {
	status := source.StatusOf(err)
	if status == http.StatusNotImplemented {
		return true
	}
	if status != 0 && status != http.StatusBadRequest && status != http.StatusForbidden {
		return false
	}
	message := strings.ToLower(err.Error())
	feature := strings.Contains(message, "scope") ||
		strings.Contains(message, "advanced search") ||
		strings.Contains(message, "exact code search") ||
		strings.Contains(message, "global search") ||
		strings.Contains(message, "code search")
	unavailable := strings.Contains(message, "does not have a valid value") ||
		strings.Contains(message, "not supported") ||
		strings.Contains(message, "unsupported") ||
		strings.Contains(message, "not enabled") ||
		strings.Contains(message, "unavailable") ||
		strings.Contains(message, "disabled") ||
		strings.Contains(message, "invalid scope")
	return feature && unavailable
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

// SearchChangeRequests searches merge requests of one project. GitLab filters
// server side, which keeps the response small on busy repositories.
func (c *Client) SearchChangeRequests(ctx context.Context, r source.RepositoryRef, query, state string, limit int) ([]source.ChangeRequest, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	params := url.Values{"per_page": []string{strconv.Itoa(limit)}, "order_by": []string{"updated_at"}, "sort": []string{"desc"}}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "all":
		params.Set("state", "all")
	case "open", "opened":
		params.Set("state", "opened")
	case "merged":
		params.Set("state", "merged")
	case "closed":
		params.Set("state", "closed")
	default:
		params.Set("state", "all")
	}
	if query = strings.TrimSpace(query); query != "" {
		params.Set("search", query)
	}
	var items []struct {
		IID          int64     `json:"iid"`
		Title        string    `json:"title"`
		Description  string    `json:"description"`
		State        string    `json:"state"`
		WebURL       string    `json:"web_url"`
		SourceBranch string    `json:"source_branch"`
		TargetBranch string    `json:"target_branch"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
		Author       struct {
			Name string `json:"name"`
		} `json:"author"`
	}
	if err := c.json(ctx, http.MethodGet, c.repo(r)+"/merge_requests", params, nil, &items); err != nil {
		return nil, err
	}
	out := make([]source.ChangeRequest, 0, len(items))
	for _, item := range items {
		out = append(out, source.ChangeRequest{
			ID: fmt.Sprintf("!%d", item.IID), Number: item.IID, Title: item.Title, Description: item.Description,
			State: item.State, Author: item.Author.Name, SourceRef: item.SourceBranch, TargetRef: item.TargetBranch,
			URL: item.WebURL, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		})
	}
	return out, nil
}

// ListCommits returns the commits that touched a path, newest first.
func (c *Client) ListCommits(ctx context.Context, r source.RepositoryRef, refName, filePath string, limit int) ([]source.Commit, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var items []struct {
		ID            string    `json:"id"`
		ShortID       string    `json:"short_id"`
		Title         string    `json:"title"`
		Message       string    `json:"message"`
		AuthorName    string    `json:"author_name"`
		AuthorEmail   string    `json:"author_email"`
		CommittedDate time.Time `json:"committed_date"`
		WebURL        string    `json:"web_url"`
	}
	params := url.Values{"per_page": []string{strconv.Itoa(limit)}}
	if refName != "" {
		params.Set("ref_name", refName)
	}
	if filePath != "" {
		params.Set("path", filePath)
	}
	if err := c.json(ctx, http.MethodGet, c.repo(r)+"/repository/commits", params, nil, &items); err != nil {
		return nil, err
	}
	out := make([]source.Commit, 0, len(items))
	for _, item := range items {
		message := item.Message
		if strings.TrimSpace(message) == "" {
			message = item.Title
		}
		out = append(out, source.Commit{
			ID: item.ID, DisplayID: item.ShortID, Message: message, Author: item.AuthorName,
			AuthorEmail: item.AuthorEmail, AuthoredAt: item.CommittedDate, URL: item.WebURL,
		})
	}
	return out, nil
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
	params := url.Values{"scope": []string{"blobs"}, "search": []string{query}}
	if ref = strings.TrimSpace(ref); ref != "" {
		params.Set("ref", ref)
	}
	out := make([]source.QueryResult, 0, limit)
	seen := map[string]bool{}
	err := c.searchBlobs(ctx, c.repo(r)+"/search", params, limit, func(item blobHit) bool {
		path := item.filePath()
		if path == "" || seen[path] {
			return true
		}
		seen[path] = true
		start := max(1, item.StartLine)
		out = append(out, source.QueryResult{Path: path, Snippet: item.Data, CommitID: item.Ref, LineStart: start, LineEnd: start + max(0, strings.Count(item.Data, "\n"))})
		return len(out) < limit
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (c *Client) GetPermissions(ctx context.Context, r source.RepositoryRef) ([]source.Permission, error) {
	var members []struct {
		ID          int64
		State       string
		AccessLevel int `json:"access_level"`
	}
	if err := c.pages(ctx, c.repo(r)+"/members/all", nil, &members); err != nil {
		return nil, err
	}
	out := make([]source.Permission, 0, len(members))
	for _, m := range members {
		// /members/all can retain users whose account is blocked. GitLab denies
		// those users repository access, so carrying their old membership into
		// the local ACL would expose indexed content that GitLab itself hides.
		// Guest and lower base roles cannot read private project repository code.
		// Planner (15) can read code on current GitLab versions. Custom Guest
		// capabilities are not present in this response, so those fail closed.
		if m.ID == 0 ||
			!strings.EqualFold(strings.TrimSpace(m.State), "active") ||
			m.AccessLevel < 15 {
			continue
		}
		out = append(out, source.Permission{Principal: "gitlab:" + strconv.FormatInt(m.ID, 10), Kind: "user", Permission: accessName(m.AccessLevel)})
	}
	var project struct {
		Visibility            string `json:"visibility"`
		RepositoryAccessLevel string `json:"repository_access_level"`
	}
	if err := c.json(ctx, http.MethodGet, c.repo(r), nil, nil, &project); err != nil {
		return nil, err
	}
	repositoryAccess := strings.ToLower(strings.TrimSpace(project.RepositoryAccessLevel))
	switch repositoryAccess {
	case "disabled":
		// The repository feature is unavailable even to project members. Do not
		// preserve access to stale indexed content after it is disabled.
		return nil, nil
	case "private":
		// Only the active, read-capable members collected above may read code.
		return out, nil
	case "enabled", "":
		// Older GitLab versions omit repository_access_level. Preserve their
		// historical project-visibility behavior explicitly for compatibility.
	default:
		// New or malformed feature levels fail closed for broad principals while
		// retaining only individually verified memberships.
		return out, nil
	}
	// Every git-ctx query is authenticated. GitLab public and internal projects
	// are therefore readable by all platform users without an explicit member row.
	if strings.EqualFold(strings.TrimSpace(project.Visibility), "public") {
		out = append(out, source.Permission{Principal: "*", Kind: "all", Permission: "read"})
	} else if strings.EqualFold(strings.TrimSpace(project.Visibility), "internal") {
		out = append(out, source.Permission{Principal: "gitlab:authenticated", Kind: "all", Permission: "read"})
	}
	return out, nil
}
func (c *Client) RegisterWebhook(ctx context.Context, r source.RepositoryRef, target, secret string) error {
	body := map[string]any{"url": target, "token": secret, "push_events": true, "tag_push_events": true, "enable_ssl_verification": true}
	return c.json(ctx, http.MethodPost, c.repo(r)+"/hooks", nil, body, nil)
}

// CloseIdleConnections releases the pooled connections of this client. The
// application calls it when an administrator changes the setting and the
// adapter is replaced, so the old transport does not keep sockets open to a
// host that is no longer in use.
func (c *Client) CloseIdleConnections() {
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
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
	case level >= 15:
		// Planner is a read-capable role. Emit the local capability name rather
		// than a role name so older consumers also authorize it correctly.
		return "read"
	default:
		return "guest"
	}
}
func (c *Client) endpoint(p string, q url.Values) string {
	u := *c.base
	// A configured base URL contributes only scheme, authority and path. Carrying
	// an accidental query or fragment into API requests changes nil-query calls
	// and can leak unrelated configuration into every request.
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
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
	// A request without a body can be replayed, which covers every read call.
	attempts := source.MaxAttempts
	if input != nil {
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
			lastErr = e
			if ctx.Err() != nil || attempt == attempts-1 {
				return nil, e
			}
			wait = source.RetryDelay(nil, attempt)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			// The typed error lets the search layer stop on an expired token
			// instead of repeating it for every repository, and skip a deleted
			// repository without treating the instance as down.
			apiErr := &source.APIError{Source: "gitlab", StatusCode: resp.StatusCode, Status: resp.Status,
				Body: string(raw), RetryAfter: source.RetryDelay(resp, attempt)}
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

// maxPages bounds one paginated read at 100 items per page. Hitting the bound
// while GitLab still advertises another page is an error, never a complete
// result: consumers use these lists to replace indexes and ACL snapshots.
const maxPages = 50

var errPaginationLimitExceeded = errors.New("gitlab pagination safety limit exceeded")

// searchBlobs streams paginated blob matches to visit. GitLab can return
// multiple entries for one file, so callers deduplicate while visiting and
// continue to later pages until they have the requested number of distinct
// files.
func (c *Client) searchBlobs(ctx context.Context, p string, q url.Values, perPage int, visit func(blobHit) bool) error {
	q = cloneValues(q)
	page := 1
	for pages := 0; pages < maxPages; pages++ {
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))
		resp, err := c.request(ctx, http.MethodGet, p, q, nil)
		if err != nil {
			return err
		}
		var current []blobHit
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&current)
		resp.Body.Close()
		if decodeErr != nil {
			return decodeErr
		}
		for _, item := range current {
			if !visit(item) {
				return nil
			}
		}
		next, nextErr := nextPage(resp, page)
		if nextErr != nil {
			return nextErr
		}
		if next == 0 {
			return nil
		}
		page = next
	}
	return fmt.Errorf("%w after %d pages for %s", errPaginationLimitExceeded, maxPages, p)
}

func (c *Client) pages(ctx context.Context, p string, q url.Values, output any) error {
	q = cloneValues(q)
	page := 1
	// Decode each page into RawMessage first so callers retain strong typing.
	var all []json.RawMessage
	// An instance with tens of thousands of projects would otherwise be pulled
	// into memory in full by one discovery call. The cap is high enough for a
	// normal on-premises group and low enough to stay a bounded request.
	for pages := 0; pages < maxPages; pages++ {
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		resp, e := c.request(ctx, http.MethodGet, p, q, nil)
		if e != nil {
			return e
		}
		var current []json.RawMessage
		e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&current)
		resp.Body.Close()
		if e != nil {
			return e
		}
		all = append(all, current...)
		next, nextErr := nextPage(resp, page)
		if nextErr != nil {
			return nextErr
		}
		if next == 0 {
			raw, _ := json.Marshal(all)
			return json.Unmarshal(raw, output)
		}
		page = next
	}
	return fmt.Errorf("%w after %d pages for %s", errPaginationLimitExceeded, maxPages, p)
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

// nextPage prefers GitLab's X-Next-Page convenience header and then its
// standards-based Link header. Some GitLab.com responses omit X-Next-Page.
func nextPage(resp *http.Response, current int) (int, error) {
	if value := strings.TrimSpace(resp.Header.Get("X-Next-Page")); value != "" {
		return parseNextPage(value, current, "X-Next-Page")
	}
	for _, header := range resp.Header.Values("Link") {
		for _, entry := range strings.Split(header, ",") {
			parts := strings.Split(entry, ";")
			if len(parts) < 2 || !hasNextRelation(parts[1:]) {
				continue
			}
			target := strings.TrimSpace(parts[0])
			if len(target) < 2 || target[0] != '<' || target[len(target)-1] != '>' {
				return 0, errors.New("invalid GitLab pagination Link header")
			}
			parsed, err := url.Parse(target[1 : len(target)-1])
			if err != nil {
				return 0, fmt.Errorf("invalid GitLab pagination Link header: %w", err)
			}
			value := parsed.Query().Get("page")
			if value == "" {
				return 0, errors.New("GitLab next pagination link has no page")
			}
			return parseNextPage(value, current, "Link")
		}
	}
	return 0, nil
}

func hasNextRelation(attributes []string) bool {
	for _, attribute := range attributes {
		key, value, ok := strings.Cut(strings.TrimSpace(attribute), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "rel") {
			continue
		}
		for _, relation := range strings.Fields(strings.Trim(strings.TrimSpace(value), `"'`)) {
			if strings.EqualFold(relation, "next") {
				return true
			}
		}
	}
	return false
}

func parseNextPage(value string, current int, header string) (int, error) {
	page, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || page <= current {
		return 0, fmt.Errorf("invalid GitLab pagination %s value %q", header, value)
	}
	return page, nil
}

func (c *Client) bytes(ctx context.Context, p string, q url.Values) ([]byte, error) {
	resp, e := c.request(ctx, http.MethodGet, p, q, nil)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxRawFileBytes {
		return nil, fmt.Errorf("%w: Content-Length %d exceeds %d bytes", errFileTooLarge, resp.ContentLength, maxRawFileBytes)
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, maxRawFileBytes+1))
	if int64(len(raw)) > maxRawFileBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", errFileTooLarge, maxRawFileBytes)
	}
	if e != nil {
		return nil, e
	}
	return raw, nil
}

const maxRawFileBytes int64 = 10 << 20

var errFileTooLarge = errors.New("gitlab file exceeds download limit")

package jira

import (
	"context"
	"encoding/base64"
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
	BaseURL, AuthType, Token, Username, Password, ProxyURL, CACertificate string
	Timeout                                                               time.Duration
	TLSVerify                                                             *bool
	AllowedPrincipals                                                     []string
}
type Client struct {
	base, authType, token, username, password string
	http                                      *http.Client
	principals                                []string
}

func New(cfg Config) (*Client, error) {
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("valid Jira base URL is required")
	}
	if len(cfg.AllowedPrincipals) == 0 {
		return nil, errors.New("Jira allowedPrincipals is required for fail-closed ACL")
	}
	authType := strings.ToLower(strings.TrimSpace(cfg.AuthType))
	if authType == "" || authType == "auto" {
		if cfg.Token != "" {
			authType = "bearer"
		} else {
			authType = "basic"
		}
	}
	if authType != "bearer" && authType != "basic" {
		return nil, errors.New("Jira authType must be bearer or basic")
	}
	if authType == "bearer" && strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("Jira token is required for bearer authentication")
	}
	if authType == "basic" && (strings.TrimSpace(cfg.Username) == "" || cfg.Password == "") {
		return nil, errors.New("Jira username and password are required for basic authentication")
	}
	httpClient, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	return &Client{base: strings.TrimRight(parsed.String(), "/"), authType: authType, token: cfg.Token, username: cfg.Username, password: cfg.Password, http: httpClient, principals: cfg.AllowedPrincipals}, nil
}
func (*Client) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityDiscovery, source.CapabilityContent, source.CapabilityACL, source.CapabilityQuery}
}

type project struct {
	ID, Key, Name string
}

func (c *Client) ListProjects(ctx context.Context) ([]source.Project, error) {
	var projects []project
	if err := c.get(ctx, "/rest/api/2/project", nil, &projects); err != nil {
		return nil, err
	}
	out := make([]source.Project, len(projects))
	for i, item := range projects {
		out[i] = source.Project{Key: item.Key, Name: item.Name}
	}
	return out, nil
}
func (c *Client) ListRepositories(ctx context.Context, projectKey string) ([]source.Repository, error) {
	var item project
	if err := c.get(ctx, "/rest/api/2/project/"+url.PathEscape(projectKey), nil, &item); err != nil {
		return nil, err
	}
	return []source.Repository{{ID: stableID(item.ID + ":" + item.Key), ProjectKey: "jira", Slug: strings.ToLower(item.Key), Name: item.Name, Description: "Jira issues and operational knowledge", DefaultBranch: "current"}}, nil
}
func (c *Client) ListBranches(ctx context.Context, repo source.RepositoryRef) ([]source.Reference, error) {
	items, err := c.issues(ctx, repo.Slug, "", 1)
	if err != nil {
		return nil, err
	}
	latest := "empty"
	if len(items) > 0 && items[0].Fields.Updated != "" {
		latest = items[0].Fields.Updated + ":" + items[0].ID
	}
	return []source.Reference{{ID: "current", Name: "current", LatestCommit: latest, Default: true}}, nil
}
func (*Client) ListTags(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, nil
}
func (*Client) GetCommit(_ context.Context, _ source.RepositoryRef, id string) (source.Commit, error) {
	return source.Commit{ID: id, DisplayID: id, Message: "Jira current issues"}, nil
}

type issue struct {
	ID, Key string
	Fields  struct {
		Summary     string
		Description any
		Updated     string
		Comment     struct {
			Comments []struct {
				Body any
			} `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
}
type searchPayload struct {
	Issues []issue `json:"issues"`
	Total  int     `json:"total"`
}

func (c *Client) issues(ctx context.Context, projectKey, text string, limit int) ([]issue, error) {
	jql := "project=" + projectKey + " ORDER BY updated DESC"
	if text != "" {
		jql = fmt.Sprintf(`project=%s AND text~"%s" ORDER BY updated DESC`, projectKey, strings.ReplaceAll(text, `"`, `\"`))
	}
	var payload searchPayload
	err := c.get(ctx, "/rest/api/2/search", url.Values{"jql": {jql}, "maxResults": {strconv.Itoa(limit)}, "fields": {"summary,description,comment,updated"}}, &payload)
	return payload.Issues, err
}
func (c *Client) ListFiles(ctx context.Context, repo source.RepositoryRef, _ string) ([]source.File, error) {
	items, err := c.issues(ctx, repo.Slug, "", 1000)
	if err != nil {
		return nil, err
	}
	out := make([]source.File, len(items))
	for i, item := range items {
		out[i] = source.File{Path: "issues/" + item.Key + ".md"}
	}
	return out, nil
}
func (c *Client) GetFile(ctx context.Context, _ source.RepositoryRef, _ string, path string) ([]byte, error) {
	key := strings.TrimSuffix(strings.TrimPrefix(path, "issues/"), ".md")
	var item issue
	if err := c.get(ctx, "/rest/api/2/issue/"+url.PathEscape(key), url.Values{"fields": {"summary,description,comment,updated"}}, &item); err != nil {
		return nil, err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# %s · %s\n\n%s\n", item.Key, item.Fields.Summary, renderText(item.Fields.Description))
	if len(item.Fields.Comment.Comments) > 0 {
		body.WriteString("\n## Comments\n")
		for _, comment := range item.Fields.Comment.Comments {
			body.WriteString("\n" + renderText(comment.Body) + "\n")
		}
	}
	return []byte(body.String()), nil
}
func (c *Client) GetPermissions(context.Context, source.RepositoryRef) ([]source.Permission, error) {
	out := make([]source.Permission, 0, len(c.principals))
	for _, principal := range c.principals {
		kind := "user"
		if principal == "*" {
			kind = "all"
		} else if strings.HasPrefix(principal, "group:") {
			kind = "group"
		}
		out = append(out, source.Permission{Principal: principal, Kind: kind, Permission: "read"})
	}
	return out, nil
}
func (*Client) RegisterWebhook(context.Context, source.RepositoryRef, string, string) error {
	return errors.New("Jira webhook registration is not supported; use polling")
}
func (c *Client) SearchQuery(ctx context.Context, repo source.RepositoryRef, _ string, query string, limit int) ([]source.QueryResult, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	items, err := c.issues(ctx, repo.Slug, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]source.QueryResult, len(items))
	for i, item := range items {
		out[i] = source.QueryResult{Path: "issues/" + item.Key + ".md", Snippet: item.Fields.Summary + "\n" + renderText(item.Fields.Description), LineStart: 1, LineEnd: 1}
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, target any) error {
	endpoint, _ := url.Parse(c.base + path)
	endpoint.RawQuery = query.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if c.authType == "bearer" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.password)))
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Jira HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(target); err != nil {
		return netclient.DecodeFailure("Jira", "Jira REST API", endpointOf(req), err)
	}
	return nil
}
func renderText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		raw, _ := json.Marshal(typed)
		return string(raw)
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}
func stableID(value string) int64 {
	var result int64 = 17
	for _, r := range value {
		result = result*31 + int64(r)
	}
	if result < 0 {
		result = -result
	}
	return result
}

// endpointOf names the path a failed response came from, without the query
// string: a token or search term in a diagnostic is a leak, not a detail.
func endpointOf(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "the requested endpoint"
	}
	return req.URL.EscapedPath()
}

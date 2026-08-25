package confluence

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
		return nil, errors.New("valid Confluence base URL is required")
	}
	if len(cfg.AllowedPrincipals) == 0 {
		return nil, errors.New("Confluence allowedPrincipals is required for fail-closed ACL")
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
		return nil, errors.New("Confluence authType must be bearer or basic")
	}
	if authType == "bearer" && strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("Confluence token is required for bearer authentication")
	}
	if authType == "basic" && (strings.TrimSpace(cfg.Username) == "" || cfg.Password == "") {
		return nil, errors.New("Confluence username and password are required for basic authentication")
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

type space struct {
	ID, Key, Name, Description string
}

func (c *Client) ListProjects(ctx context.Context) ([]source.Project, error) {
	var payload struct {
		Results []space `json:"results"`
	}
	if err := c.get(ctx, "/rest/api/space", url.Values{"limit": {"200"}}, &payload); err != nil {
		return nil, err
	}
	out := make([]source.Project, len(payload.Results))
	for i, item := range payload.Results {
		out[i] = source.Project{Key: item.Key, Name: item.Name, Description: item.Description}
	}
	return out, nil
}

func (c *Client) ListRepositories(ctx context.Context, projectKey string) ([]source.Repository, error) {
	var payload struct {
		ID, Key, Name, Description string
	}
	if err := c.get(ctx, "/rest/api/space/"+url.PathEscape(projectKey), nil, &payload); err != nil {
		return nil, err
	}
	return []source.Repository{{ID: stableID(payload.ID + ":" + payload.Key), ProjectKey: "confluence", Slug: strings.ToLower(payload.Key), Name: payload.Name, Description: payload.Description, DefaultBranch: "current"}}, nil
}

func (c *Client) ListBranches(ctx context.Context, repo source.RepositoryRef) ([]source.Reference, error) {
	var payload struct {
		Results []struct {
			ID      string
			Version struct {
				Number int
				When   string
			} `json:"version"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/rest/api/content", url.Values{"type": {"page"}, "spaceKey": {repo.Slug}, "expand": {"version"}, "limit": {"1000"}}, &payload); err != nil {
		return nil, err
	}
	latest := "empty"
	for _, item := range payload.Results {
		candidate := item.Version.When + ":" + strconv.Itoa(item.Version.Number) + ":" + item.ID
		if candidate > latest {
			latest = candidate
		}
	}
	return []source.Reference{{ID: "current", Name: "current", LatestCommit: latest, Default: true}}, nil
}
func (*Client) ListTags(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, nil
}
func (*Client) GetCommit(_ context.Context, _ source.RepositoryRef, id string) (source.Commit, error) {
	return source.Commit{ID: id, DisplayID: id, Message: "Confluence current content"}, nil
}

func (c *Client) ListFiles(ctx context.Context, repo source.RepositoryRef, _ string) ([]source.File, error) {
	var payload struct {
		Results []struct {
			ID, Title string
		} `json:"results"`
	}
	if err := c.get(ctx, "/rest/api/content", url.Values{"type": {"page"}, "spaceKey": {repo.Slug}, "limit": {"1000"}}, &payload); err != nil {
		return nil, err
	}
	out := make([]source.File, 0, len(payload.Results))
	for _, item := range payload.Results {
		out = append(out, source.File{Path: "pages/" + item.ID + "-" + slug(item.Title) + ".md"})
	}
	return out, nil
}

func (c *Client) GetFile(ctx context.Context, _ source.RepositoryRef, _ string, path string) ([]byte, error) {
	id := strings.SplitN(strings.TrimPrefix(path, "pages/"), "-", 2)[0]
	if id == "" {
		return nil, errors.New("invalid Confluence page path")
	}
	var payload struct {
		ID, Title string
		Body      struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	if err := c.get(ctx, "/rest/api/content/"+url.PathEscape(id), url.Values{"expand": {"body.storage,version"}}, &payload); err != nil {
		return nil, err
	}
	return []byte("# " + payload.Title + "\n\n" + htmlToText(payload.Body.Storage.Value)), nil
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
	return errors.New("Confluence webhook registration is not supported; use polling")
}

func (c *Client) SearchQuery(ctx context.Context, repo source.RepositoryRef, _ string, query string, limit int) ([]source.QueryResult, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	cql := fmt.Sprintf(`space="%s" AND type=page AND text~"%s"`, strings.ReplaceAll(repo.Slug, `"`, `\"`), strings.ReplaceAll(query, `"`, `\"`))
	var payload struct {
		Results []struct {
			Content struct{ ID, Title string } `json:"content"`
			Excerpt string                     `json:"excerpt"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/rest/api/search", url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}}, &payload); err != nil {
		return nil, err
	}
	out := make([]source.QueryResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		out = append(out, source.QueryResult{Path: "pages/" + item.Content.ID + "-" + slug(item.Content.Title) + ".md", Snippet: htmlToText(item.Excerpt), LineStart: 1, LineEnd: 1})
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
		return fmt.Errorf("Confluence HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(target); err != nil {
		return netclient.DecodeFailure("Confluence", "Confluence REST API", endpointOf(req), err)
	}
	return nil
}

var tagRE = regexp.MustCompile(`<[^>]+>`)

func htmlToText(value string) string {
	value = strings.ReplaceAll(value, "</p>", "\n\n")
	value = strings.ReplaceAll(value, "<br>", "\n")
	value = strings.ReplaceAll(value, "<br/>", "\n")
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(value, "")))
}
func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = regexp.MustCompile(`[^a-z0-9가-힣]+`).ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
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

package source

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
)

type Capability string

const (
	CapabilityDiscovery  Capability = "discovery"
	CapabilityContent    Capability = "content"
	CapabilityACL        Capability = "acl"
	CapabilityChangeFeed Capability = "change-feed"
	CapabilityQuery      Capability = "query"
	CapabilityWebhook    Capability = "webhook"
)

type CapabilityProvider interface {
	Capabilities() []Capability
}

// Discovery, ContentReader and ACLProvider let non-Git sources implement only
// the capabilities they actually provide. RepositorySource remains the
// compatibility facade consumed by the existing index worker.
type Discovery interface {
	ListProjects(context.Context) ([]Project, error)
	ListRepositories(context.Context, string) ([]Repository, error)
}

type ContentReader interface {
	ListFiles(context.Context, RepositoryRef, string) ([]File, error)
	GetFile(context.Context, RepositoryRef, string, string) ([]byte, error)
}

type ACLProvider interface {
	GetPermissions(context.Context, RepositoryRef) ([]Permission, error)
}

type Project struct{ Key, Name, Description string }
type Repository struct {
	ID                                                 int64
	ProjectKey, Slug, Name, Description, DefaultBranch string
	Archived                                           bool
}
type RepositoryRef struct{ ProjectKey, Slug string }
type Reference struct {
	ID, Name, LatestCommit string
	Default                bool
}
type Commit struct {
	ID, DisplayID, Message, Author string
	AuthorEmail                    string
	AuthoredAt                     time.Time
	URL                            string
}

// ChangeRequest is a GitLab merge request or a Bitbucket pull request. The
// description and review trail carry the rationale that commits omit.
type ChangeRequest struct {
	ID                           string
	Number                       int64
	Title, Description, State    string
	Author, SourceRef, TargetRef string
	URL                          string
	CreatedAt, UpdatedAt         time.Time
}

// ChangeRequestSearcher searches merge or pull requests of one repository.
type ChangeRequestSearcher interface {
	SearchChangeRequests(ctx context.Context, repo RepositoryRef, query, state string, limit int) ([]ChangeRequest, error)
}

// HistoryProvider returns the commits that touched a path. "Why is this code
// like this" is a normal debugging question and needs history, not content.
type HistoryProvider interface {
	ListCommits(ctx context.Context, repo RepositoryRef, refName, path string, limit int) ([]Commit, error)
}
type File struct {
	Path string
	Size int64
}
type Change struct {
	Path    string
	OldPath string
	Type    string
}
type Permission struct{ Principal, Kind, Permission string }
type QueryResult struct {
	Path      string
	Snippet   string
	CommitID  string
	LineStart int
	LineEnd   int
}
type QuerySearcher interface {
	SearchQuery(context.Context, RepositoryRef, string, string, int) ([]QueryResult, error)
}

// GlobalQueryResult carries a code hit found by an instance wide search API
// together with the repository it belongs to, so the caller can apply the
// repository ACL before the snippet is shown to a user.
type GlobalQueryResult struct {
	ProjectKey, Slug, Name, Description, Ref, DefaultBranch string
	ID                                                      int64
	QueryResult
}

// GlobalQuerySearcher searches source code across every repository the service
// account can read. Bitbucket Server exposes this through the code search API
// and GitLab through advanced search, so implementations must return
// ErrGlobalSearchUnsupported when the remote instance has the feature disabled.
type GlobalQuerySearcher interface {
	SearchGlobalQuery(context.Context, string, int) ([]GlobalQueryResult, error)
}

// RepositorySearcher performs a server side repository name search instead of
// enumerating every project, which keeps discovery usable on instances with
// thousands of repositories.
type RepositorySearcher interface {
	SearchRepositories(context.Context, string, int) ([]Repository, error)
}

// ErrGlobalSearchUnsupported reports that the remote instance cannot run an
// instance wide code search, so the caller should fall back to per repository
// queries instead of surfacing an error.
var ErrGlobalSearchUnsupported = errors.New("global source code search is not available on this instance")

// LibraryID returns the canonical source-aware library ID used by the catalog.
// Bitbucket keeps /project/repository while other sources receive a namespace
// so repositories with the same name cannot collide.
func LibraryID(sourceType, projectKey, slug string) string {
	project := normalizeIDPart(projectKey)
	if sourceType != "" && sourceType != "bitbucket" {
		project = normalizeIDPart(sourceType) + "~" + project
	}
	return "/" + project + "/" + normalizeIDPart(slug)
}

func normalizeIDPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	dash := false
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '.' || char == '_' || char == '-' {
			result.WriteRune(char)
			dash = false
		} else if !dash {
			result.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

type ChangeFeed interface {
	Changes(context.Context, RepositoryRef, string, string) ([]Change, error)
}

type RepositorySource interface {
	ListProjects(context.Context) ([]Project, error)
	ListRepositories(context.Context, string) ([]Repository, error)
	ListBranches(context.Context, RepositoryRef) ([]Reference, error)
	ListTags(context.Context, RepositoryRef) ([]Reference, error)
	GetCommit(context.Context, RepositoryRef, string) (Commit, error)
	ListFiles(context.Context, RepositoryRef, string) ([]File, error)
	GetFile(context.Context, RepositoryRef, string, string) ([]byte, error)
	GetPermissions(context.Context, RepositoryRef) ([]Permission, error)
	RegisterWebhook(context.Context, RepositoryRef, string, string) error
}

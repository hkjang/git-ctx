package source

import "context"

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
type Commit struct{ ID, DisplayID, Message, Author string }
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

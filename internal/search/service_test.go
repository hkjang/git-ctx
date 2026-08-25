package search

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"git-ctx/internal/embedding"
	"git-ctx/internal/rerank"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

func TestHybridRankingAndGitLabSource(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('gitlab:1','core','gpu','GPU','gitlab','1','/core/gpu','main')`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','group:engineering','read')`)
	insert := func(id, heading, content, path string) {
		t.Helper()
		_, e := db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES(?,'gitlab:1','main','abc',?,1,5,?,'document',?,?,?)`, id, path, heading, content, id, embedding.Encode(embedding.Embed(heading+" "+content)))
		if e != nil {
			t.Fatal(e)
		}
	}
	insert("a", "GPU Metrics", "Kubernetes Pod GPU metrics use DCGM exporter.", "gpu.md")
	insert("c", "GPU Setup", "Configure GPU nodes and Pod resources.", "setup.md")
	insert("b", "Database", "PostgreSQL transaction and backup guide.", "db.md")
	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 1, CandidateLimit: 100}
	})
	result, err := service.Query(ctx, []string{"alice"}, "/core/gpu/main", "Pod GPU metrics")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "gitlab://core/gpu@abc/gpu.md#L1-L5") {
		t.Fatalf("source missing: %s", result)
	}
	if strings.Contains(result, "db.md") {
		t.Fatalf("irrelevant result included: %s", result)
	}
	if strings.Count(result, "Source:") != 1 {
		t.Fatalf("finalK not applied: %s", result)
	}
	if _, err := New(db).Query(ctx, []string{"unknown", "group:engineering"}, "/core/gpu/main", "Pod GPU"); err != nil {
		t.Fatalf("group ACL failed: %v", err)
	}
}

// querySource is shared by the tests below. SearchSource queries candidate
// repositories concurrently, so the double guards its counters.
type querySource struct {
	mu        sync.Mutex
	calls     int
	lastQuery string
}

type errorQuerySource struct {
	querySource
	err error
}

func (q *errorQuerySource) SearchQuery(context.Context, source.RepositoryRef, string, string, int) ([]source.QueryResult, error) {
	q.mu.Lock()
	q.calls++
	q.mu.Unlock()
	return nil, q.err
}

type discoveryQuerySource struct {
	querySource
	allowed bool
}

func (q *discoveryQuerySource) ListProjects(context.Context) ([]source.Project, error) {
	return []source.Project{{Key: "apps", Name: "Applications"}}, nil
}
func (q *discoveryQuerySource) ListRepositories(context.Context, string) ([]source.Repository, error) {
	return []source.Repository{{ID: 77, ProjectKey: "apps", Slug: "dify", Name: "Dify", Description: "AI application platform", DefaultBranch: "main"}}, nil
}
func (q *discoveryQuerySource) GetPermissions(context.Context, source.RepositoryRef) ([]source.Permission, error) {
	if !q.allowed {
		return []source.Permission{{Principal: "bob", Kind: "user", Permission: "read"}}, nil
	}
	return []source.Permission{{Principal: "alice", Kind: "user", Permission: "read"}}, nil
}

type boundedDiscoverySource struct {
	querySource
	projects        []source.Project
	repositories    map[string][]source.Repository
	permissionCalls int
}

func (q *boundedDiscoverySource) ListProjects(context.Context) ([]source.Project, error) {
	return q.projects, nil
}
func (q *boundedDiscoverySource) ListRepositories(_ context.Context, project string) ([]source.Repository, error) {
	return q.repositories[project], nil
}
func (q *boundedDiscoverySource) GetPermissions(context.Context, source.RepositoryRef) ([]source.Permission, error) {
	q.permissionCalls++
	return []source.Permission{{Principal: "alice", Kind: "user", Permission: "read"}}, nil
}

func (q *querySource) ListProjects(context.Context) ([]source.Project, error) { return nil, nil }
func (q *querySource) ListRepositories(context.Context, string) ([]source.Repository, error) {
	return nil, nil
}
func (q *querySource) ListBranches(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, nil
}
func (q *querySource) ListTags(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, nil
}
func (q *querySource) GetCommit(context.Context, source.RepositoryRef, string) (source.Commit, error) {
	return source.Commit{}, nil
}
func (q *querySource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	return nil, nil
}
func (q *querySource) GetFile(context.Context, source.RepositoryRef, string, string) ([]byte, error) {
	return nil, nil
}
func (q *querySource) GetPermissions(context.Context, source.RepositoryRef) ([]source.Permission, error) {
	return nil, nil
}
func (q *querySource) RegisterWebhook(context.Context, source.RepositoryRef, string, string) error {
	return nil
}
func (q *querySource) SearchQuery(_ context.Context, repo source.RepositoryRef, ref, query string, limit int) ([]source.QueryResult, error) {
	q.mu.Lock()
	q.calls++
	q.lastQuery = query
	q.mu.Unlock()
	return []source.QueryResult{{Path: "docs/source-api.md", Snippet: "source API result", CommitID: "remote-commit", LineStart: 4, LineEnd: 8}}, nil
}

func TestSourceQueryAPIUsedWithoutRemoteModelsAfterACL(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:source-query-search?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c','r','main','indexed-commit','docs/source-api.md',4,8,'Source API','sanitized','safe indexed source API result','hash')`)
	remote := &querySource{}
	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: 0, FinalK: 8, CandidateLimit: 100, SourceQuerySearch: true}
	})
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })
	result, err := service.Query(ctx, []string{"alice"}, "/core/demo/main", "GPU usage")
	if err != nil || !strings.Contains(result, "gitlab://core/demo@indexed-commit/docs/source-api.md#L4-L8") || !strings.Contains(result, "safe indexed source API result") || strings.Contains(result, "\n\nsource API result\n\n") || remote.calls != 1 {
		t.Fatalf("result=%s calls=%d err=%v", result, remote.calls, err)
	}
	if _, err = service.Query(ctx, []string{"mallory"}, "/core/demo/main", "GPU usage"); err == nil || remote.calls != 1 {
		t.Fatalf("ACL did not protect source API calls: calls=%d err=%v", remote.calls, err)
	}
}

func TestSourceQueryAPIFailureIsCalledOnceAndReturned(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:source-query-error?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	remote := &errorQuerySource{err: &source.APIError{Source: "gitlab", StatusCode: 401, Status: "401 Unauthorized", Body: "invalid token"}}
	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: 0, FinalK: 8, CandidateLimit: 100, SourceQuerySearch: true}
	})
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.Query(ctx, []string{"alice"}, "/core/demo/main", "GPU usage")
	if err == nil || source.StatusOf(err) != 401 {
		t.Fatalf("result=%q err=%v status=%d", result, err, source.StatusOf(err))
	}
	if remote.calls != 1 {
		t.Fatalf("one documentation query called the failing source %d times", remote.calls)
	}
}

func TestBitbucketDocumentationQueryRejectsNonDefaultRef(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:bitbucket-query-ref?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','bitbucket','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	remote := &querySource{}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.Query(ctx, []string{"alice"}, "/core/demo/release", "GPU usage")
	if err == nil || !errors.Is(err, source.ErrCodeSearchRefUnsupported) {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if remote.calls != 0 {
		t.Fatalf("Bitbucket non-default ref unexpectedly called the source %d times", remote.calls)
	}

	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
VALUES('release-doc','r','release','release-commit','docs/release.md',1,2,'GPU usage','document','GPU usage on release','release-hash')`)
	result, err = service.Query(ctx, []string{"alice"}, "/core/demo/release", "GPU usage")
	if err != nil || !strings.Contains(result, "GPU usage on release") || !strings.Contains(result, "only the default branch") {
		t.Fatalf("locally indexed non-default ref result=%q err=%v", result, err)
	}
	if remote.calls != 0 {
		t.Fatalf("local Bitbucket non-default ref unexpectedly called the source %d times", remote.calls)
	}
}

func TestRepositoryAndSourceSearchWithoutLibraryID(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:source-discovery?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','GPU Demo','Kubernetes GPU metrics','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c','r','main','indexed-commit','docs/source-api.md',4,8,'Source API','document','safe indexed source API result','hash')`)
	remote := &querySource{}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })
	repositories, err := service.SearchRepositories(ctx, []string{"alice"}, "GPU", "gitlab", 10)
	if err != nil || len(repositories) != 1 || repositories[0].LibraryID != "/core/demo" {
		t.Fatalf("repositories=%#v err=%v", repositories, err)
	}
	if hidden, err := service.SearchRepositories(ctx, []string{"mallory"}, "GPU", "", 10); err != nil || len(hidden) != 0 {
		t.Fatalf("ACL repository leak=%#v err=%v", hidden, err)
	}
	hits, err := service.SearchSource(ctx, []string{"alice"}, "GPU usage", "gitlab", "", "", "", 10)
	if err != nil || len(hits) != 1 || hits[0].LibraryID != "/core/demo" || hits[0].CommitID != "indexed-commit" || hits[0].Snippet != "safe indexed source API result" {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
	if hidden, err := service.SearchSource(ctx, []string{"mallory"}, "GPU usage", "", "", "", "", 10); err != nil || len(hidden) != 0 || remote.calls != 1 {
		t.Fatalf("ACL source leak=%#v calls=%d err=%v", hidden, remote.calls, err)
	}
}

func TestSearchCodeReturnsRepositoryAndSafeUnindexedRemoteResult(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:unindexed-source-search?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch) VALUES('r','apps','dify','Dify','AI application platform','gitlab','1','/apps/dify','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	remote := &querySource{}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.SearchCode(ctx, []string{"alice"}, "dify 소스 검색해", "gitlab", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Query != "dify" || remote.lastQuery != "dify" || len(result.Repositories) != 1 || len(result.Hits) != 1 {
		t.Fatalf("result=%#v remote=%#v", result, remote)
	}
	if result.Hits[0].Snippet != "source API result" || result.Hits[0].CommitID != "remote-commit" {
		t.Fatalf("unindexed remote hit was not preserved safely: %#v", result.Hits[0])
	}
	hidden, err := service.SearchCode(ctx, []string{"mallory"}, "dify 소스 검색해", "", "", "", "", 10)
	if err != nil || len(hidden.Repositories) != 0 || len(hidden.Hits) != 0 || remote.calls != 1 {
		t.Fatalf("ACL leak=%#v calls=%d err=%v", hidden, remote.calls, err)
	}
}

func TestSearchCodeDiscoversUnregisteredRemoteRepositoryAfterACL(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:remote-discovery?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := &discoveryQuerySource{allowed: true}
	service := New(db)
	service.SetSourceLoader(func(_ context.Context, sourceType string) (source.RepositorySource, error) {
		if sourceType != "gitlab" {
			return nil, errors.New("not configured")
		}
		return remote, nil
	})
	result, err := service.SearchCode(ctx, []string{"alice"}, "dify 소스 검색해", "gitlab", "", "", "", 10)
	if err != nil || len(result.Repositories) != 1 || result.Repositories[0].LibraryID != "/gitlab~apps/dify" || len(result.Hits) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Hits[0].LibraryID != "/gitlab~apps/dify" || remote.lastQuery != "dify" {
		t.Fatalf("remote hit=%#v query=%q", result.Hits[0], remote.lastQuery)
	}
	remote.calls = 0
	unmapped, err := service.SearchCode(ctx, nil, "dify 소스 검색해", "gitlab", "", "", "", 10)
	if err != nil || len(unmapped.Repositories) != 0 || len(unmapped.Hits) != 0 || remote.calls != 0 {
		t.Fatalf("unmapped identity leak=%#v calls=%d err=%v", unmapped, remote.calls, err)
	}
	remote.allowed = false
	remote.calls = 0
	hidden, err := service.SearchCode(ctx, []string{"alice"}, "dify 소스 검색해", "gitlab", "", "", "", 10)
	if err != nil || len(hidden.Repositories) != 0 || len(hidden.Hits) != 0 || remote.calls != 0 {
		t.Fatalf("remote ACL leak=%#v calls=%d err=%v", hidden, remote.calls, err)
	}
}

func TestRemoteDiscoveryHonorsLateExplicitProjectAndRepositoryCap(t *testing.T) {
	ctx := context.Background()
	remote := &boundedDiscoverySource{repositories: map[string][]source.Repository{}}
	for index := 0; index < maxDiscoveryProjects+5; index++ {
		key := fmt.Sprintf("P%02d", index)
		remote.projects = append(remote.projects, source.Project{Key: key})
	}
	target := remote.projects[len(remote.projects)-1].Key
	remote.repositories[target] = []source.Repository{{
		ID: 1, ProjectKey: target, Slug: "dify", Name: "Dify", DefaultBranch: "main",
	}}
	service := &Service{}
	scan := &remoteScan{
		service: service, adapter: remote, sourceType: "gitlab", principals: []string{"alice"},
		query: "dify", project: target, limit: 10,
		seenRepository: map[string]bool{}, seenHit: map[string]bool{}, allowed: map[string]bool{},
	}
	scan.discoverRepositories(ctx)
	if len(scan.candidates) != 1 || scan.candidates[0].ProjectKey != target {
		t.Fatalf("explicit project beyond the global scan cap was skipped: %#v", scan.candidates)
	}

	remote.projects = []source.Project{{Key: "BIG"}}
	remote.repositories = map[string][]source.Repository{"BIG": {}}
	for index := 0; index < maxDiscoveryRepositories+75; index++ {
		remote.repositories["BIG"] = append(remote.repositories["BIG"], source.Repository{
			ID: int64(index + 1), ProjectKey: "BIG", Slug: fmt.Sprintf("repo-%03d", index),
			Name: "Repo", DefaultBranch: "main",
		})
	}
	scan = &remoteScan{
		service: service, adapter: remote, sourceType: "gitlab",
		principals: WithUnrestricted(nil, []string{"source-admin"}), query: "repo", limit: 10,
		seenRepository: map[string]bool{}, seenHit: map[string]bool{}, allowed: map[string]bool{},
	}
	scan.discoverRepositories(ctx)
	if len(scan.candidates) != maxDiscoveryRepositories {
		t.Fatalf("repository discovery candidates=%d want cap=%d", len(scan.candidates), maxDiscoveryRepositories)
	}
}

// globalQuerySource models a GitLab instance with advanced search enabled: the
// term appears only inside a file, never in a repository name.
type globalQuerySource struct {
	discoveryQuerySource
	globalCalls int
	unsupported bool
}

func (q *globalQuerySource) SearchGlobalQuery(_ context.Context, query string, _ int) ([]source.GlobalQueryResult, error) {
	q.mu.Lock()
	q.globalCalls++
	q.lastQuery = query
	q.mu.Unlock()
	if q.unsupported {
		return nil, source.ErrGlobalSearchUnsupported
	}
	return []source.GlobalQueryResult{{
		ProjectKey: "apps", Slug: "dify", Name: "Dify", Ref: "main", DefaultBranch: "main", ID: 77,
		QueryResult: source.QueryResult{Path: "api/core/auth.py", Snippet: "def verify_token(", LineStart: 42, LineEnd: 42},
	}}, nil
}

type unsupportedGlobalSource struct {
	*scopedRemoteSource
}

func (q *unsupportedGlobalSource) SearchGlobalQuery(context.Context, string, int) ([]source.GlobalQueryResult, error) {
	q.mu.Lock()
	q.globalCalls++
	q.mu.Unlock()
	return nil, source.ErrGlobalSearchUnsupported
}

type scopedRemoteSource struct {
	querySource
	mu                 sync.Mutex
	projects           []source.Project
	repositories       map[string][]source.Repository
	nameResults        []source.Repository
	globalResults      []source.GlobalQueryResult
	queryResults       map[string][]source.QueryResult
	allowed            map[string]bool
	queryRepositories  []source.RepositoryRef
	queryRefs          []string
	globalCalls        int
	globalLimit        int
	permissionCalls    map[string]int
	repositorySearches []string
}

func (q *scopedRemoteSource) ListProjects(context.Context) ([]source.Project, error) {
	return q.projects, nil
}

func (q *scopedRemoteSource) ListRepositories(_ context.Context, project string) ([]source.Repository, error) {
	return q.repositories[project], nil
}

func (q *scopedRemoteSource) SearchRepositories(_ context.Context, query string, _ int) ([]source.Repository, error) {
	q.mu.Lock()
	q.repositorySearches = append(q.repositorySearches, query)
	q.mu.Unlock()
	return q.nameResults, nil
}

func (q *scopedRemoteSource) SearchGlobalQuery(_ context.Context, _ string, limit int) ([]source.GlobalQueryResult, error) {
	q.mu.Lock()
	q.globalCalls++
	q.globalLimit = limit
	q.mu.Unlock()
	if len(q.globalResults) > limit {
		return q.globalResults[:limit], nil
	}
	return q.globalResults, nil
}

func (q *scopedRemoteSource) SearchQuery(_ context.Context, repository source.RepositoryRef, ref, _ string, _ int) ([]source.QueryResult, error) {
	q.mu.Lock()
	q.calls++
	q.queryRepositories = append(q.queryRepositories, repository)
	q.queryRefs = append(q.queryRefs, ref)
	q.mu.Unlock()
	return q.queryResults[strings.ToLower(repository.ProjectKey+"/"+repository.Slug)], nil
}

func (q *scopedRemoteSource) GetPermissions(_ context.Context, repository source.RepositoryRef) ([]source.Permission, error) {
	key := strings.ToLower(repository.ProjectKey + "/" + repository.Slug)
	q.mu.Lock()
	if q.permissionCalls == nil {
		q.permissionCalls = map[string]int{}
	}
	q.permissionCalls[key]++
	allowed := q.allowed[key]
	q.mu.Unlock()
	if allowed {
		return []source.Permission{{Principal: "alice", Kind: "user", Permission: "read"}}, nil
	}
	return []source.Permission{{Principal: "bob", Kind: "user", Permission: "read"}}, nil
}

func newScopedRemote(project, slug, defaultBranch string) *scopedRemoteSource {
	key := strings.ToLower(project + "/" + slug)
	return &scopedRemoteSource{
		projects: []source.Project{{Key: project}},
		repositories: map[string][]source.Repository{
			project: {{ID: 77, ProjectKey: project, Slug: slug, Name: "Application", DefaultBranch: defaultBranch}},
		},
		queryResults: map[string][]source.QueryResult{
			key: {{Path: "api/auth.go", Snippet: "func verifyToken()", LineStart: 12, LineEnd: 12}},
		},
		allowed: map[string]bool{key: true},
	}
}

// Searching for a code identifier must return file hits even though no
// repository name, slug or description contains the term. Gating remote search
// on repository metadata is what made GitLab code search look empty.
func TestSearchCodeFindsContentThatNoRepositoryNameMatches(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:global-code-search?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := &globalQuerySource{discoveryQuerySource: discoveryQuerySource{allowed: true}}
	service := New(db)
	service.SetSourceLoader(func(_ context.Context, sourceType string) (source.RepositorySource, error) {
		if sourceType != "gitlab" {
			return nil, errors.New("not configured")
		}
		return remote, nil
	})
	result, err := service.SearchCode(ctx, []string{"alice"}, "verify_token 코드 검색해", "gitlab", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Path != "api/core/auth.py" || result.Hits[0].LibraryID != "/gitlab~apps/dify" {
		t.Fatalf("global code hit missing: %#v diagnostics=%v", result.Hits, result.Diagnostics)
	}
	if result.Hits[0].Ref != "main" || result.Hits[0].CommitID != "main" {
		t.Fatalf("ref was not propagated: %#v", result.Hits[0])
	}
	if len(result.Diagnostics) == 0 {
		t.Fatal("diagnostics must explain which search path ran")
	}

	// Instance-wide search cannot constrain results to an explicit ref. GitLab
	// must use its repository-scoped endpoint instead of returning default-branch
	// hits labelled as the requested branch.
	remote.globalCalls = 0
	remote.calls = 0
	explicitRef, err := service.SearchCode(ctx, []string{"alice"}, "dify", "gitlab", "", "", "release", 10)
	if err != nil || len(explicitRef.Hits) == 0 || explicitRef.Hits[0].Ref != "release" {
		t.Fatalf("explicit-ref result=%#v err=%v", explicitRef, err)
	}
	if remote.globalCalls != 0 || remote.calls == 0 {
		t.Fatalf("explicit ref used wrong API path: global=%d repository=%d", remote.globalCalls, remote.calls)
	}

	// A user without access to the project sees nothing, and the diagnostics must
	// not leak the repository name.
	remote.allowed = false
	hidden, err := service.SearchCode(ctx, []string{"mallory"}, "verify_token", "gitlab", "", "", "", 10)
	if err != nil || len(hidden.Hits) != 0 || len(hidden.Repositories) != 0 {
		t.Fatalf("ACL leak: %#v err=%v", hidden, err)
	}

	// Without advanced search the client falls back to per repository queries.
	remote.allowed = true
	remote.unsupported = true
	remote.calls = 0
	fallback, err := service.SearchCode(ctx, []string{"alice"}, "dify", "gitlab", "", "", "", 10)
	if err != nil || len(fallback.Hits) == 0 || remote.calls == 0 {
		t.Fatalf("fallback search did not run: %#v calls=%d err=%v", fallback, remote.calls, err)
	}
}

func TestUnsupportedGlobalSearchEnumeratesForCodeOnlyQuery(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:unsupported-global-code-only?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := &unsupportedGlobalSource{scopedRemoteSource: newScopedRemote("apps", "dify", "main")}
	// The repository metadata deliberately does not contain verify_token, so
	// repository-name search returns nothing and only project enumeration can
	// feed the repository-scoped GitLab/Bitbucket query API.
	remote.nameResults = nil
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.SearchCode(ctx, []string{"alice"}, "verify_token", "gitlab", "", "", "", 10)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].RepositorySlug != "dify" {
		t.Fatalf("unsupported-global fallback=%#v err=%v", result, err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.globalCalls != 1 || remote.calls != 1 {
		t.Fatalf("global=%d repository=%d", remote.globalCalls, remote.calls)
	}
	if !strings.Contains(strings.Join(result.Diagnostics, " "), "unavailable") {
		t.Fatalf("fallback path was not diagnosed: %v", result.Diagnostics)
	}
}

func TestSearchCodeSearchesExplicitUnregisteredRepository(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:explicit-unregistered-repository?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := newScopedRemote("apps", "dify", "main")
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.SearchCode(ctx, []string{"alice"}, "verify_token", "gitlab", "apps", "dify", "release", 10)
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("explicit remote repository result=%#v err=%v", result, err)
	}
	if result.Hits[0].ProjectKey != "apps" || result.Hits[0].RepositorySlug != "dify" || result.Hits[0].Ref != "release" {
		t.Fatalf("explicit scope was not preserved: %#v", result.Hits[0])
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.calls != 1 || remote.globalCalls != 0 || len(remote.queryRepositories) != 1 ||
		remote.queryRepositories[0] != (source.RepositoryRef{ProjectKey: "apps", Slug: "dify"}) ||
		remote.queryRefs[0] != "release" {
		t.Fatalf("targeted calls=%d global=%d repositories=%#v refs=%#v", remote.calls, remote.globalCalls, remote.queryRepositories, remote.queryRefs)
	}
}

func TestExplicitGitLabRefDoesNotDependOnRepositoryNameMatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:explicit-ref-code-only?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := newScopedRemote("apps", "dify", "main")
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	// "verify_token" appears only in source. The repository metadata is the
	// deliberately unrelated "Application".
	result, err := service.SearchCode(ctx, []string{"alice"}, "verify_token", "gitlab", "apps", "", "release", 10)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Ref != "release" {
		t.Fatalf("code-only explicit-ref result=%#v err=%v", result, err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.globalCalls != 0 || remote.calls != 1 || len(remote.queryRefs) != 1 || remote.queryRefs[0] != "release" {
		t.Fatalf("explicit-ref path global=%d repository=%d refs=%#v", remote.globalCalls, remote.calls, remote.queryRefs)
	}
}

func TestGlobalFilteredToZeroFallsBackWithoutOutOfScopeACL(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:global-filtered-fallback?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := newScopedRemote("apps", "target", "main")
	remote.globalResults = []source.GlobalQueryResult{{
		ProjectKey: "other", Slug: "secret", DefaultBranch: "main",
		QueryResult: source.QueryResult{Path: "secret.go", Snippet: "verify_token", LineStart: 1, LineEnd: 1},
	}}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.SearchCode(ctx, []string{"alice"}, "verify_token", "gitlab", "apps", "", "", 10)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].RepositorySlug != "target" {
		t.Fatalf("filtered global fallback=%#v err=%v", result, err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.globalCalls != 1 || remote.calls != 1 {
		t.Fatalf("global=%d repository=%d", remote.globalCalls, remote.calls)
	}
	if remote.permissionCalls["other/secret"] != 0 {
		t.Fatalf("out-of-scope repository received %d ACL calls", remote.permissionCalls["other/secret"])
	}
}

func TestGlobalSearchOverscansBeforeACLFiltering(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:global-overscan?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := &scopedRemoteSource{
		repositories: map[string][]source.Repository{},
		queryResults: map[string][]source.QueryResult{},
		allowed:      map[string]bool{"public/visible": true},
	}
	for index := 0; index < 9; index++ {
		remote.globalResults = append(remote.globalResults, source.GlobalQueryResult{
			ProjectKey: "private", Slug: fmt.Sprintf("denied-%d", index), DefaultBranch: "main",
			QueryResult: source.QueryResult{Path: fmt.Sprintf("denied-%d.go", index), Snippet: "verify_token", LineStart: 1, LineEnd: 1},
		})
	}
	remote.globalResults = append(remote.globalResults, source.GlobalQueryResult{
		ProjectKey: "public", Slug: "visible", DefaultBranch: "main",
		QueryResult: source.QueryResult{Path: "visible.go", Snippet: "verify_token", LineStart: 3, LineEnd: 3},
	})
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.SearchCode(ctx, []string{"alice"}, "verify_token", "gitlab", "", "", "", 2)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].RepositorySlug != "visible" {
		t.Fatalf("overscanned result=%#v err=%v", result, err)
	}
	remote.mu.Lock()
	rawLimit := remote.globalLimit
	remote.mu.Unlock()
	if rawLimit != 10 {
		t.Fatalf("global raw limit=%d want bounded overscan 10", rawLimit)
	}
	if !strings.Contains(strings.Join(result.Diagnostics, " "), "scan cap") {
		t.Fatalf("global cap was not diagnosed: %v", result.Diagnostics)
	}
}

func TestUnregisteredBitbucketNonDefaultRefExplainsLimitation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:unregistered-bitbucket-ref?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := newScopedRemote("CORE", "demo", "main")
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	result, err := service.SearchCode(ctx, []string{"alice"}, "verify_token", "bitbucket", "CORE", "demo", "release", 10)
	if err != nil || len(result.Hits) != 0 || !strings.Contains(result.Warning, "default branch") {
		t.Fatalf("Bitbucket non-default result=%#v err=%v", result, err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.calls != 0 || remote.globalCalls != 0 {
		t.Fatalf("Bitbucket non-default ref called code APIs: repository=%d global=%d", remote.calls, remote.globalCalls)
	}
}

func TestBitbucketNonDefaultRefDoesNotReturnDefaultBranchHits(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:bitbucket-ref-search?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch)
VALUES('bitbucket:1','CORE','demo','Demo','bitbucket','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('bitbucket:1','alice','read')`)
	remote := &querySource{}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	hits, err := service.SearchSource(ctx, []string{"alice"}, "verify_token", "bitbucket", "CORE", "demo", "release", 10)
	if !errors.Is(err, source.ErrCodeSearchRefUnsupported) || len(hits) != 0 {
		t.Fatalf("non-default Bitbucket ref hits=%#v err=%v", hits, err)
	}
	if remote.calls != 0 {
		t.Fatalf("default-branch-only API was called for a non-default ref: %d", remote.calls)
	}
}

// slowACLSource reports how many permission lookups overlapped, so the scan can
// be shown to verify repositories concurrently instead of one at a time.
type slowACLSource struct {
	querySource
	repositories []source.Repository
	mu           sync.Mutex
	active, peak int
}

func (q *slowACLSource) ListProjects(context.Context) ([]source.Project, error) {
	return []source.Project{{Key: "apps", Name: "Applications"}}, nil
}
func (q *slowACLSource) ListRepositories(context.Context, string) ([]source.Repository, error) {
	return q.repositories, nil
}
func (q *slowACLSource) GetPermissions(context.Context, source.RepositoryRef) ([]source.Permission, error) {
	q.mu.Lock()
	q.active++
	if q.active > q.peak {
		q.peak = q.active
	}
	q.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	q.mu.Lock()
	q.active--
	q.mu.Unlock()
	return []source.Permission{{Principal: "alice", Kind: "user", Permission: "read"}}, nil
}

func TestRemoteDiscoveryVerifiesRepositoryACLsConcurrently(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:concurrent-acl?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	remote := &slowACLSource{}
	for index := 0; index < 12; index++ {
		remote.repositories = append(remote.repositories, source.Repository{
			ID: int64(index), ProjectKey: "apps", Slug: fmt.Sprintf("dify-%d", index),
			Name: fmt.Sprintf("Dify %d", index), DefaultBranch: "main",
		})
	}
	service := New(db)
	service.SetSourceLoader(func(_ context.Context, sourceType string) (source.RepositorySource, error) {
		if sourceType != "gitlab" {
			return nil, errors.New("not configured")
		}
		return remote, nil
	})
	started := time.Now()
	result, err := service.SearchCode(ctx, []string{"alice"}, "dify", "gitlab", "", "", "", 20)
	if err != nil || len(result.Repositories) == 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	remote.mu.Lock()
	peak := remote.peak
	remote.mu.Unlock()
	if peak < 2 {
		t.Fatalf("ACL lookups ran serially: peak concurrency %d", peak)
	}
	if elapsed := time.Since(started); elapsed > 12*20*time.Millisecond {
		t.Fatalf("concurrent ACL verification was not faster than a serial scan: %v", elapsed)
	}
}

// Platform, source and search administrators operate the catalog, so their
// searches must cover every registered and remote repository even when no
// Bitbucket or GitLab account is mapped to them.
func TestAdministratorRolesSearchWithoutRepositoryACL(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:admin-acl-bypass?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	// The repository is readable only by bob, and one row has no permission at all.
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch) VALUES('r','core','dify','Dify','AI platform','gitlab','1','/core/dify','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','bob','read')`)
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch) VALUES('r2','core','orphan','Dify Orphan','no permission rows','gitlab','2','/core/orphan','main')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c','r','main','abc','docs/dify.md',1,4,'Dify','document','dify platform guide','h')`)

	for _, role := range []string{"platform-admin", "source-admin", "search-admin"} {
		principals := WithUnrestricted(nil, []string{role})
		if !Unrestricted(principals) {
			t.Fatalf("%s did not receive catalog-wide search", role)
		}
		repositories, repoErr := New(db).SearchRepositories(ctx, principals, "dify", "", 10)
		if repoErr != nil || len(repositories) != 2 {
			t.Fatalf("%s repositories=%#v err=%v", role, repositories, repoErr)
		}
		libraries, resolveErr := New(db).Resolve(ctx, principals, "dify", "platform guide")
		if resolveErr != nil || len(libraries) == 0 {
			t.Fatalf("%s resolve=%#v err=%v", role, libraries, resolveErr)
		}
		text, queryErr := New(db).Query(ctx, principals, "/core/dify/main", "dify platform")
		if queryErr != nil || !strings.Contains(text, "docs/dify.md") {
			t.Fatalf("%s query=%q err=%v", role, text, queryErr)
		}
	}

	// A developer without a matching principal still sees nothing.
	developer := WithUnrestricted([]string{"alice"}, []string{"developer"})
	if Unrestricted(developer) {
		t.Fatal("developer must not receive catalog-wide search")
	}
	if repositories, err := New(db).SearchRepositories(ctx, developer, "dify", "", 10); err != nil || len(repositories) != 0 {
		t.Fatalf("developer leak=%#v err=%v", repositories, err)
	}

	// Remote discovery must skip the permission API entirely for administrators.
	remote := &discoveryQuerySource{allowed: false}
	service := New(db)
	service.SetSourceLoader(func(_ context.Context, sourceType string) (source.RepositorySource, error) {
		if sourceType != "gitlab" {
			return nil, errors.New("not configured")
		}
		return remote, nil
	})
	result, err := service.SearchCode(ctx, WithUnrestricted(nil, []string{"source-admin"}), "dify", "gitlab", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) == 0 {
		t.Fatalf("administrator remote discovery returned nothing: %#v", result)
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic, "bypassed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the ACL bypass must be stated in the diagnostics: %v", result.Diagnostics)
	}
}

// treeSource returns a full file listing the way a source server does for a
// repository that has been registered but not indexed yet.
type treeSource struct {
	querySource
	files []string
	calls int
}

func (t *treeSource) GetFile(_ context.Context, _ source.RepositoryRef, _ string, path string) ([]byte, error) {
	return []byte("replicaCount: 2\nimage:\n  tag: v1\n"), nil
}

func (t *treeSource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	t.calls++
	out := make([]source.File, 0, len(t.files))
	for _, path := range t.files {
		out = append(out, source.File{Path: path, Size: 100})
	}
	return out, nil
}

func TestFindFilesMatchesNamesGlobsAndPaths(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:find-files?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	for _, file := range []struct {
		path    string
		indexed int
	}{
		{"Dockerfile", 1},
		{"deploy/Dockerfile.build", 0},
		{"api/core/auth.py", 1},
		{"api/core/authz.py", 1},
		{"db/migrations/001_initial.sql", 1},
		{"db/migrations/002_users.sql", 1},
		{"infra/main.tf", 1},
		{"web/logo.png", 0},
	} {
		base := file.path
		if index := strings.LastIndex(base, "/"); index >= 0 {
			base = base[index+1:]
		}
		if _, err = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r','main',?,?,?,?,'abc')`,
			file.path, strings.ToLower(base), 120, file.indexed); err != nil {
			t.Fatal(err)
		}
	}
	service := New(db)
	find := func(pattern string) []string {
		t.Helper()
		result, findErr := service.FindFiles(ctx, []string{"alice"}, pattern, "", "", "", "", "", 50)
		if findErr != nil {
			t.Fatalf("%s: %v", pattern, findErr)
		}
		paths := make([]string, 0, len(result.Files))
		for _, item := range result.Files {
			paths = append(paths, item.Path)
		}
		return paths
	}
	if got := find("Dockerfile"); len(got) != 2 || got[0] != "Dockerfile" {
		t.Fatalf("exact base name must rank first: %v", got)
	}
	if got := find("*.tf"); len(got) != 1 || got[0] != "infra/main.tf" {
		t.Fatalf("glob=%v", got)
	}
	if got := find("auth*.py"); len(got) != 2 {
		t.Fatalf("prefix glob=%v", got)
	}
	if got := find("**/migrations/*.sql"); len(got) != 2 {
		t.Fatalf("recursive glob=%v", got)
	}
	if got := find("db/migrations/"); len(got) != 2 {
		t.Fatalf("path fragment=%v", got)
	}
	if got := find("logo"); len(got) != 1 || got[0] != "web/logo.png" {
		t.Fatalf("a file with no indexed content must still be findable: %v", got)
	}
	// The result says whether the content can be read, so an agent picks the
	// right follow-up tool.
	result, err := service.FindFiles(ctx, []string{"alice"}, "logo.png", "", "", "", "", "", 10)
	if err != nil || len(result.Files) != 1 || result.Files[0].ContentIndexed {
		t.Fatalf("result=%#v err=%v", result.Files, err)
	}
	if hidden, _ := service.FindFiles(ctx, []string{"mallory"}, "Dockerfile", "", "", "", "", "", 10); len(hidden.Files) != 0 {
		t.Fatalf("ACL leak: %#v", hidden.Files)
	}
}

// A repository registered a minute ago has no stored listing. Filename search
// must still answer by reading the tree once.
func TestFindFilesFallsBackToRemoteTreeListing(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:find-files-remote?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','fresh','Fresh','gitlab','1','/core/fresh','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	remote := &treeSource{files: []string{"README.md", "charts/values.yaml", "Dockerfile"}}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })
	result, err := service.FindFiles(ctx, []string{"alice"}, "values.yaml", "", "", "", "", "", 10)
	if err != nil || len(result.Files) != 1 || result.Files[0].Path != "charts/values.yaml" {
		t.Fatalf("result=%#v err=%v", result.Files, err)
	}
	if result.Files[0].Origin != "remote" {
		t.Fatalf("origin=%s", result.Files[0].Origin)
	}
	if remote.calls != 1 {
		t.Fatalf("the tree must be listed once, calls=%d", remote.calls)
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic, "no stored file listing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the live listing must be stated: %v", result.Diagnostics)
	}
}

// A vector database widens the candidate set with semantically close chunks
// that share no keyword, and a failing one must never break search.
func TestVectorDatabaseAddsCandidatesAndFailsSoft(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:vector-candidates?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','gpu','GPU','gitlab','1','/core/gpu','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	// The wanted chunk shares no term with the query; only a vector store can
	// propose it.
	semantic := embedding.Encode(embedding.Embed("accelerator utilisation telemetry"))
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c-semantic','r','main','abc','docs/telemetry.md',1,4,'Telemetry','document','Accelerator utilisation telemetry pipeline.','h1',?)`, semantic)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c-other','r','main','abc','docs/other.md',1,4,'Other','document','Unrelated billing document.','h2',?)`, embedding.Encode(embedding.Embed("billing invoices")))

	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: 1, FinalK: 5, CandidateLimit: 100}
	})
	// Without the vector loader the lexical prefilter finds nothing for this query.
	plain, err := service.Query(ctx, []string{"alice"}, "/core/gpu/main", "zzzqqq")
	if err != nil || strings.Contains(plain, "docs/telemetry.md") {
		t.Fatalf("baseline should not find the semantic chunk: %s err=%v", plain, err)
	}

	var asked int
	service.SetVectorLoader(func(_ context.Context, repositoryID, ref, query string, limit int) ([]VectorCandidate, error) {
		asked++
		if repositoryID != "r" || ref != "main" {
			t.Errorf("scope=%s/%s", repositoryID, ref)
		}
		return []VectorCandidate{{ID: "c-semantic", Score: 0.82}}, nil
	})
	found, err := service.Query(ctx, []string{"alice"}, "/core/gpu/main", "zzzqqq")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(found, "docs/telemetry.md") {
		t.Fatalf("the vector candidate was not retrieved: %s", found)
	}
	if strings.Contains(found, "docs/other.md") {
		t.Fatalf("an unrelated chunk leaked in: %s", found)
	}
	if asked != 1 {
		t.Fatalf("vector loader calls=%d", asked)
	}

	// A broken vector database must degrade to the previous behaviour.
	service.SetVectorLoader(func(context.Context, string, string, string, int) ([]VectorCandidate, error) {
		return nil, errors.New("milvus unavailable")
	})
	degraded, err := service.Query(ctx, []string{"alice"}, "/core/gpu/main", "accelerator")
	if err != nil {
		t.Fatalf("a failing vector database must not fail the search: %v", err)
	}
	if degraded == "" {
		t.Fatal("expected the in-database path to answer")
	}
}

// Semantic search must work with and without a vector database, and must never
// return a repository the caller cannot read.
func TestSemanticSearchUsesVectorDatabaseAndFallsBackToScan(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:semantic-search?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	insert := func(repository, principal, chunkID, path, text string) {
		t.Helper()
		_, _ = db.DB.Exec(`INSERT OR IGNORE INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES(?,'core',?,?,'gitlab',?,?,'main')`,
			repository, repository, repository, repository, "/core/"+repository)
		_, _ = db.DB.Exec(`INSERT OR IGNORE INTO repository_permissions(repository_id,principal,permission) VALUES(?,?,'read')`, repository, principal)
		if _, err := db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES(?,?,'main','abc',?,1,4,?,'document',?,?,?)`,
			chunkID, repository, path, path, text, chunkID, embedding.Encode(embedding.Embed(text))); err != nil {
			t.Fatal(err)
		}
	}
	insert("api", "alice", "c-retry", "internal/webhook/retry.go", "retry failed webhook deliveries with exponential backoff")
	insert("api", "alice", "c-billing", "docs/billing.md", "monthly invoice reconciliation report")
	insert("secret", "bob", "c-hidden", "internal/webhook/retry.go", "retry failed webhook deliveries with exponential backoff")

	service := New(db)
	// Without a vector database the scan still answers.
	scanned, err := service.SemanticSearch(ctx, []string{"alice"}, "retry failed webhook deliveries", "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned.Hits) == 0 || scanned.Hits[0].FilePath != "internal/webhook/retry.go" {
		t.Fatalf("scan hits=%#v", scanned.Hits)
	}
	if scanned.Mode != "in-database scan" {
		t.Fatalf("mode=%s", scanned.Mode)
	}
	for _, hit := range scanned.Hits {
		if hit.LibraryID == "/core/secret" {
			t.Fatalf("ACL leak: %#v", hit)
		}
	}

	// With a vector database the ANN result is used and still ACL filtered.
	service.SetGlobalVectorLoader(func(_ context.Context, query string, limit int) ([]VectorCandidate, error) {
		return []VectorCandidate{{ID: "c-hidden", Score: 0.99}, {ID: "c-retry", Score: 0.95}}, nil
	})
	vectored, err := service.SemanticSearch(ctx, []string{"alice"}, "retry failed webhook deliveries", "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if vectored.Mode != "vector database ANN" {
		t.Fatalf("mode=%s", vectored.Mode)
	}
	if len(vectored.Hits) != 1 || vectored.Hits[0].LibraryID != "/core/api" {
		t.Fatalf("the ANN result must be ACL filtered: %#v", vectored.Hits)
	}
	if vectored.Hits[0].Score < 0.9 {
		t.Fatalf("the store score must be preserved: %#v", vectored.Hits[0])
	}

	// A failing vector database degrades to the scan instead of erroring.
	service.SetGlobalVectorLoader(func(context.Context, string, int) ([]VectorCandidate, error) {
		return nil, errors.New("pgvector unavailable")
	})
	degraded, err := service.SemanticSearch(ctx, []string{"alice"}, "retry failed webhook deliveries", "", "", 5)
	if err != nil || len(degraded.Hits) == 0 || degraded.Mode != "in-database scan" {
		t.Fatalf("degraded=%#v err=%v", degraded, err)
	}
	if !strings.Contains(strings.Join(degraded.Diagnostics, " "), "falling back") {
		t.Fatalf("the fallback must be stated: %v", degraded.Diagnostics)
	}
}

func TestEmbeddingPolicyKeepsQueryAndSemanticSearchAvailableWithoutVectors(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:embedding-policy?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','dify','Dify','gitlab','1','/core/dify','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c','r','main','abc','api/search.go',10,20,'Search service','code','Dify source search query implementation','h',NULL)`)

	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 5, CandidateLimit: 100, RetrievalMode: RetrievalKeywordOnly}
	})
	service.SetEmbeddingLoader(func(context.Context) (embedding.Provider, error) {
		return nil, errors.New("embedding endpoint is down")
	})
	semantic, err := service.SemanticSearch(ctx, []string{"alice"}, "Dify source search", "", "", 5)
	if err != nil || len(semantic.Hits) != 1 || semantic.Hits[0].FilePath != "api/search.go" {
		t.Fatalf("semantic=%#v err=%v", semantic, err)
	}
	if !strings.Contains(semantic.Mode, "keyword") {
		t.Fatalf("mode=%q", semantic.Mode)
	}
	query, err := service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search")
	if err != nil || !strings.Contains(query, "api/search.go") || !strings.Contains(query, "Embedding use is disabled") {
		t.Fatalf("query=%q err=%v", query, err)
	}

	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 5, CandidateLimit: 100, RetrievalMode: RetrievalHybridFallback}
	})
	query, err = service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search")
	if err != nil || !strings.Contains(query, "keyword/source-query retrieval only") {
		t.Fatalf("fallback query=%q err=%v", query, err)
	}
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 5, CandidateLimit: 100, RetrievalMode: RetrievalHybridRequired}
	})
	if _, err = service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search"); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("required mode error=%v", err)
	}
}

func TestEmbeddingCoveragePreventsPartialVectorBias(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:embedding-coverage?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','dify','Dify','gitlab','1','/core/dify','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c','r','main','abc','api/search.go',10,20,'Search service','code','Dify source search query implementation','h',NULL)`)
	_, _ = db.DB.Exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,total_chunks,embedded_chunks,embedding_status) VALUES('r','main','abc',10,2,'partial')`)

	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{
			KeywordWeight: 1, VectorWeight: .35, FinalK: 5, CandidateLimit: 100,
			RetrievalMode: RetrievalHybridFallback, MinimumEmbeddingCoverage: 80,
		}
	})
	vectorCalls := 0
	service.SetVectorLoader(func(context.Context, string, string, string, int) ([]VectorCandidate, error) {
		vectorCalls++
		return []VectorCandidate{{ID: "c", Score: 1}}, nil
	})
	fallbacks := map[string]int{}
	service.SetFallbackReporter(func(reason string) { fallbacks[reason]++ })

	query, err := service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search")
	if err != nil || !strings.Contains(query, "api/search.go") {
		t.Fatalf("query=%q err=%v", query, err)
	}
	if !strings.Contains(query, "coverage 20.0%") || !strings.Contains(query, "keyword/source-query retrieval only") {
		t.Fatalf("partial coverage was not explained: %q", query)
	}
	if vectorCalls != 0 || fallbacks["coverage-below-threshold"] != 1 {
		t.Fatalf("vectorCalls=%d fallbacks=%v", vectorCalls, fallbacks)
	}

	semantic, err := service.SemanticSearch(ctx, []string{"alice"}, "Dify source search", "", "", 5)
	if err != nil || len(semantic.Hits) != 1 || !strings.Contains(semantic.Mode, "keyword") {
		t.Fatalf("semantic=%#v err=%v", semantic, err)
	}
	if fallbacks["coverage-below-threshold"] != 2 {
		t.Fatalf("semantic fallback was not recorded: %v", fallbacks)
	}

	remote := &querySource{}
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })
	query, err = service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search")
	if err != nil || !strings.Contains(query, "source-api.md") || !strings.Contains(query, "Retrieval notice: Embedding coverage 20.0%") {
		t.Fatalf("remote fallback query=%q err=%v", query, err)
	}
	if remote.calls != 1 || fallbacks["coverage-below-threshold"] != 3 {
		t.Fatalf("remote calls=%d fallbacks=%v", remote.calls, fallbacks)
	}

	service.SetConfigLoader(func(context.Context) Config {
		return Config{
			KeywordWeight: 1, VectorWeight: .35, FinalK: 5, CandidateLimit: 100,
			RetrievalMode: RetrievalHybridRequired, MinimumEmbeddingCoverage: 80,
		}
	})
	if _, err = service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search"); err == nil || !strings.Contains(err.Error(), "coverage 20.0%") {
		t.Fatalf("required query error=%v", err)
	}
	if _, err = service.SemanticSearch(ctx, []string{"alice"}, "Dify source search", "", "", 5); err == nil || !strings.Contains(err.Error(), "coverage 20.0%") {
		t.Fatalf("required semantic error=%v", err)
	}
}

func TestEmbeddingRevisionMismatchNeverMixesOldVectors(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:embedding-revision?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','dify','Dify','gitlab','1','/core/dify','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding,embedding_revision) VALUES('c','r','main','abc','api/search.go',10,20,'Search service','code','Dify source search query implementation','h',?,'model-v1')`, embedding.Encode(embedding.Embed("old vector")))
	_, _ = db.DB.Exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,embedding_revision,total_chunks,embedded_chunks,embedding_status) VALUES('r','main','abc','model-v1',1,1,'ready')`)

	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{
			KeywordWeight: 1, VectorWeight: 1, FinalK: 5, CandidateLimit: 100,
			RetrievalMode: RetrievalHybridFallback, MinimumEmbeddingCoverage: 80,
			EmbeddingRevision: "model-v2",
		}
	})
	vectorCalls := 0
	service.SetVectorLoader(func(context.Context, string, string, string, int) ([]VectorCandidate, error) {
		vectorCalls++
		return []VectorCandidate{{ID: "c", Score: 1}}, nil
	})
	fallbacks := map[string]int{}
	service.SetFallbackReporter(func(reason string) { fallbacks[reason]++ })

	query, err := service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search")
	if err != nil || !strings.Contains(query, "api/search.go") || !strings.Contains(query, "different model revision") {
		t.Fatalf("query=%q err=%v", query, err)
	}
	if vectorCalls != 0 || fallbacks["embedding-revision-mismatch"] != 1 {
		t.Fatalf("vectorCalls=%d fallbacks=%v", vectorCalls, fallbacks)
	}
	semantic, err := service.SemanticSearch(ctx, []string{"alice"}, "Dify source search", "", "", 5)
	if err != nil || len(semantic.Hits) != 1 || !strings.Contains(strings.Join(semantic.Diagnostics, " "), "different embedding model revision") {
		t.Fatalf("semantic=%#v err=%v", semantic, err)
	}
	if fallbacks["embedding-revision-mismatch"] != 2 {
		t.Fatalf("semantic revision fallback was not recorded: %v", fallbacks)
	}

	service.SetConfigLoader(func(context.Context) Config {
		return Config{
			KeywordWeight: 1, VectorWeight: 1, FinalK: 5, CandidateLimit: 100,
			RetrievalMode: RetrievalHybridRequired, MinimumEmbeddingCoverage: 80,
			EmbeddingRevision: "model-v2",
		}
	})
	if _, err = service.Query(ctx, []string{"alice"}, "/core/dify/main", "Dify source search"); err == nil || !strings.Contains(err.Error(), "different model revision") {
		t.Fatalf("required revision error=%v", err)
	}
}

// Before changing shared code an agent must see every repository that uses it,
// not only the one it happens to be reading.
func TestFindDependentsCrossesRepositoriesAndRespectsACL(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:dependents?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	for _, repository := range []struct{ id, library, principal string }{
		{"a", "/core/api", "alice"},
		{"b", "/core/web", "alice"},
		{"c", "/core/secret", "bob"},
	} {
		_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES(?,'core',?,?,'gitlab',?,?,'main')`,
			repository.id, repository.id, repository.id, repository.id, repository.library)
		_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,?,'read')`, repository.id, repository.principal)
		_, _ = db.DB.Exec(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES(?,?,'main','c1','main.go','Boot','internal/auth','import',7)`,
			"dep-"+repository.id, repository.id)
	}
	// An incidental substring must rank below the exact target.
	_, _ = db.DB.Exec(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES('dep-extra','a','main','c1','extra.go','Extra','internal/authz','import',3)`)

	service := New(db)
	result, err := service.FindDependents(ctx, []string{"alice"}, "internal/auth", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 {
		t.Fatalf("expected the two readable repositories, got %v", result.Repositories)
	}
	for _, item := range result.Dependents {
		if item.LibraryID == "/core/secret" {
			t.Fatalf("ACL leak: %#v", item)
		}
	}
	if result.Dependents[0].Target != "internal/auth" {
		t.Fatalf("exact target must rank first: %#v", result.Dependents[0])
	}
	if len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0], "still indexing") {
		t.Fatalf("the coverage caveat must be stated: %v", result.Diagnostics)
	}
	if empty, _ := service.FindDependents(ctx, nil, "internal/auth", "", 50); len(empty.Dependents) != 0 {
		t.Fatalf("unmapped identity leak: %#v", empty)
	}
}

func TestCompareRefsDetectsFunctionBodyOnlyChanges(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:compare-body?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','api','API','gitlab','1','/core/api','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	for _, item := range []struct {
		ref, commit, bodyHash string
	}{
		{"base", "base-commit", "body-v1"},
		{"head", "head-commit", "body-v2"},
	} {
		_, _ = db.DB.Exec(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash)
VALUES(?,'r',?,?,'service.go','Run','Run','function','go','func Run()','starts work',10,12,'legacy-signature-hash')`,
			"symbol-"+item.ref, item.ref, item.commit)
		_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
VALUES(?,'r',?,?,'service.go',10,12,'Run','code',?,?)`,
			"chunk-"+item.ref, item.ref, item.commit, "func Run() { "+item.bodyHash+" }", item.bodyHash)
		_, _ = db.DB.Exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('r',?,?)`, item.ref, item.commit)
	}
	comparison, err := New(db).CompareRefs(ctx, []string{"alice"}, "/core/api", "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Changes) != 1 || comparison.Changes[0].Type != "modified" || comparison.Changes[0].Name != "Run" {
		t.Fatalf("body-only change was missed: %#v", comparison.Changes)
	}
	if _, err = New(db).CompareRefs(ctx, []string{"alice"}, "/core/api", "base", "typo"); err == nil || !strings.Contains(err.Error(), "both baseRef and headRef") {
		t.Fatalf("missing ref was treated as a real comparison: %v", err)
	}
}

// An agent that reads a repository's own rules writes code that fits the
// project, so the map points at them.
func TestRepositoryMapListsProjectConventions(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:conventions?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_maps(repository_id,ref_name,commit_id,summary_json) VALUES('r','main','c1','{"languages":{"go":3}}')`)
	for _, path := range []string{"CONTRIBUTING.md", "AGENTS.md", "docs/adr/0001-auth.md", "internal/app/app.go", "vendor/lib/README.md"} {
		base := path
		if index := strings.LastIndex(base, "/"); index >= 0 {
			base = base[index+1:]
		}
		_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r','main',?,?,10,1,'c1')`, path, strings.ToLower(base))
	}
	result, err := New(db).RepositoryMap(ctx, []string{"alice"}, "/core/demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(result.Conventions, ",")
	for _, expected := range []string{"CONTRIBUTING.md", "AGENTS.md", "docs/adr/0001-auth.md"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("conventions must include %s: %v", expected, result.Conventions)
		}
	}
	if strings.Contains(joined, "internal/app/app.go") {
		t.Fatalf("ordinary source files are not conventions: %v", result.Conventions)
	}
}

// Finding a file only helps if the agent can then read it. Indexed files come
// from the stored chunks, everything else is read live, and both paths mask
// credentials and bound the response.
func TestReadFileServesIndexedAndUnindexedFiles(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:read-file?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r','main','docs/gpu.md','gpu.md',40,1,'abc')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r','main','charts/values.yaml','values.yaml',20,0,'abc')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','r','main','abc','docs/gpu.md',1,2,'GPU','document','# GPU\nfirst chunk','h1')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c2','r','main','abc','docs/gpu.md',3,4,'GPU','document','second chunk','h2')`)
	remote := &treeSource{}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	indexed, err := service.ReadFile(ctx, []string{"alice"}, "", "", "docs/gpu.md", "", 0, 0)
	if err != nil || indexed.Origin != "index" || !strings.Contains(indexed.Content, "second chunk") {
		t.Fatalf("indexed read=%#v err=%v", indexed, err)
	}
	if indexed.TotalLines != 2 || indexed.StartLine != 1 {
		t.Fatalf("line accounting=%#v", indexed)
	}

	// A line range must narrow the response.
	ranged, err := service.ReadFile(ctx, []string{"alice"}, "", "", "docs/gpu.md", "", 2, 2)
	if err != nil || ranged.StartLine != 2 || ranged.EndLine != 2 || strings.Contains(ranged.Content, "# GPU") {
		t.Fatalf("ranged read=%#v err=%v", ranged, err)
	}

	// A file the policy skipped is read live and marked as such.
	live, err := service.ReadFile(ctx, []string{"alice"}, "", "", "charts/values.yaml", "", 0, 0)
	if err != nil || live.Origin != "remote" || live.Content == "" {
		t.Fatalf("remote read=%#v err=%v", live, err)
	}

	if _, err = service.ReadFile(ctx, []string{"mallory"}, "", "", "docs/gpu.md", "", 0, 0); err == nil {
		t.Fatal("ACL leak")
	}
	if _, err = service.ReadFile(ctx, []string{"alice"}, "", "", "nope.md", "", 0, 0); err == nil {
		t.Fatal("unknown path must fail")
	}
}

func TestFileToolsDefaultToRepositoryDefaultBranch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:file-default-ref?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	for _, item := range []struct {
		ref, path, commit string
	}{
		{"main", "docs/shared.md", "main-commit"},
		{"main", "docs/main-only.md", "main-commit"},
		{"aaa-dev", "docs/shared.md", "dev-commit"},
		{"aaa-dev", "docs/dev-only.md", "dev-commit"},
	} {
		base := path.Base(item.path)
		_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r',?,?,?,20,1,?)`,
			item.ref, item.path, base, item.commit)
	}
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('main-chunk','r','main','main-commit','docs/shared.md',1,1,'Shared','document','main branch content','main-hash')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('dev-chunk','r','aaa-dev','dev-commit','docs/shared.md',1,1,'Shared','document','development branch content','dev-hash')`)

	service := New(db)
	defaultFile, err := service.ReadFile(ctx, []string{"alice"}, "/core/demo", "", "docs/shared.md", "", 0, 0)
	if err != nil || defaultFile.Ref != "main" || defaultFile.Content != "main branch content" {
		t.Fatalf("default file=%#v err=%v", defaultFile, err)
	}
	devFile, err := service.ReadFile(ctx, []string{"alice"}, "/core/demo/aaa-dev", "", "docs/shared.md", "", 0, 0)
	if err != nil || devFile.Ref != "aaa-dev" || devFile.Content != "development branch content" {
		t.Fatalf("versioned file=%#v err=%v", devFile, err)
	}

	defaultFiles, err := service.FindFiles(ctx, []string{"alice"}, "*.md", "/core/demo", "", "", "", "", 20)
	if err != nil || len(defaultFiles.Files) != 2 {
		t.Fatalf("default files=%#v err=%v", defaultFiles.Files, err)
	}
	for _, file := range defaultFiles.Files {
		if file.Ref != "main" || file.Path == "docs/dev-only.md" {
			t.Fatalf("default search mixed refs: %#v", defaultFiles.Files)
		}
	}
	devFiles, err := service.FindFiles(ctx, []string{"alice"}, "*.md", "/core/demo/aaa-dev", "", "", "", "", 20)
	if err != nil || len(devFiles.Files) != 2 {
		t.Fatalf("versioned files=%#v err=%v", devFiles.Files, err)
	}
	for _, file := range devFiles.Files {
		if file.Ref != "aaa-dev" || file.Path == "docs/main-only.md" {
			t.Fatalf("versioned search ignored the library ref: %#v", devFiles.Files)
		}
	}

	listing, err := service.ListDirectory(ctx, []string{"alice"}, "/core/demo", "", "docs", "")
	if err != nil || listing.Ref != "main" || len(listing.Entries) != 2 {
		t.Fatalf("default directory=%#v err=%v", listing, err)
	}
	resolved, err := service.resolveRepositoryPath(ctx, []string{"alice"}, "/core/demo", "", "docs/shared.md", "")
	if err != nil || resolved.refName != "main" {
		t.Fatalf("default resolved path=%#v err=%v", resolved, err)
	}
	resolved, err = service.resolveRepositoryPath(ctx, []string{"alice"}, "/core/demo/aaa-dev", "", "docs/shared.md", "")
	if err != nil || resolved.refName != "aaa-dev" {
		t.Fatalf("versioned resolved path=%#v err=%v", resolved, err)
	}
}

// The same path can exist in several repositories; the caller must be asked
// rather than served a random one.
func TestReadFileAsksWhichRepositoryWhenAmbiguous(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:read-file-ambiguous?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	for _, id := range []string{"a", "b"} {
		_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES(?,?,?,?,'gitlab',?,?,'main')`,
			id, "core", "repo-"+id, "Repo "+id, id, "/core/repo-"+id)
		_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,'alice','read')`, id)
		_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES(?,'main','Dockerfile','dockerfile',10,1,'abc')`, id)
		_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,?,'main','abc','Dockerfile',1,1,'Dockerfile','code','FROM alpine',?)`, "chunk-"+id, id, "h-"+id)
	}
	service := New(db)
	result, err := service.ReadFile(ctx, []string{"alice"}, "", "", "Dockerfile", "", 0, 0)
	if err == nil || len(result.Candidates) != 2 {
		t.Fatalf("ambiguous read must list candidates: %#v err=%v", result, err)
	}
	if !strings.Contains(err.Error(), "libraryId") {
		t.Fatalf("the error must say how to disambiguate: %v", err)
	}
	chosen, err := service.ReadFile(ctx, []string{"alice"}, "/core/repo-a", "", "Dockerfile", "", 0, 0)
	if err != nil || chosen.LibraryID != "/core/repo-a" {
		t.Fatalf("scoped read=%#v err=%v", chosen, err)
	}
}

// A registered repository that is still being embedded has no chunks yet.
// query-docs must fail over to the source code search API instead of answering
// "not indexed", which is useless to a coding agent.
func TestQueryFailsOverToSourceSearchWhileIndexing(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:query-failover?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	remote := &querySource{}
	service := New(db)
	// A remote embedding model is configured, so the source query mode is off and
	// only the failover can produce an answer.
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: 0.35, FinalK: 8, CandidateLimit: 100, SourceQuerySearch: false}
	})
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })
	result, err := service.Query(ctx, []string{"alice"}, "/core/demo/main", "source API")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "docs/source-api.md") || !strings.Contains(result, "source API result") {
		t.Fatalf("failover did not return remote content: %s", result)
	}
	if !strings.Contains(result, "no local index yet") {
		t.Fatalf("the failover mode must be stated: %s", result)
	}
	remote.mu.Lock()
	calls := remote.calls
	remote.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly one remote query, got %d", calls)
	}

	// An unauthorized principal must not reach the remote API through failover.
	before := calls
	if _, err = service.Query(ctx, []string{"mallory"}, "/core/demo/main", "source API"); err == nil {
		t.Fatal("expected an ACL error")
	}
	remote.mu.Lock()
	after := remote.calls
	remote.mu.Unlock()
	if after != before {
		t.Fatalf("failover leaked past the ACL: calls %d -> %d", before, after)
	}
}

// search-code must return file contents, not only repository names, for a
// repository that is registered but not indexed.
func TestSearchCodeReturnsCodeForRegisteredButUnindexedRepository(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:code-without-index?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	for index := 1; index <= 5; index++ {
		id := fmt.Sprintf("r%d", index)
		_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch) VALUES(?,?,?,?,?,'gitlab',?,?,'main')`,
			id, "core", fmt.Sprintf("demo-%d", index), fmt.Sprintf("Demo %d", index), "gpu platform", fmt.Sprint(index), fmt.Sprintf("/core/demo-%d", index))
		_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,'alice','read')`, id)
	}
	remote := &querySource{}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })
	result, err := service.SearchCode(ctx, []string{"alice"}, "gpu", "gitlab", "", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 5 {
		t.Fatalf("repositories=%d", len(result.Repositories))
	}
	if len(result.Hits) == 0 {
		t.Fatalf("code hits are missing even though the source API answered: %#v", result)
	}
	remote.mu.Lock()
	calls := remote.calls
	remote.mu.Unlock()
	if calls != 5 {
		t.Fatalf("every candidate repository must be queried, calls=%d", calls)
	}
}

// An account with no Bitbucket or GitLab claim must be told why the result set
// is empty instead of receiving a silent zero-result response.
func TestSearchCodeExplainsMissingACLPrincipal(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:missing-acl-principal?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	result, err := New(db).SearchCode(ctx, nil, "verify_token", "", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Warning == "" || len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0], "acl") {
		t.Fatalf("missing ACL diagnostics: %#v", result)
	}
}

func TestOpenSearchCandidatesAreHydratedFromIndexedDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:keyword-candidate?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('safe','r','main','abc','docs/safe.md',1,3,'Unrelated heading','document','authoritative database content','hash')`)
	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: 0, FinalK: 8, CandidateLimit: 100}
	})
	called := false
	service.SetKeywordLoader(func(_ context.Context, repo, ref string, principals []string, query string, limit int) ([]KeywordCandidate, error) {
		called = repo == "r" && ref == "main" && len(principals) == 1 && principals[0] == "alice"
		return []KeywordCandidate{{ID: "safe", Score: 9}}, nil
	})
	result, err := service.Query(ctx, []string{"alice"}, "/core/demo/main", "term absent from local content")
	if err != nil || !called || !strings.Contains(result, "authoritative database content") {
		t.Fatalf("result=%s called=%v err=%v", result, called, err)
	}
	if _, err = service.Query(ctx, []string{"mallory"}, "/core/demo/main", "anything"); err == nil {
		t.Fatal("repository ACL was not enforced before OpenSearch")
	}
}

type fixedReranker struct {
	scores []float64
	err    error
}

func (f fixedReranker) Rerank(context.Context, string, []string) ([]float64, error) {
	return f.scores, f.err
}

var _ rerank.Provider = fixedReranker{}

func TestRerankerRunsAfterACLAndChangesFinalOrder(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:rerank-search?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','p','repo','Repo','bitbucket','r','/p/repo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	insert := func(id, heading, path string) {
		_, e := db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES(?,'r','main','abc',?,1,2,?,'document','GPU Pod guide',?,?)`, id, path, heading, id, embedding.Encode(embedding.Embed(heading+" GPU Pod guide")))
		if e != nil {
			t.Fatal(e)
		}
	}
	insert("a", "First", "first.md")
	insert("b", "Second", "second.md")
	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 1, CandidateLimit: 100, RerankLimit: 2}
	})
	service.SetRerankerLoader(func(context.Context) rerank.Provider { return fixedReranker{scores: []float64{0.1, 0.9}} })
	result, err := service.Query(ctx, []string{"alice"}, "/p/repo/main", "GPU Pod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "second.md") || strings.Contains(result, "first.md") {
		t.Fatalf("reranker did not change final order: %s", result)
	}
	if _, err = service.Query(ctx, []string{"mallory"}, "/p/repo/main", "GPU Pod"); err == nil {
		t.Fatal("unauthorized query unexpectedly reached results")
	}
}

// failingSource stands in for a source server that is down. Every call fails
// the same way a connection error does.
type failingSource struct {
	querySource
	mu    sync.Mutex
	calls int
}

func (f *failingSource) SearchQuery(context.Context, source.RepositoryRef, string, string, int) ([]source.QueryResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil, errors.New("dial tcp 10.0.0.9:443: connect: connection refused")
}

// countingBreaker is the registry the service talks to, with the same contract
// the application implements over source.Breakers.
type countingBreaker struct {
	mu       sync.Mutex
	failures int
	open     bool
}

func (c *countingBreaker) Allow(string) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open {
		return false, "연동이 일시 중단되었습니다."
	}
	return true, ""
}

func (c *countingBreaker) Report(_ string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.failures = 0
		return
	}
	c.failures++
	if c.failures >= 3 {
		c.open = true
	}
}

// When a source server is down the search must stop calling it, keep answering
// from the index, and say that the live path was skipped — instead of spending
// the whole tool timeout on one failing host per repository.
func TestFailingSourceIsPausedAndReported(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:breaker-search?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("gpu-%d", index)
		if _, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES(?,'core',?,?,'gitlab',?,?,'main')`,
			id, id, id, id, "/core/"+id); err != nil {
			t.Fatal(err)
		}
		if _, err = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,'alice','read')`, id); err != nil {
			t.Fatal(err)
		}
	}
	service := New(db)
	remote := &failingSource{}
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })
	breaker := &countingBreaker{}
	service.SetBreakers(breaker)

	if _, err = service.SearchSource(ctx, []string{"alice"}, "gpu", "gitlab", "", "", "", 10); err == nil {
		t.Fatal("a failing source must report an error on the first search")
	}
	firstRound := remote.calls
	if firstRound == 0 {
		t.Fatal("the first search must actually try the source")
	}

	// The breaker is open now, so the next search must not touch the server.
	result, err := service.SearchSource(ctx, []string{"alice"}, "gpu", "gitlab", "", "", "", 10)
	if err == nil || !errors.Is(err, ErrSourcePaused) {
		t.Fatalf("a paused source must be reported as paused: %v %#v", err, result)
	}
	if remote.calls != firstRound {
		t.Fatalf("the paused source was called again: %d then %d", firstRound, remote.calls)
	}

	// search-code keeps working: the repository list comes from the index and the
	// diagnostics explain that the live search was skipped.
	code, err := service.SearchCode(ctx, []string{"alice"}, "gpu", "gitlab", "", "", "", 10)
	if err != nil {
		t.Fatalf("search-code must degrade instead of failing: %v", err)
	}
	if len(code.Repositories) == 0 {
		t.Fatal("the indexed repository list must still be returned")
	}
	if !strings.Contains(strings.Join(code.Diagnostics, " "), "일시 중단") {
		t.Fatalf("the diagnostics must state that the source is paused: %v", code.Diagnostics)
	}
}

// An empty principal set must not build "IN ()": SQLite accepts it and yields
// false, but PostgreSQL rejects it outright, so the tests would stay green while
// production broke. The clause denies instead.
func TestRepositoryACLDeniesEmptyPrincipalSet(t *testing.T) {
	join, predicate, args := RepositoryACLClause(nil)
	if strings.Contains(predicate, "IN ()") {
		t.Fatalf("predicate builds an empty IN list: %q", predicate)
	}
	if predicate != "1=0" {
		t.Errorf("predicate = %q, want a clause that matches nothing", predicate)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none", args)
	}
	if !strings.Contains(join, "JOIN repository_permissions") {
		t.Errorf("join = %q, want the permissions join kept so the p alias resolves", join)
	}
}

func TestRepositoryACLStillScopesToPrincipals(t *testing.T) {
	_, predicate, args := RepositoryACLClause([]string{"alice", "group:devs"})
	if !strings.Contains(predicate, "IN (?,?)") {
		t.Errorf("predicate = %q, want one placeholder per principal", predicate)
	}
	if len(args) != 2 {
		t.Errorf("args = %v, want both principals bound", args)
	}
}

// failingEmbedder stands in for a model endpoint that is reachable at
// configuration time and broken afterwards.
type failingEmbedder struct{ calls int }

func (f *failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	f.calls++
	return nil, errors.New("connection refused")
}

type fixedEmbedder struct{ calls int }

func (f *fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	f.calls++
	return embedding.Embed("Dify source search"), nil
}

// An operator whose semantic search stops working needs to know which half
// broke. The two halves fail differently on purpose: a dead model endpoint
// leaves nothing to score with and drops to keyword retrieval, while a dead
// vector database still has usable embeddings in the metadata database and must
// keep scoring them rather than giving up on meaning.
func TestSemanticSearchDegradesDifferentlyForModelAndVectorFailures(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:degrade-split?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','dify','Dify','gitlab','1','/core/dify','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding,embedding_revision) VALUES('c','r','main','abc','api/search.go',10,20,'Search service','code','Dify source search query implementation','h',?,'')`, embedding.Encode(embedding.Embed("Dify source search query implementation")))
	_, _ = db.DB.Exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,total_chunks,embedded_chunks,embedding_status) VALUES('r','main','abc',1,1,'ready')`)

	newService := func() (*Service, map[string]int) {
		service := New(db)
		service.SetConfigLoader(func(context.Context) Config {
			return Config{KeywordWeight: 1, VectorWeight: .35, FinalK: 5, CandidateLimit: 100,
				RetrievalMode: RetrievalHybridFallback, MinimumEmbeddingCoverage: 50}
		})
		fallbacks := map[string]int{}
		service.SetFallbackReporter(func(reason string) { fallbacks[reason]++ })
		return service, fallbacks
	}

	t.Run("모델 엔드포인트 다운", func(t *testing.T) {
		service, fallbacks := newService()
		embedder := &failingEmbedder{}
		service.SetEmbeddingLoader(func(context.Context) (embedding.Provider, error) { return embedder, nil })
		vectorCalls := 0
		service.SetGlobalVectorLoader(func(context.Context, string, int) ([]VectorCandidate, error) {
			vectorCalls++
			return []VectorCandidate{{ID: "c", Score: 1}}, nil
		})

		result, err := service.SemanticSearch(ctx, []string{"alice"}, "Dify source search", "", "", 5)
		if err != nil {
			t.Fatalf("a dead model endpoint must degrade, not fail: %v", err)
		}
		if len(result.Hits) == 0 {
			t.Error("degraded search returned nothing")
		}
		if !strings.Contains(result.Mode, "keyword") {
			t.Errorf("Mode = %q, want keyword retrieval", result.Mode)
		}
		if fallbacks["query-embedding-failed"] != 1 {
			t.Errorf("fallbacks = %v, want the model failure named", fallbacks)
		}
		if embedder.calls == 0 {
			t.Error("the model was never tried")
		}
		// Without a query vector there is nothing to search the ANN index with.
		if vectorCalls != 0 {
			t.Errorf("the vector database was queried %d times with no query vector", vectorCalls)
		}
		if !hasDiagnostic(result.Diagnostics, "embedding") {
			t.Errorf("diagnostics do not point at the model: %v", result.Diagnostics)
		}
	})

	t.Run("벡터 DB 다운", func(t *testing.T) {
		service, fallbacks := newService()
		embedder := &fixedEmbedder{}
		service.SetEmbeddingLoader(func(context.Context) (embedding.Provider, error) { return embedder, nil })
		service.SetGlobalVectorLoader(func(context.Context, string, int) ([]VectorCandidate, error) {
			return nil, errors.New("dial tcp: connection refused")
		})

		result, err := service.SemanticSearch(ctx, []string{"alice"}, "Dify source search", "", "", 5)
		if err != nil {
			t.Fatalf("a dead vector database must degrade, not fail: %v", err)
		}
		if len(result.Hits) == 0 {
			t.Error("degraded search returned nothing")
		}
		// The embeddings are still in the metadata database, so meaning-based
		// scoring must continue rather than dropping to keywords.
		if strings.Contains(result.Mode, "keyword") {
			t.Errorf("Mode = %q, want the in-database embedding scan, not keywords", result.Mode)
		}
		if fallbacks["query-embedding-failed"] != 0 {
			t.Errorf("fallbacks = %v, want no model failure recorded", fallbacks)
		}
		if embedder.calls == 0 {
			t.Error("the model should still be used to embed the query")
		}
		if !hasDiagnostic(result.Diagnostics, "vector database") {
			t.Errorf("diagnostics do not point at the vector database: %v", result.Diagnostics)
		}
	})
}

func hasDiagnostic(diagnostics []string, want string) bool {
	for _, item := range diagnostics {
		if strings.Contains(strings.ToLower(item), strings.ToLower(want)) {
			return true
		}
	}
	return false
}

// The content leg of search-code queries the source servers because the index
// can lag a push. When that query cannot run — a paused connector, a Bitbucket
// without code search, an outage — the answer used to be empty even though the
// file was sitting in this platform's own index, and an agent reads an empty
// answer as "the code does not exist".
func TestSearchCodeAnswersFromTheIndexWhenTheSourceQueryCannot(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:code-index-fallback?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','payments','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','gitlab:1','main','abc123','README.md',1,4,'Payments','document','DCGM exporter collects the metrics.','h1')`)
	exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('gitlab:1','main','abc123')`)

	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("gitlab search API is unavailable")
	})
	result, err := service.SearchCode(ctx, []string{"alice"}, "DCGM exporter", "gitlab", "", "", "", 10)
	if err != nil {
		t.Fatalf("a failing source query must not fail the call when the index can answer: %v", err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Path != "README.md" || result.Hits[0].LibraryID != "/core/api" {
		t.Fatalf("the indexed content was not returned: %#v diagnostics=%v", result.Hits, result.Diagnostics)
	}
	if result.Hits[0].Ref != "main" || result.Hits[0].CommitID != "abc123" {
		t.Fatalf("the answer must cite the indexed ref and commit: %#v", result.Hits[0])
	}
	joined := strings.Join(result.Diagnostics, " ")
	// The reader has to be told both that this came from the index and why the
	// live path did not run — the answer is only as fresh as the last run.
	if !strings.Contains(joined, "index:") || !strings.Contains(joined, "unavailable") {
		t.Fatalf("diagnostics must explain the path taken: %v", result.Diagnostics)
	}

	// A query that matches nothing stays empty rather than inventing hits.
	empty, err := service.SearchCode(ctx, []string{"alice"}, "kubernetes operator", "gitlab", "", "", "", 10)
	if err == nil && len(empty.Hits) != 0 {
		t.Fatalf("unrelated query matched: %#v", empty.Hits)
	}
	// The ACL still applies to the fallback.
	denied, err := service.SearchCode(ctx, []string{"mallory"}, "DCGM exporter", "gitlab", "", "", "", 10)
	if err == nil && len(denied.Hits) != 0 {
		t.Fatalf("the index fallback leaked past the ACL: %#v", denied.Hits)
	}
}

// A lexical scan over the index stops at a fixed number of rows. On a corpus
// where a common term matches most chunks, the answer is then a sample of
// whatever was read first — and an agent that is not told this concludes the
// code lives in the handful of repositories it can see.
func TestAPartialIndexScanSaysThatItIsPartial(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:scan-cap?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if _, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`); err != nil {
		t.Fatal(err)
	}
	transaction, err := db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < indexedScanLimit+50; index++ {
		if _, err = transaction.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'gitlab:1','main','abc',?,1,4,'handler','code','payment handling code',?)`,
			fmt.Sprintf("c%d", index), fmt.Sprintf("internal/payment_%d.go", index), fmt.Sprintf("h%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err = transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("gitlab search API is unavailable")
	})
	result, err := service.SearchCode(ctx, []string{"alice"}, "payment", "gitlab", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) == 0 {
		t.Fatalf("the index must still answer: %v", result.Diagnostics)
	}
	joined := strings.Join(result.Diagnostics, " ")
	if !strings.Contains(joined, "sample rather than every match") {
		t.Fatalf("a capped scan must say so: %v", result.Diagnostics)
	}

	// A query whose matches fit inside the cap must not carry the warning: a
	// caveat on every answer is a caveat nobody reads.
	small, err := store.Open(ctx, "sqlite", "file:scan-cap-small?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer small.DB.Close()
	if _, err = small.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1);
INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read');
INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','gitlab:1','main','abc','internal/payment.go',1,4,'handler','code','payment handling code','h1')`); err != nil {
		t.Fatal(err)
	}
	smallService := New(small)
	smallService.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("gitlab search API is unavailable")
	})
	complete, err := smallService.SearchCode(ctx, []string{"alice"}, "payment", "gitlab", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(complete.Diagnostics, " "), "sample rather than every match") {
		t.Fatalf("a complete scan must not claim to be partial: %v", complete.Diagnostics)
	}
}

// The indexed lookup and the scan it replaces have to answer the same question.
// This test runs whichever path the build provides, so it holds on a binary
// built without FTS5 as well — and on one built with it, it is the proof that
// switching to the index did not change what a caller gets.
func TestIndexedSearchFindsTheSameContentThroughEitherPath(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:fulltext-parity?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES
('c1','gitlab:1','main','abc','internal/settlement/handler.go',1,20,'Settlement','code','func settleInvoice() error { return nil }','h1'),
('c2','gitlab:1','main','abc','internal/cache/warm.go',1,20,'Cache','code','func warmCache() error { return nil }','h2')`)

	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("gitlab search API is unavailable")
	})
	// An exact word, a prefix, a path fragment, and a fragment inside a
	// camel-case identifier all have to reach the file. The last one is what a
	// word index cannot do on its own: "invoice" is not a prefix of
	// settleInvoice, and someone searching for it still means that function.
	for _, query := range []string{"settleInvoice", "settlement", "settle", "invoice"} {
		result, err := service.SearchCode(ctx, []string{"alice"}, query, "gitlab", "", "", "", 10)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if len(result.Hits) == 0 || result.Hits[0].Path != "internal/settlement/handler.go" {
			t.Fatalf("%q found %#v (full text: %v)", query, result.Hits, db.FullTextAvailable())
		}
	}
	// A term that appears nowhere must stay empty on either path.
	absent, err := service.SearchCode(ctx, []string{"alice"}, "kubernetes", "gitlab", "", "", "", 10)
	if err == nil && len(absent.Hits) != 0 {
		t.Fatalf("unrelated term matched: %#v", absent.Hits)
	}
	// The ACL applies to the lookup exactly as it does to the scan.
	denied, err := service.SearchCode(ctx, []string{"mallory"}, "settleInvoice", "gitlab", "", "", "", 10)
	if err == nil && len(denied.Hits) != 0 {
		t.Fatalf("the lookup leaked past the ACL: %#v", denied.Hits)
	}
}

// Every path that filters chunks by words has to give the same answer whether
// it reaches the index or scans, including the fallback that answers
// search-semantic when embeddings are off — which on an on-premises install is
// the usual case, not the exception.
func TestSemanticFallbackAndDocsFindTheSameContentThroughEitherPath(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:lexical-parity?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('gitlab:1','main','abc')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES
('c1','gitlab:1','main','abc','docs/settlement.md',1,20,'Settlement runbook','document','정산 배치는 settleInvoice 를 호출한다.','h1'),
('c2','gitlab:1','main','abc','docs/cache.md',1,20,'Cache','document','캐시는 warmCache 로 채운다.','h2')`)

	service := New(db)
	for _, query := range []string{"settleInvoice", "settlement", "settle", "invoice", "정산"} {
		semantic, err := service.SemanticSearch(ctx, []string{"alice"}, query, "", "", 10)
		if err != nil {
			t.Fatalf("semantic %q: %v", query, err)
		}
		if len(semantic.Hits) == 0 || semantic.Hits[0].FilePath != "docs/settlement.md" {
			t.Fatalf("semantic %q found %#v (full text: %v)", query, semantic.Hits, db.FullTextAvailable())
		}
	}
	// query-docs ranks whole words; a fragment has never matched there and this
	// change does not alter that. What must hold is that the words it did match
	// still match now that the candidates come from the index.
	for _, query := range []string{"settleInvoice", "settlement", "정산"} {
		docs, err := service.Query(ctx, []string{"alice"}, "/core/api", query)
		if err != nil {
			t.Fatalf("docs %q: %v", query, err)
		}
		if !strings.Contains(docs, "settlement") {
			t.Fatalf("docs %q missed the file:\n%s", query, docs)
		}
	}
	// A term that appears nowhere stays empty on either path.
	absent, err := service.SemanticSearch(ctx, []string{"alice"}, "kubernetes", "", "", 10)
	if err == nil && len(absent.Hits) != 0 {
		t.Fatalf("unrelated term matched: %#v", absent.Hits)
	}
	// The ACL applies to the lookup as it does to the scan.
	denied, err := service.SemanticSearch(ctx, []string{"mallory"}, "settleInvoice", "", "", 10)
	if err == nil && len(denied.Hits) != 0 {
		t.Fatalf("the lookup leaked past the ACL: %#v", denied.Hits)
	}
}

// SQLite is opened with a single connection, so a second query issued while a
// rows cursor is still open waits for a connection this goroutine is itself
// holding. It ends only when the statement times out — fifteen seconds per call
// on a real instance, with the configuration it was trying to read silently
// falling back to defaults. The store here uses one connection so the stall is
// reproduced rather than hidden.
func TestExplainSearchDoesNotQueryWhileHoldingItsCursor(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:explain-single-conn?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	db.DB.SetMaxOpenConns(1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','gitlab:1','main','abc','internal/payment.go',1,20,'Payment','code','func retryPayment() error { return nil }','h1')`)

	service := New(db)
	// A configuration loader that reads the database is what the running server
	// installs; the default in-memory one would not reproduce the stall.
	service.SetConfigLoader(func(ctx context.Context) Config {
		var count int
		if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories`).Scan(&count); err != nil {
			t.Errorf("the configuration load was starved of a connection: %v", err)
		}
		return Config{KeywordWeight: 1, RetrievalMode: RetrievalKeywordOnly, FinalK: 8, CandidateLimit: 100}
	})

	done := make(chan error, 1)
	go func() {
		explanation, err := service.ExplainSearch(ctx, []string{"alice"}, "/core/api", "main", "payment", 10)
		if err == nil && len(explanation.Hits) == 0 {
			err = errors.New("no candidates were explained")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExplainSearch stalled: it is querying while its own cursor is open")
	}
}

// Runbooks are found by markers in the path or heading. The index knows them as
// words; the substring scan also catches one buried inside a longer word. Both
// have to keep working, because an operator looking for the runbook during an
// incident does not get a second try.
func TestRunbookLookupKeepsBothWaysOfMatchingAMarker(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:runbook-markers?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES
('c1','gitlab:1','main','abc','docs/runbooks/payment.md',1,20,'Payment restart','document','결제 배치가 멈추면 컨슈머를 재시작한다.','h1'),
('c2','gitlab:1','main','abc','docs/oncall.md',1,20,'Runbook: cache','document','캐시 워머를 재시작한다.','h2'),
('c3','gitlab:1','main','abc','docs/team.md',1,20,'Team','document','팀 소개.','h3')`)

	service := New(db)
	// A marker as its own word in the path, and as a word in a heading.
	for _, query := range []string{"결제", "캐시"} {
		found, err := service.FindRunbooks(ctx, []string{"alice"}, "", query, 10)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if len(found) == 0 {
			t.Fatalf("%q found no runbook (full text: %v)", query, db.FullTextAvailable())
		}
	}
	// A marker inside a longer word: only the substring form sees this, and it
	// must still run when the index comes back empty.
	exec(`DELETE FROM document_chunks WHERE id IN ('c1','c2')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c4','gitlab:1','main','abc','docs/myrunbooks.md',1,20,'Restart','document','결제 컨슈머를 재시작한다.','h4')`)
	buried, err := service.FindRunbooks(ctx, []string{"alice"}, "", "결제", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(buried) != 1 || buried[0].FilePath != "docs/myrunbooks.md" {
		t.Fatalf("a marker inside a word was lost: %#v", buried)
	}
	// A document with no marker at all stays out, whichever path ran.
	if plain, err := service.FindRunbooks(ctx, []string{"alice"}, "", "팀", 10); err != nil || len(plain) != 0 {
		t.Fatalf("a non-runbook matched: %#v err=%v", plain, err)
	}
	// The ACL applies to both paths.
	if denied, err := service.FindRunbooks(ctx, []string{"mallory"}, "", "결제", 10); err != nil || len(denied) != 0 {
		t.Fatalf("runbook lookup leaked past the ACL: %#v err=%v", denied, err)
	}
}

// When embeddings cannot be used and the index has nothing, the caller is given
// an error — that is right, because "we could not look" must never be reported
// as "nothing exists". What the error says matters: naming only the last
// failure hands an agent a message about the source server while hiding that
// the embeddings were unavailable and that the index was already searched.
func TestSemanticFailureNamesEveryPathThatWasTried(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:semantic-failure?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('c1','gitlab:1','main','abc','internal/payment.go',1,20,'Payment','code','func retryPayment() error { return nil }','h1')`)

	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		// Embeddings are configured but the provider is unreachable, which is
		// what an outage looks like from here.
		return Config{KeywordWeight: 1, VectorWeight: 0.35, FinalK: 8, CandidateLimit: 100,
			RetrievalMode: RetrievalHybridFallback, MinimumEmbeddingCoverage: 80}
	})
	service.SetEmbeddingLoader(func(context.Context) (embedding.Provider, error) {
		return nil, errors.New("embedding endpoint refused the connection")
	})
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("gitlab returned an unreadable response")
	})

	// A term the index does not hold, so every path comes back empty.
	_, err = service.SemanticSearch(ctx, []string{"alice"}, "kubernetes operator scheduling", "", "", 10)
	if err == nil {
		t.Fatal("an answer that could not be produced must not be reported as an empty one")
	}
	message := err.Error()
	for _, expected := range []string{"embeddings were not used", "indexed content had no match", "live source query then failed"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("the error hides a path that was tried: %q", message)
		}
	}
	if !strings.Contains(message, "gitlab returned an unreadable response") {
		t.Fatalf("the underlying source failure must still be readable: %q", message)
	}

	// With something in the index, the same outage returns the indexed answer
	// rather than an error, and says why the live path did not run.
	found, err := service.SemanticSearch(ctx, []string{"alice"}, "retryPayment", "", "", 10)
	if err != nil {
		t.Fatalf("the index could answer this: %v", err)
	}
	if len(found.Hits) == 0 {
		t.Fatalf("indexed hits were discarded: %#v", found.Diagnostics)
	}
	if !strings.Contains(strings.Join(found.Diagnostics, " "), "source search:") {
		t.Fatalf("the failed live path must be reported: %v", found.Diagnostics)
	}
}

// The order of an answer is part of the answer. A configured reranker that
// silently failed left the caller reading vector order while believing it had
// been reranked — and nothing in the answer, the diagnostics or the platform
// status said otherwise.
func TestRerankingSaysWhetherItRan(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:rerank-visibility?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('gitlab:1','main','abc')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES
('c1','gitlab:1','main','abc','docs/payment.md',1,20,'Payment','document','결제 재시도 배치','h1'),
('c2','gitlab:1','main','abc','docs/cache.md',1,20,'Cache','document','결제 캐시 워머 재시작','h2')`)

	service := New(db)
	service.SetConfigLoader(func(context.Context) Config {
		return Config{KeywordWeight: 1, VectorWeight: 0, FinalK: 8, CandidateLimit: 100, RerankLimit: 5,
			RetrievalMode: RetrievalKeywordOnly}
	})

	// A reranker that fails must not fail the search, and must not be silent.
	service.SetRerankerLoader(func(context.Context) rerank.Provider {
		return rerankFunc(func(context.Context, string, []string) ([]float64, error) {
			return nil, errors.New("reranker endpoint refused the connection")
		})
	})
	failed, err := service.Query(ctx, []string{"alice"}, "/core/api", "결제")
	if err != nil {
		t.Fatalf("a failing reranker must not fail the search: %v", err)
	}
	if !strings.Contains(failed, "재순위 모델을 호출하지 못해") || !strings.Contains(failed, "refused the connection") {
		t.Fatalf("a failed reranking must be reported:\n%s", failed)
	}

	// One that answers must say that the order is its own.
	service.SetRerankerLoader(func(context.Context) rerank.Provider {
		return rerankFunc(func(_ context.Context, _ string, documents []string) ([]float64, error) {
			scores := make([]float64, len(documents))
			for index := range documents {
				scores[index] = float64(len(documents) - index)
			}
			return scores, nil
		})
	})
	ranked, err := service.Query(ctx, []string{"alice"}, "/core/api", "결제")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ranked, "재순위 모델 점수로 정렬") {
		t.Fatalf("a successful reranking must be reported:\n%s", ranked)
	}

	// A provider that answers with the wrong number of scores is a silent
	// corruption of the order if it is trusted; it must be refused and said.
	service.SetRerankerLoader(func(context.Context) rerank.Provider {
		return rerankFunc(func(context.Context, string, []string) ([]float64, error) {
			return []float64{1}, nil
		})
	})
	mismatched, err := service.Query(ctx, []string{"alice"}, "/core/api", "결제")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mismatched, "점수를 돌려줘 순서를 바꾸지 않았습니다") {
		t.Fatalf("a mismatched reranking must be reported:\n%s", mismatched)
	}
}

type rerankFunc func(context.Context, string, []string) ([]float64, error)

func (f rerankFunc) Rerank(ctx context.Context, query string, documents []string) ([]float64, error) {
	return f(ctx, query, documents)
}

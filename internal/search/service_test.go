package search

import (
	"context"
	"errors"
	"fmt"
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

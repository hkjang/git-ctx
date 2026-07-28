package search

import (
	"context"
	"errors"
	"strings"
	"testing"

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

type querySource struct {
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
	q.calls++
	q.lastQuery = query
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

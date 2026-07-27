package search

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/embedding"
	"git-ctx/internal/rerank"
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

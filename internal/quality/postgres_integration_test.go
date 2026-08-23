package quality

import (
	"context"
	"os"
	"testing"

	"git-ctx/internal/store"
	"git-ctx/internal/testsupport"
)

func TestPostgresQualityBenchmarkIntegration(t *testing.T) {
	base := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if reason := testsupport.SkipReason("GIT_CTX_TEST_POSTGRES_DSN", base); reason != "" {
		t.Skip(reason)
	}
	dsn, dropDatabase, err := testsupport.NewPostgresDatabase(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dropDatabase)
	ctx := context.Background()
	db, err := store.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if _, err = db.DB.Exec(`DELETE FROM quality_benchmark_results; DELETE FROM quality_benchmark_runs; DELETE FROM quality_benchmark_cases`); err != nil {
		t.Fatal(err)
	}
	service := New(db, fakeSearch{text: "Source: `gitlab://pg/demo@abc/docs/postgres.md#L1-L4`"})
	created, err := service.CreateCase(ctx, Case{Name: "PostgreSQL contract", LibraryID: "/pg/demo", Query: "contract", Principals: []string{"integration"}, RelevantSources: []string{"docs/postgres.md"}}, "integration")
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Run(ctx, "integration", 5, Thresholds{Recall: 1, MRR: 1, NDCG: 1})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "passed" || run.RecallAtK != 1 || run.MRR != 1 || run.NDCGAtK != 1 {
		t.Fatalf("run=%#v", run)
	}
	results, err := service.Results(ctx, run.ID)
	if err != nil || len(results) != 1 || results[0].CaseID != created.ID {
		t.Fatalf("results=%#v err=%v", results, err)
	}
}

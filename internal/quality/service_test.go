package quality

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"git-ctx/internal/store"
)

type fakeSearch struct {
	text string
	err  error
}

func (f fakeSearch) Query(context.Context, []string, string, string) (string, error) {
	return f.text, f.err
}

func fixture(t *testing.T, search fakeSearch) *Service {
	t.Helper()
	db, err := store.Open(context.Background(), "sqlite", "file:"+filepath.Join(t.TempDir(), "quality.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return New(db, search)
}

func TestMetrics(t *testing.T) {
	recall, rr, ndcg := metrics([]string{"noise.md", "b.md", "a.md"}, []string{"a.md", "b.md"}, 3)
	if recall != 1 || rr != .5 || ndcg <= 0 || ndcg >= 1 {
		t.Fatalf("recall=%v rr=%v ndcg=%v", recall, rr, ndcg)
	}
}

func TestBenchmarkLifecycleAndRegression(t *testing.T) {
	service := fixture(t, fakeSearch{text: "Source: `bitbucket://kcb/demo@abc/noise.md#L1-L2`\n\nSource: `bitbucket://kcb/demo@abc/docs/right.md#L3-L9`"})
	ctx := context.Background()
	created, err := service.CreateCase(ctx, Case{Name: "right answer", LibraryID: "/kcb/demo/main", Query: "how", Principals: []string{"alice"}, RelevantSources: []string{"docs/right.md"}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || !created.Enabled {
		t.Fatalf("created=%#v", created)
	}
	run, err := service.Run(ctx, "admin", 2, Thresholds{Recall: .9, MRR: .75, NDCG: .5})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "regressed" || run.RecallAtK != 1 || run.MRR != .5 || run.PassedCount != 0 {
		t.Fatalf("run=%#v", run)
	}
	results, err := service.Results(ctx, run.ID)
	if err != nil || len(results) != 1 || len(results[0].RetrievedSources) != 2 {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	runs, err := service.ListRuns(ctx)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
	if err = service.DeleteCase(ctx, created.ID); err == nil {
		t.Fatal("case referenced by a run was deleted")
	}
}

func TestCaseValidationAndSearchFailure(t *testing.T) {
	service := fixture(t, fakeSearch{err: errors.New("offline")})
	ctx := context.Background()
	if _, err := service.CreateCase(ctx, Case{Name: "bad", LibraryID: "bad", Query: "q"}, "admin"); err == nil {
		t.Fatal("invalid case accepted")
	}
	_, err := service.CreateCase(ctx, Case{Name: "failure", LibraryID: "/kcb/demo", Query: "q", Principals: []string{"alice"}, RelevantSources: []string{"README.md"}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Run(ctx, "admin", 5, Thresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "regressed" || run.PassedCount != 0 {
		t.Fatalf("run=%#v", run)
	}
	results, err := service.Results(ctx, run.ID)
	if err != nil || results[0].ErrorMessage == "" {
		t.Fatalf("results=%#v err=%v", results, err)
	}
}

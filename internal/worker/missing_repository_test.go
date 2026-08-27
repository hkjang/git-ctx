package worker

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/indexer"
	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

type movedSource struct{ fakeSource }

func (movedSource) ListBranches(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, &source.APIError{Source: "bitbucket", StatusCode: 404, Status: "404 Not Found",
		Body: `{"errors":[{"message":"Repository KCB/old-name does not exist."}]}`}
}

type brokenSource struct{ fakeSource }

func (brokenSource) ListBranches(context.Context, source.RepositoryRef) ([]source.Reference, error) {
	return nil, &source.APIError{Source: "bitbucket", StatusCode: 500, Status: "500 Internal Server Error", Body: "gateway"}
}

// A repository that is not at its stored path answered with the source's own
// wording — "Repository KCB/old-name does not exist" — passed through as if it
// were a fact. It is not one. Bitbucket and GitLab both answer 404 for a
// repository the service account may not see, precisely so an unauthorised
// caller cannot tell absence from denial; neither can the operator reading it.
// Renamed, deleted and access-withdrawn need three different actions.
//
// The platform also knows something the source's message cannot: the entry is
// keyed by the source id, so registering the repository again under its current
// name updates it in place instead of leaving a second copy.
func TestARepositoryMissingFromItsPathSaysWhatThatCanMean(t *testing.T) {
	message := indexOnce(t, "missing-path", movedSource{})
	for _, want := range []string{"renamed", "deleted", "access withdrawn", "404, not 403", "bitbucket id 42", "Register it again"} {
		if !strings.Contains(message, want) {
			t.Errorf("the failure does not mention %q:\n%s", want, message)
		}
	}
	// The console cuts this to 300 characters. What survives has to be the part
	// that says what to do, and to what.
	visible := message
	if len(visible) > 300 {
		visible = visible[:300]
	}
	for _, want := range []string{"bitbucket id 42", "Register it again under its current name"} {
		if !strings.Contains(visible, want) {
			t.Errorf("%q is past the 300 characters an operator is shown:\n%s", want, visible)
		}
	}
	// The source's own words are kept, after the part that is useful.
	if !strings.Contains(message, "does not exist") {
		t.Errorf("the underlying source error was dropped:\n%s", message)
	}
}

// A server that broke is not a repository that moved, and must not be dressed
// up as one.
func TestAFailingSourceIsNotReportedAsAMissingRepository(t *testing.T) {
	message := indexOnce(t, "broken-source", brokenSource{})
	if strings.Contains(message, "renamed") || strings.Contains(message, "Register it again") {
		t.Errorf("a 500 was reported as a missing repository:\n%s", message)
	}
	if !strings.Contains(message, "500") {
		t.Errorf("the server failure was lost:\n%s", message)
	}
}

// indexOnce runs one job against a source and returns what the operator is
// shown for it.
func indexOnce(t *testing.T, name string, adapter source.RepositorySource) string {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:"+name+"?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	if _, err = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('bitbucket:42','KCB','old-name','Old','bitbucket','42','/kcb/old-name','main')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`INSERT INTO index_jobs(id,repository_id,ref_name,kind,status) VALUES('job','bitbucket:42','main','webhook','pending')`); err != nil {
		t.Fatal(err)
	}
	w := New(db, indexer.New(db, indexer.DefaultPolicy()), func(context.Context, string) (source.RepositorySource, error) {
		return adapter, nil
	})
	if ok, _ := w.RunOnce(ctx); !ok {
		t.Fatal("no job was claimed")
	}
	var message string
	if err = db.DB.QueryRow(`SELECT error_message FROM index_jobs WHERE id='job'`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

package search

import (
	"context"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/store"
)

func TestIndexAgesReportsPerRefAndRespectsTheACL(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:freshness?mode=memory&cache=shared&_foreign_keys=on")
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
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('fresh','core','a','A','gitlab','1','/core/a','main')`)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('stale','core','b','B','gitlab','2','/core/b','main')`)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('hidden','core','c','C','gitlab','3','/core/c','main')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('fresh','alice','read')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('stale','alice','read')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('hidden','bob','read')`)
	exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at) VALUES('fresh','main','aaa',?)`, now.Add(-2*time.Hour))
	exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at) VALUES('stale','main','bbb',?)`, now.AddDate(0, 0, -45))
	exec(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id,indexed_at) VALUES('hidden','main','ccc',?)`, now.AddDate(0, 0, -90))

	service := New(db)
	ages, err := service.IndexAges(ctx, []string{"alice"}, []string{"/core/a", "/core/b", "/core/c"}, now)
	if err != nil {
		t.Fatalf("IndexAges: %v", err)
	}
	byLibrary := map[string]IndexAge{}
	for _, age := range ages {
		byLibrary[age.LibraryID] = age
	}
	// A repository alice cannot read must not appear, however stale it is.
	if _, present := byLibrary["/core/c"]; present {
		t.Errorf("a repository outside the ACL was reported: %#v", ages)
	}
	if len(ages) != 2 {
		t.Fatalf("ages = %#v, want the two readable repositories", ages)
	}
	// Oldest first, so the worst case is what a reader sees.
	if ages[0].LibraryID != "/core/b" {
		t.Errorf("ordering = %s first, want the stalest", ages[0].LibraryID)
	}
	if byLibrary["/core/b"].Age < 44*24*time.Hour {
		t.Errorf("age = %v, want about 45 days", byLibrary["/core/b"].Age)
	}
	if byLibrary["/core/b"].CommitID != "bbb" {
		t.Errorf("the indexed commit was not reported: %#v", byLibrary["/core/b"])
	}

	// A caller with no principals gets nothing rather than everything.
	if ages, err := service.IndexAges(ctx, nil, []string{"/core/a"}, now); err != nil || len(ages) != 0 {
		t.Errorf("ages=%#v err=%v, want none without principals", ages, err)
	}
}

// The note exists to interrupt a reader who would otherwise treat the answer as
// current, so it must stay quiet when there is nothing to interrupt for.
func TestFreshnessNoteOnlyFiresWhenTheIndexIsActuallyOld(t *testing.T) {
	recent := []IndexAge{{LibraryID: "/core/a", Age: 6 * 24 * time.Hour}}
	if note := FreshnessNote(recent); note != "" {
		t.Errorf("a six day old index produced a warning: %q", note)
	}

	old := []IndexAge{
		{LibraryID: "/core/a", Age: 45 * 24 * time.Hour},
		{LibraryID: "/core/b", Age: 8 * 24 * time.Hour},
	}
	note := FreshnessNote(old)
	if !strings.Contains(note, "/core/a") || !strings.Contains(note, "1개월") {
		t.Errorf("note = %q, want it to name the stale repository and its age", note)
	}
	if !strings.Contains(note, "이후 변경은 포함되지 않았습니다") {
		t.Errorf("note = %q, want it to say what the staleness means", note)
	}

	// A long list is summarised rather than printed in full.
	many := make([]IndexAge, 10)
	for i := range many {
		many[i] = IndexAge{LibraryID: "/core/x", Age: 30 * 24 * time.Hour}
	}
	if summary := FreshnessNote(many); !strings.Contains(summary, "외 7곳") {
		t.Errorf("note = %q, want the tail summarised", summary)
	}
}

func TestHumanDaysSwitchesToMonths(t *testing.T) {
	for _, c := range []struct {
		age  time.Duration
		want string
	}{
		{3 * 24 * time.Hour, "3일"},
		{29 * 24 * time.Hour, "29일"},
		{60 * 24 * time.Hour, "2개월"},
	} {
		if got := humanDays(c.age); got != c.want {
			t.Errorf("humanDays(%v) = %q, want %q", c.age, got, c.want)
		}
	}
}

package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

// "No file contents matched. This is not the same as 'the code does not exist':
// check the notes below." — and there were no notes.
//
// A query made only of punctuation has no word the index stores, so the index
// was never asked; and every caveat from the indexed path was attached to the
// sentence about how many matches it contributed, so when it contributed none
// the explanation went with them. Searching for "c++" in a repository holding
// C++ therefore reported nothing, twice over: no results, and no reason.
func TestAQueryTheIndexCannotExpressIsSearchedAndSaidSo(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:unsearchable?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if !db.FullTextAvailable() {
		t.Skip("this build has no full-text index")
	}
	seedAgreementCorpus(t, ctx, db)
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) {
		return nil, errors.New("the platform API is unavailable")
	})
	search := func(query string) (paths []string, note string) {
		t.Helper()
		result, err := service.SearchCode(ctx, []string{"alice"}, query, "gitlab", "", "", "", 10)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		for _, hit := range result.Hits {
			paths = append(paths, hit.Path)
		}
		for _, diagnostic := range result.Diagnostics {
			if strings.HasPrefix(diagnostic, "index:") {
				note = diagnostic
			}
		}
		return paths, note
	}

	// The text is in the corpus, so the scan can find it even though the index
	// cannot be asked for it.
	paths, note := search("c++")
	if len(paths) != 1 || paths[0] != "src/native/parser.cpp" {
		t.Errorf(`searching for "c++" returned %v; the text is in src/native/parser.cpp`, paths)
	}
	if !strings.Contains(note, "no word the index can look up") {
		t.Errorf("the answer does not say the index could not be asked: %q", note)
	}

	// Finding nothing is where the explanation matters most, and where it used
	// to be dropped.
	paths, note = search("a&b")
	if len(paths) != 0 {
		t.Errorf(`"a&b" matched %v`, paths)
	}
	if !strings.Contains(note, "no word the index can look up") {
		t.Errorf("an empty answer carries no note at all: %q", note)
	}

	// Punctuation the tokenizer keeps rather than drops reaches the same place
	// by a different route, and has to be reported the same way.
	if _, note = search("--"); !strings.Contains(note, "no word the index can look up") {
		t.Errorf("a query of punctuation the tokenizer keeps is not reported: %q", note)
	}

	// Too short to search for is a third fact, and is not the same as either.
	if _, note = search("x"); !strings.Contains(note, "too short") {
		t.Errorf("a single-character query is not reported as such: %q", note)
	}

	// An ordinary query keeps its ordinary answer: none of this appears when the
	// index was asked and answered.
	paths, note = search("settleInvoice")
	if len(paths) == 0 {
		t.Fatal("the ordinary case stopped working")
	}
	if strings.Contains(note, "no word the index can look up") {
		t.Errorf("a query the index answered was reported as unaskable: %q", note)
	}
}

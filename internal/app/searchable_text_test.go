package app

import (
	"fmt"
	"strings"
	"testing"
)

// Text this platform stores and then searches with nothing.
//
// The full-text index covers the file path, the heading and the content, and
// the scan clause it falls back to reads all three — and the step that scored
// the rows read only the path and the content. A chunk the index had found by
// its heading scored zero and was thrown away, so a word that appears only in a
// heading found nothing. "## Rollback procedure", "## Settlement": a heading is
// what a document is about.
//
// The parser extracts the comment above a function, the indexer stores it and
// find-symbol prints it, and nothing looked in it. The chunk began at the
// declaration, so the comment was not in the index either. A comment above a
// function is usually the only prose about it in the repository, and it is what
// somebody searching for behaviour rather than for a name types.

const searchableREADME = "# Payment API\n\n결제 서비스.\n\n## Settlement\n\n정산 배치는 매일 02:00 에 돈다.\n"
const searchableGo = "package settlement\n\n// Handle reconciles one order against the ledger.\nfunc Handle() error {\n\treturn nil\n}\n"

func searchableFixture() map[string]string {
	return map[string]string{"README.md": searchableREADME, "internal/a.go": searchableGo}
}

func sourceMatchCount(t *testing.T, a *App, query string) int {
	t.Helper()
	answer := toolAnswer(t, a, "search-code", fmt.Sprintf(`{"query":%q}`, query))
	for _, line := range strings.Split(answer, "\n") {
		if !strings.HasPrefix(line, "### Source Matches (") {
			continue
		}
		var count int
		if _, err := fmt.Sscanf(line, "### Source Matches (%d)", &count); err == nil {
			return count
		}
	}
	return 0
}

func TestAWordThatOnlyAppearsInAHeadingIsFound(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(searchableFixture())
	defer source.Close()
	a := indexedApp(t, source.URL, model.URL, "headings")

	// "Settlement" is a Markdown heading and nothing else in this repository:
	// the chunker lifts heading lines out of the content into a column of their
	// own, so the body of that section does not contain the word.
	var stored int
	if err := a.store.DB.QueryRow(a.store.Rebind(
		`SELECT COUNT(*) FROM document_chunks WHERE file_path='README.md' AND LOWER(heading)='settlement' AND LOWER(content) NOT LIKE '%settlement%'`)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("the fixture no longer has a heading-only word: %d chunks", stored)
	}

	for _, query := range []string{"Settlement", "settlement"} {
		if found := sourceMatchCount(t, a, query); found == 0 {
			t.Errorf("%q is a heading in the corpus and search-code found nothing", query)
		}
	}
	// The snippet has to show the heading, or the answer carries no occurrence
	// of what was searched for.
	answer := toolAnswer(t, a, "search-code", `{"query":"Settlement"}`)
	if !strings.Contains(answer, "Settlement\n정산") {
		t.Errorf("a section matched by its heading does not show the heading:\n%s", answer)
	}
}

func TestAWordThatOnlyAppearsInADocCommentIsFound(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(searchableFixture())
	defer source.Close()
	a := indexedApp(t, source.URL, model.URL, "doccomments")

	// The chunk starts at the comment, not at the declaration, and its line
	// number moves with it so the range still describes the text.
	var start int
	var content string
	if err := a.store.DB.QueryRow(a.store.Rebind(
		`SELECT line_start,content FROM document_chunks WHERE file_path='internal/a.go' AND heading='Handle'`)).Scan(&start, &content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "reconciles") {
		t.Fatalf("the doc comment is not in the indexed chunk: %q", content)
	}
	if start != 3 {
		t.Fatalf("the chunk claims to start at line %d; the comment is at line 3", start)
	}

	if found := sourceMatchCount(t, a, "reconciles"); found == 0 {
		t.Error("a word from a doc comment found nothing in search-code")
	}
	symbols := toolAnswer(t, a, "find-symbol", `{"libraryId":"/gitlab~core/api","query":"reconciles"}`)
	if !strings.Contains(symbols, "Handle") {
		t.Errorf("find-symbol prints the documentation it stores and does not search it:\n%s", symbols)
	}
}

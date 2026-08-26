package app

import (
	"strings"
	"testing"
	"time"
)

// An answer that does not carry its own age is read as current.
//
// This platform knows exactly how old the index behind every answer is — it has
// a tool that reports nothing else — and told four of its tools to say so.
// read-file returned a month-old function body and said only that it came from
// the index; find-symbol, query-docs, list-directory and the rest said nothing
// at all. An agent reads that and reports the code as it stands today.

// contentAnsweringTools return indexed content, or a fact read out of it.
var contentAnsweringTools = []struct{ tool, arguments string }{
	{"read-file", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
	{"query-docs", `{"libraryId":"/gitlab~core/api","query":"how is settlement retried"}`},
	{"find-symbol", `{"libraryId":"/gitlab~core/api","query":"settle"}`},
	{"get-symbol-context", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
	{"find-tests", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
	{"trace-dependencies", `{"libraryId":"/gitlab~core/api","symbol":"settleInvoice"}`},
	{"list-directory", `{"libraryId":"/gitlab~core/api","path":"internal"}`},
	{"get-repository-map", `{"libraryId":"/gitlab~core/api"}`},
	{"explain-search-result", `{"libraryId":"/gitlab~core/api","query":"settlement"}`},
	{"get-file-history", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
}

func ageTheIndex(t *testing.T, a *App, by time.Duration) {
	t.Helper()
	when := time.Now().UTC().Add(-by)
	for _, statement := range []string{
		`UPDATE document_chunks SET indexed_at=?`,
		`UPDATE repository_ref_states SET indexed_at=?`,
		`UPDATE repositories SET indexed_at=?`,
		`UPDATE code_symbols SET indexed_at=?`,
	} {
		if _, err := a.store.DB.Exec(a.store.Rebind(statement), when); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func TestAnAnswerFromAnOldIndexSaysHowOldItIs(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(agreementFixture())
	defer source.Close()
	a := indexedApp(t, source.URL, model.URL, "stale")

	// Every tool answers without a word about age while the index is current.
	for _, ask := range contentAnsweringTools {
		answer := toolAnswer(t, a, ask.tool, ask.arguments)
		if strings.Contains(answer, "색인이 오래됐습니다") {
			t.Errorf("%s calls a fresh index stale: %s", ask.tool, first(answer))
		}
	}

	// A repository whose webhook stopped firing a month ago.
	ageTheIndex(t, a, 30*24*time.Hour)
	for _, ask := range contentAnsweringTools {
		answer := toolAnswer(t, a, ask.tool, ask.arguments)
		if !strings.Contains(answer, "색인이 오래됐습니다") {
			t.Errorf("%s hands back month-old content without saying so: %s", ask.tool, first(answer))
		}
	}
}

// The audit trail has to record which repository a file was read out of. It did
// not for the two tools that hand back file contents and file history, because
// both were left out of the list of tools whose libraryId names a repository —
// the same omission that was fixed for find-symbol and find-runbook.
func TestReadingAFileRecordsWhichRepositoryItCameFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(agreementFixture())
	defer source.Close()
	a := indexedApp(t, source.URL, model.URL, "audited")

	for _, ask := range []struct{ tool, arguments string }{
		{"read-file", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
		{"get-file-history", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
	} {
		toolAnswer(t, a, ask.tool, ask.arguments)
		var recorded string
		if err := a.store.DB.QueryRow(a.store.Rebind(
			`SELECT library_id FROM mcp_calls WHERE tool=? ORDER BY id DESC LIMIT 1`), ask.tool).Scan(&recorded); err != nil {
			t.Fatalf("%s was not audited: %v", ask.tool, err)
		}
		if recorded != "/gitlab~core/api" {
			t.Errorf("%s audited the call without the repository it read: library_id=%q", ask.tool, recorded)
		}
	}
}

// Every tool that takes a libraryId has to record it. Four did not, so a call
// scoped to one repository was audited as if it had been asked of all of them,
// and the freshness note — which needs to know which index the answer came
// from — could not be attached either.
func TestEveryToolThatTakesALibraryRecordsIt(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(agreementFixture())
	defer source.Close()
	a := indexedApp(t, source.URL, model.URL, "libaudit")

	for _, ask := range []struct{ tool, arguments string }{
		{"read-file", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
		{"get-file-history", `{"libraryId":"/gitlab~core/api","path":"README.md"}`},
		{"list-directory", `{"libraryId":"/gitlab~core/api","path":"internal"}`},
		{"find-file", `{"libraryId":"/gitlab~core/api","pattern":"*.go"}`},
		{"search-semantic", `{"libraryId":"/gitlab~core/api","query":"settlement"}`},
		{"search-merge-requests", `{"libraryId":"/gitlab~core/api","query":"settlement"}`},
	} {
		toolAnswer(t, a, ask.tool, ask.arguments)
		var recorded string
		if err := a.store.DB.QueryRow(a.store.Rebind(
			`SELECT library_id FROM mcp_calls WHERE tool=? ORDER BY id DESC LIMIT 1`), ask.tool).Scan(&recorded); err != nil {
			t.Errorf("%s was not audited: %v", ask.tool, err)
			continue
		}
		if recorded != "/gitlab~core/api" {
			t.Errorf("%s audited a call scoped to one repository without naming it: library_id=%q", ask.tool, recorded)
		}
	}
}

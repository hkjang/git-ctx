package app

import (
	"strings"
	"testing"
)

// read-file has to return the file.
//
// The stored chunks are a search index, not a copy. The chunker emits one chunk
// per symbol for code, so a package clause, the imports and every comment
// outside a symbol are never stored; for Markdown it lifts the heading lines
// into a column of their own and drops the blank lines. Read back and joined, a
// five-line Go file came out as one line and a twelve-line README as three —
// and read-file presented that as the file, with line numbers to match. An
// agent asked to edit a file it had read that way would delete most of it.

const fidelityREADME = "# Payment API\n\n결제 서비스.\n\n## Settlement\n\n정산 배치는 매일 02:00 에 돈다.\n"
const fidelityGo = "package settlement\n\nimport \"errors\"\n\n// Handle reconciles one order.\nfunc Handle() error { return errors.New(\"no\") }\n"

func fidelityFixture() map[string]string {
	return map[string]string{
		"README.md":                      fidelityREADME,
		"internal/settlement/handler.go": fidelityGo,
	}
}

func TestReadFileReturnsTheWholeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(fidelityFixture())
	defer source.Close()
	a := indexedApp(t, source.URL, model.URL, "fidelity")

	for _, expected := range []struct{ path, missing string }{
		{"README.md", "# Payment API"},
		{"README.md", "## Settlement"},
		{"internal/settlement/handler.go", "package settlement"},
		{"internal/settlement/handler.go", "import \"errors\""},
		{"internal/settlement/handler.go", "// Handle reconciles one order."},
	} {
		answer := toolAnswer(t, a, "read-file", `{"libraryId":"/gitlab~core/api","path":"`+expected.path+`"}`)
		if !strings.Contains(answer, expected.missing) {
			t.Errorf("read-file dropped %q from %s:\n%s", expected.missing, expected.path, answer)
		}
	}
}

// When the source cannot be reached the index is still an answer — an
// on-premises platform has to keep working through an outage — but it must not
// be handed over as if it were the file.
func TestTheIndexFallbackSaysItIsNotTheWholeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(fidelityFixture())
	a := indexedApp(t, source.URL, model.URL, "fallback")
	source.Close()

	answer := toolAnswer(t, a, "read-file", `{"libraryId":"/gitlab~core/api","path":"internal/settlement/handler.go","startLine":1}`)
	if !strings.Contains(answer, "func Handle()") {
		t.Fatalf("the index did not answer while the source was unreachable:\n%s", answer)
	}
	for _, expected := range []string{
		"not the whole file",
		"line numbers are of the reassembled text",
		"connection refused",
	} {
		if !strings.Contains(answer, expected) {
			t.Errorf("the fallback does not say %q:\n%s", expected, answer)
		}
	}
}

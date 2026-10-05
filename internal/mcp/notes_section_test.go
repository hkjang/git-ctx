package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// clampResponse reserves the trailing `### Notes` section from the budget,
// because that is where a tool says which retrieval path ran and what the ACL
// removed. It found that section with the last occurrence of the line anywhere in
// the answer, which is not the same thing: a `### Notes` heading of the *content*
// being shown is also such a line, and so is a result whose own heading happens
// to be Notes.
//
// Nothing here is synthesized: every assertion reads the answer a real
// `tools/call` round trip produced through the real formatters.

// notesTemplateSymbol is a Go helper that returns a Markdown report template, so
// the `### Notes` heading inside it is content of the symbol body rather than a
// section of the answer. The lines carry no blank line between them, so the only
// boundary the body offers before the heading is a line break.
func notesTemplateSymbol() string {
	var b strings.Builder
	b.WriteString("func reportTemplate() string {\n")
	b.WriteString("\treturn `# GPU incident report\n")
	b.WriteString("## Timeline\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "- %02d:00 the exporter rejoins the pool and the metrics gap closes on its own\n", i)
	}
	b.WriteString("### Notes\n")
	b.WriteString("Record the DCGM exporter version here.\n")
	b.WriteString("`\n}")
	return b.String()
}

// get-symbol-context has no Notes section of its own: formatSymbolContext writes
// the citation, the documentation and the fenced body, and stops. A `### Notes`
// heading inside that fenced body was reserved as if it were the answer's Notes,
// so the cut kept it and everything after it — the rest of the body and the
// closing fence — and moved it past the truncation notice. The closing fence
// landed outside the block it was supposed to close and opened one that never
// closes, which is exactly what the answer's fence rules exist to prevent.
func TestSymbolContextTruncationKeepsAFencedNotesHeadingInsideTheContent(t *testing.T) {
	const budget = 2000
	s := fixture(t)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	body := notesTemplateSymbol()
	must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash)
		VALUES('sn1','r1','main','4fa21bd','report/template.go','reportTemplate','reportTemplate','function','go','func reportTemplate() string','Returns the incident report template.',1,46,'snh1')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
		VALUES('cn1','r1','main','4fa21bd','report/template.go',1,46,'reportTemplate','code',?,'cnh1')`, body)

	text := answerText(t, s, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-symbol-context","arguments":{"libraryId":"/kcb/clustara","symbol":"reportTemplate","ref":"main","maxBytes":%d}}}`, budget))
	if !strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to exceed the budget:\n%s", text)
	}
	if blocks, unterminated := markdownBlocks(text); unterminated {
		t.Errorf("the answer ends inside a code fence that never closes, so everything after it is read as source (%d blocks):\n%s", len(blocks), text)
	}
	// The truncation notice is the end of a get-symbol-context answer: there is no
	// Notes section to come after it.
	if tail := afterTruncationNotice(text); strings.Contains(tail, "\n### ") {
		t.Errorf("the symbol body was moved past the truncation notice as if it were the tool's Notes:\n%s", tail)
	}
}

// afterTruncationNotice is everything the answer carries past the truncation
// notice. For a tool with a Notes section that is the Notes block; for a tool
// without one it has to be nothing at all.
func afterTruncationNotice(text string) string {
	const heading = "\n\n### Truncated\n"
	at := strings.Index(text, heading)
	if at < 0 {
		return ""
	}
	return text[at+len(heading):]
}

// storeRunbook makes one runbook section findable through find-runbook. The path
// decides the order find-runbook returns equally scored sections in.
func storeRunbook(t *testing.T, s *Server, id, path, heading, content string) {
	t.Helper()
	if _, err := s.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'r1','main','4fa21bd',?,1,20,?,'document',?,?)`,
		id, path, heading, content, id+"h"); err != nil {
		t.Fatal(err)
	}
}

// findRunbooks returns the answer text of one find-runbook call.
func findRunbooks(t *testing.T, s *Server, budget int) string {
	t.Helper()
	return answerText(t, s, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find-runbook","arguments":{"query":"runbook","limit":20,"maxBytes":%d}}}`, budget))
}

// formatRunbooks writes one `### ` heading per runbook section and no Notes
// section at all, so a document section titled Notes becomes a `### Notes` line
// of the answer. Reserving it as the tool's Notes moved it, and every result
// after it, past the truncation notice — and counted the results above it alone,
// so the notice reported a total smaller than the number of runbooks found.
func TestRunbookTruncationCountsASectionHeadedNotesAsAResult(t *testing.T) {
	const budget = 8000
	s := fixture(t)
	long := strings.Repeat("Drain the node pool before the restart so the runbook is followed in order.\n", 60)
	storeRunbook(t, s, "rb10", "docs/runbooks/10-drain.md", "Drain the node pool", long)
	storeRunbook(t, s, "rb20", "docs/runbooks/20-exporter.md", "Restart the exporter", long)
	storeRunbook(t, s, "rb30", "docs/runbooks/30-notes.md", "Notes", "This runbook set is reviewed every quarter.")
	storeRunbook(t, s, "rb40", "docs/runbooks/40-rollback.md", "Roll back", "Roll back with helm rollback.")

	full := findRunbooks(t, s, MaxResponseBytes)
	if strings.Contains(full, "### Truncated") {
		t.Fatalf("this call was supposed to fit in the budget:\n%s", full)
	}
	sections := strings.Count(full, "\n### ")
	if sections < 5 || !strings.Contains(full, "\n### Notes\n") {
		t.Fatalf("the fixture was supposed to return at least five runbooks, one of them headed Notes; got %d:\n%s", sections, full)
	}

	text := findRunbooks(t, s, budget)
	if !strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to exceed the budget:\n%s", text)
	}
	tail := afterTruncationNotice(text)
	if strings.Contains(tail, "\n### ") {
		t.Errorf("runbook sections were moved past the truncation notice as if they were the tool's Notes:\n%s", tail)
	}
	if want := fmt.Sprintf("of %d result sections", sections); !strings.Contains(text, want) {
		t.Errorf("the notice does not report %q, so the total it states excludes the runbooks from the section headed Notes on:\n%s", want, text[strings.Index(text, "\n\n### Truncated\n"):])
	}
}

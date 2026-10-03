package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// The budget layer reads the formatted answer back to find its result
// boundaries. Everything the formatters put inside a code fence is the *content
// being shown* rather than the structure of the answer, and closeOpenFence
// already knows where those fences are — sectionCount and cutAtBoundary did not,
// so a document whose own text uses `### ` headings or `- ` items had them
// counted as results and cut at.
//
// Nothing here is synthesized: every assertion reads the answer a real
// `tools/call` round trip produced through the real formatters.

// fencedHeadingFile is a document with exactly one `### ` heading of its own,
// placed where a cut at the twelve-thousand-byte budget can reach it: past the
// sixty percent the boundary rules require, and well short of the room the
// budget actually offers. Everything else is dense lines with no blank line
// between them, so the only boundary the file offers before the heading is a
// line break.
func fencedHeadingFile() string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	for i := 0; i < 93; i++ {
		line("Line %03d: the exporter rejoins the pool and the metrics gap closes on its own.\n", i)
	}
	line("### Appendix\n")
	line("Appendix body: this line is only delivered if the cut read past the heading.\n")
	for i := 0; i < 200; i++ {
		line("Tail %03d: the appendix continues well past the budget of this call.\n", i)
	}
	return b.String()
}

// storeDocument makes one document readable through read-file.
func storeDocument(t *testing.T, s *Server, path, content string) {
	t.Helper()
	if _, err := s.store.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','main',?,?,?,1,'4fa21bd')`,
		path, path, len(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('fenced1','r1','main','4fa21bd',?,1,600,'Runbook','document',?,'fencedh')`,
		path, content); err != nil {
		t.Fatal(err)
	}
}

func readDocument(t *testing.T, s *Server, path string, budget int) string {
	t.Helper()
	return answerText(t, s, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read-file","arguments":{"libraryId":"/kcb/clustara","path":%q,"ref":"main","maxBytes":%d}}}`, path, budget))
}

// A read-file answer is one file inside one fence. A `### ` heading in that file
// is not a boundary between results, and stopping there hands back a third of
// the budget less than was asked for.
func TestReadFileCutIgnoresHeadingsInsideTheFencedContent(t *testing.T) {
	const budget = 12000
	s := fixture(t)
	storeDocument(t, s, "docs/appendix.md", fencedHeadingFile())
	text := readDocument(t, s, "docs/appendix.md", budget)
	if !strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to exceed the budget:\n%s", text)
	}
	body := answerBody(text)
	if !strings.Contains(body, "### Appendix") || !strings.Contains(body, "Appendix body:") {
		t.Errorf("the cut stopped at the file's own `### ` heading; %d of %d budget bytes delivered:\n…%s",
			len(text), budget, body[max(0, len(body)-300):])
	}
	if len(text)*10 < budget*9 {
		t.Errorf("the answer is %d bytes of a %d byte budget; the cut preferred a heading of the content over the room it had",
			len(text), budget)
	}
}

// The audited result count of a read-file answer describes the answer, not the
// headings of the document it carries. The operator console reads
// mcp_calls.result_count straight out of the audit row.
func TestReadFileCountIgnoresHeadingsInsideTheFencedContent(t *testing.T) {
	s := fixture(t)
	storeDocument(t, s, "docs/appendix.md", fencedHeadingFile())
	text := readDocument(t, s, "docs/appendix.md", MaxResponseBytes)
	if strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to fit in the budget:\n%s", text)
	}
	if !strings.Contains(text, "\n### Appendix\n") {
		t.Fatalf("the file was supposed to arrive with its own `### ` heading:\n%s", text)
	}
	// The only `### ` heading of the answer itself is the Notes section the
	// formatter appends after the closing fence.
	if got := auditedResultCount(t, s, "read-file"); got != 1 {
		t.Errorf("mcp_calls.result_count=%d, want 1 — the answer's own sections are the `### Notes` block alone", got)
	}
}

// Control for the blast radius of the fence rule. formatCodeSearch writes its
// snippets as prose rather than inside a fence, so the hit region holds no fence
// of the answer's own. A chunk that ends mid-fence — the chunker splits a long
// document between the two halves of one — therefore leaves a backtick run that
// never closes, and the hits printed under it are still hits.
func TestSearchCodeCountsHitsPastASnippetThatOpensAFence(t *testing.T) {
	s := fixture(t)
	// A chunk cut off inside a fenced example: the opening run is stored, the
	// closing one belongs to the next chunk.
	fenced := "GPU metrics collection, GPU exporter, GPU scrape:\n\n```\ngpu: {exporter: dcgm}"
	if _, err := s.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('cfence','r1','main','4fa21bd','docs/gpu-fenced.md',1,8,'GPU exporter','document',?,'cfenceh')`, fenced); err != nil {
		t.Fatal(err)
	}
	padChunks(t, s, 6)
	text := answerText(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","limit":20}}}`)
	if strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to fit in the budget:\n%s", text)
	}
	hits := strings.Count(text, "\n#### ")
	at := strings.Index(text, "\ngpu: {exporter: dcgm}")
	if at < 0 || hits < 2 || strings.Count(text[at:], "\n#### ") == 0 {
		t.Fatalf("the unterminated snippet was supposed to arrive with %d hits and at least one hit under it:\n%s", hits, text)
	}
	if got := auditedResultCount(t, s, "search-code"); got != hits {
		t.Errorf("mcp_calls.result_count=%d, want %d — the hits under the snippet's unterminated fence are still hits", got, hits)
	}
}

// The `- ` fallback reads the content too: get-symbol-context has no `### `
// heading at all, so a symbol body that lists its steps as `- ` items had every
// one of them counted as a result.
func TestSymbolContextCountIgnoresListItemsInsideTheFencedContent(t *testing.T) {
	s := fixture(t)
	body := strings.Join([]string{
		`def checklist():`,
		`    """Steps:`,
		`- drain the node pool`,
		`- restart the DCGM exporter`,
		`- confirm the metrics gap closed`,
		`"""`,
	}, "\n")
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash)
		VALUES('sl1','r1','main','4fa21bd','ops/checklist.py','checklist','checklist','function','python','def checklist():','Drains a node pool.',1,6,'slh1')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
		VALUES('cl1','r1','main','4fa21bd','ops/checklist.py',1,6,'checklist','code',?,'clh1')`, body)

	text := answerText(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-symbol-context","arguments":{"libraryId":"/kcb/clustara","symbol":"checklist","ref":"main"}}}`)
	if !strings.Contains(text, "- drain the node pool") {
		t.Fatalf("the symbol body was supposed to arrive with its own `- ` items:\n%s", text)
	}
	// Kind, Language, Signature and Source: the four items formatSymbolContext
	// writes above the fence, which TestHeadingOnlyFormattersKeepTheirResultCount
	// measures for a body that happens to hold no list of its own.
	if got := auditedResultCount(t, s, "get-symbol-context"); got != 4 {
		t.Errorf("mcp_calls.result_count=%d, want 4 — the `- ` items of the symbol body are content, not results", got)
	}
}

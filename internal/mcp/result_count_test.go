package mcp

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The audited result count and the truncation notice have to describe the hits an
// agent actually received.
//
// formatCodeSearch writes one `#### ` heading per source hit under the `### Source
// Matches` structural heading (format.go:118), so a counter that only looks at
// `\n### ` reports the number of structural headings — three, whatever the number
// of hits. The operator console reads mcp_calls.result_count straight out of the
// audit row, so "fifty hits" was displayed as "3", and the notice told the agent
// "2 of 2 result sections are included" while the Notes two lines below said the
// index had matched fifty.
//
// Nothing here is synthesized: every assertion reads the answer the real
// formatCodeSearch produced for a real `tools/call` round trip, and the counts it
// compares against are counted out of that same answer.

var lineCitation = regexp.MustCompile(`#L\d+-L\d+$`)

// auditedResultCount returns the result_count the server recorded for the single
// call made to this tool.
func auditedResultCount(t *testing.T, s *Server, tool string) int {
	t.Helper()
	var rows int
	if err := s.store.DB.QueryRow(`SELECT COUNT(*) FROM mcp_calls WHERE tool=?`, tool).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%s: %d audit rows, want exactly one call to read", tool, rows)
	}
	var count int
	if err := s.store.DB.QueryRow(`SELECT result_count FROM mcp_calls WHERE tool=?`, tool).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// answerBody is everything before the truncation notice — the part that is the
// document the tool produced.
func answerBody(text string) string {
	if at := strings.Index(text, "\n\n### Truncated\n"); at >= 0 {
		return text[:at]
	}
	return text
}

// padChunks adds enough GPU chunks that search-code has to leave some unsent.
func padChunks(t *testing.T, s *Server, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("pad%d", i)
		_, err := s.store.DB.Exec(
			`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'r1','main','4fa21bd',?,?,?,'GPU capacity','document',?,?)`,
			id, fmt.Sprintf("docs/gpu-%02d.md", i), 10+i, 40+i,
			fmt.Sprintf("GPU node pool %02d drains before the DCGM exporter restarts, so GPU metrics gap for a minute.", i), "pad"+id)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSearchCodeAuditsTheNumberOfHitsItFound(t *testing.T) {
	s := fixture(t)
	text := answerText(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU"}}}`)
	if strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to fit in the budget:\n%s", text)
	}
	hits := strings.Count(text, "\n#### ")
	if hits == 0 {
		t.Fatalf("no source hits to count:\n%s", text)
	}
	if got := auditedResultCount(t, s, "search-code"); got != hits {
		t.Errorf("mcp_calls.result_count=%d, want %d — the answer carries %d `#### ` hits", got, hits, hits)
	}
}

func TestTruncatedSearchCodeCountsAndKeepsWholeHits(t *testing.T) {
	s := fixture(t)
	padChunks(t, s, 60)
	text := answerText(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","limit":50,"maxBytes":4000}}}`)
	if !strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to exceed the budget:\n%s", text)
	}
	body := answerBody(text)
	sent := strings.Count(body, "\n#### ")
	if sent == 0 {
		t.Fatalf("the truncated answer carries no whole hit:\n%s", body)
	}

	// The notice has to describe the same two numbers: the hits the search found
	// and the hits that survived the cut.
	notice := regexp.MustCompile(`- (\d+) of (\d+) result sections are included\.`).FindStringSubmatch(text)
	if notice == nil {
		t.Fatalf("the notice does not say how many results were sent:\n%s", text)
	}
	found := regexp.MustCompile(`### Source Matches \((\d+)\)`).FindStringSubmatch(body)
	if found == nil {
		t.Fatalf("the answer does not state how many hits the search found:\n%s", body)
	}
	if notice[2] != found[1] {
		t.Errorf("the notice says %s result sections exist, but the search found %s hits", notice[2], found[1])
	}
	if notice[1] != fmt.Sprint(sent) {
		t.Errorf("the notice says %s result sections were included, but %d whole hits were sent", notice[1], sent)
	}

	// The cut has to land between hits, so the last one keeps its snippet and the
	// citation an agent quotes it by.
	last := body[strings.LastIndex(body, "\n#### "):]
	if !strings.Contains(last, "\nSource: ") {
		t.Errorf("the last hit lost its Source citation, so it cannot be quoted:\n%s", last)
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	tail := lines[len(lines)-1]
	if !lineCitation.MatchString(tail) {
		t.Errorf("the answer ends mid-hit on %q, not on a citation line", tail)
	}
}

// markdownWithSubsections is a documentation file that uses `#### ` subsections of
// its own and keeps a `### ` heading for later. Read through read-file, those
// `#### ` lines are the *content being shown*, not code-search hits, so neither
// the counter nor the cut may treat them as result boundaries.
func markdownWithSubsections() string {
	var b strings.Builder
	b.WriteString("# GPU Runbook\n")
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, "\n#### Symptom %02d\n\nDCGM exporter stops reporting after the node pool drains; wait for the restart.\n", i)
	}
	// Plain prose between the last subsection and the next `### `: this is the
	// stretch a cut that prefers the `#### ` throws away.
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "\nParagraph %02d: the gap closes on its own once the exporter has rejoined the pool.\n", i)
	}
	b.WriteString("\n### Escalation\n")
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, "\nStep %02d: page the platform on-call and attach the exporter log tail.\n", i)
	}
	return b.String()
}

func readMarkdownFile(t *testing.T, s *Server, budget int) string {
	t.Helper()
	content := markdownWithSubsections()
	if _, err := s.store.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','main','docs/runbook-subsections.md','runbook-subsections.md',?,1,'4fa21bd')`, len(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('sub1','r1','main','4fa21bd','docs/runbook-subsections.md',1,200,'GPU Runbook','document',?,'sub')`, content); err != nil {
		t.Fatal(err)
	}
	return answerText(t, s, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read-file","arguments":{"libraryId":"/kcb/clustara","path":"docs/runbook-subsections.md","ref":"main","maxBytes":%d}}}`, budget))
}

// A truncated read-file answer must still be cut at the latest boundary the file
// offers. Preferring a `#### ` line of the content over the later `### ` heading
// silently delivers less of the file for the same budget.
func TestReadFileCutIgnoresContentSubsectionHeadings(t *testing.T) {
	for _, budget := range []int{3400, 4000} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			s := fixture(t)
			text := readMarkdownFile(t, s, budget)
			if !strings.Contains(text, "### Truncated") {
				t.Fatalf("this call was supposed to exceed the budget:\n%s", text)
			}
			body := answerBody(text)
			// The prose past the last subsection offers the ladder a paragraph and a
			// `### ` boundary, so a cut that ignores the content's `#### ` lines reaches
			// into it. Stopping at the last `#### ` instead drops the final subsection
			// and every paragraph after it.
			if !strings.Contains(body, "#### Symptom 23") {
				t.Errorf("the cut dropped the last `#### ` subsection of the file; %d bytes delivered:\n%s", len(body), body)
			}
			if !strings.Contains(body, "Paragraph 00:") {
				t.Errorf("the cut stopped at a `#### ` subsection instead of the later boundary; %d bytes delivered:\n%s", len(body), body)
			}
		})
	}
}

// The audited count of a read-file answer describes the sections of the file, not
// the `#### ` subsections its text happens to use.
func TestReadFileCountIgnoresContentSubsectionHeadings(t *testing.T) {
	s := fixture(t)
	text := readMarkdownFile(t, s, MaxResponseBytes)
	subsections := strings.Count(text, "\n#### ")
	if subsections == 0 {
		t.Fatalf("the file was supposed to arrive with its `#### ` subsections:\n%s", text)
	}
	got := auditedResultCount(t, s, "read-file")
	if got == subsections {
		t.Errorf("mcp_calls.result_count=%d is the number of `#### ` subsections in the file being shown", got)
	}
	if want := strings.Count(text, "\n### "); got != want {
		t.Errorf("mcp_calls.result_count=%d, want the unchanged `### ` count %d", got, want)
	}
}

// The deepest-heading rule must not reach any formatter that writes its results
// as `### ` sections or `- ` items. These counts were measured against the
// unchanged counter and must not move.
func TestHeadingOnlyFormattersKeepTheirResultCount(t *testing.T) {
	for _, tc := range []struct {
		tool, request string
		want          int
	}{
		{"read-file", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read-file","arguments":{"libraryId":"/kcb/clustara","path":"docs/gpu.md","ref":"main"}}}`, 0},
		{"find-symbol", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find-symbol","arguments":{"query":"GetGPU"}}}`, 2},
		{"get-symbol-context", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-symbol-context","arguments":{"libraryId":"/kcb/clustara","symbol":"Service.GetGPU","ref":"main"}}}`, 4},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			s := fixture(t)
			text := answerText(t, s, tc.request)
			if strings.Contains(text, "\n#### ") {
				t.Fatalf("%s writes `#### ` headings, so it is not a control for this change:\n%s", tc.tool, text)
			}
			if got := auditedResultCount(t, s, tc.tool); got != tc.want {
				t.Errorf("mcp_calls.result_count=%d, want the unchanged %d:\n%s", got, tc.want, text)
			}
		})
	}
}

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

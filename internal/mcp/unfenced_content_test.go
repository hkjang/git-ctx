package mcp

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The counterpart of fenced_content_test.go. Most formatters write the indexed
// content as prose rather than inside a fence of the answer's own:
// formatSemanticSearch (format.go:257) and formatRunbooks (format.go:485) put a
// whole chunk under a `### ` heading. A chunk carries whatever markdown the source
// file had, and the chunker splits on headings without tracking fences
// (indexer.go:1110), so a chunk that ends inside a fenced example keeps only the
// opening backtick run.
//
// For those answers the backtick run is content, not structure. Reading it as the
// answer's own fence hid every hit below it from the audited count and let the cut
// skip past the heading it was supposed to stop at.
//
// Nothing here is synthesized: every assertion reads the answer a real
// `tools/call` round trip produced through the real formatters.

// storeUnbalancedChunks indexes hits that all match the same query, the first of
// which ends inside a fenced example and so keeps only the opening backtick run.
// It is first in both rankings — same words as the rest plus the example, and the
// lowest file path — so every other hit is printed below its unterminated fence.
func storeUnbalancedChunks(t *testing.T, s *Server, hits int) {
	t.Helper()
	for i := 0; i < hits; i++ {
		content := fmt.Sprintf("GPU exporter note %02d: the DCGM exporter restarts once the node pool has drained.", i)
		if i == 0 {
			content += "\n\n```bash\nkubectl rollout restart ds/dcgm-exporter"
		}
		if _, err := s.store.DB.Exec(
			`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'r1','main','4fa21bd',?,?,?,?,'document',?,?)`,
			fmt.Sprintf("unb%02d", i), fmt.Sprintf("docs/runbooks/gpu-%02d.md", i), 10+i, 40+i,
			fmt.Sprintf("GPU runbook %02d", i), content, fmt.Sprintf("unbh%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
}

// The audited count of an answer whose hits are prose is the number of hits, not
// the number of hits above the first backtick run the content happens to carry.
// The operator console reads mcp_calls.result_count straight out of the audit row.
func TestUnfencedHitsArePastAChunkThatOpensAFence(t *testing.T) {
	for _, tc := range []struct{ tool, request string }{
		{"search-semantic", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-semantic","arguments":{"query":"GPU exporter","limit":20}}}`},
		{"find-runbook", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find-runbook","arguments":{"query":"GPU exporter","limit":20}}}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			s := fixture(t)
			storeUnbalancedChunks(t, s, 8)
			text := answerText(t, s, tc.request)
			if strings.Contains(text, "### Truncated") {
				t.Fatalf("this call was supposed to fit in the budget:\n%s", text)
			}
			at := strings.Index(text, "\n```bash\n")
			if at < 0 {
				t.Fatalf("the chunk with the unterminated fence did not arrive:\n%s", text)
			}
			headings := strings.Count(text, "\n### ")
			below := strings.Count(text[at:], "\n### ")
			if below < 2 {
				t.Fatalf("only %d heading(s) below the unterminated fence, so this is not a regression test:\n%s", below, text)
			}
			if got := auditedResultCount(t, s, tc.tool); got != headings {
				t.Errorf("mcp_calls.result_count=%d, want %d — the %d hits printed below the chunk's unterminated fence are still hits",
					got, headings, below)
			}
		})
	}
}

// A truncated answer of prose hits still ends on a whole hit. Treating the
// content's backtick run as the answer's own fence made the `### ` rule skip every
// heading below it, so the cut fell through to the paragraph rule and left a
// heading with neither a snippet nor the Source citation an agent quotes it by —
// and closeOpenFence then appended a fence that belonged to the content.
func TestTruncatedUnfencedHitsEndOnAWholeHit(t *testing.T) {
	s := fixture(t)
	storeUnbalancedChunks(t, s, 20)
	text := answerText(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-semantic","arguments":{"query":"GPU exporter","limit":20,"maxBytes":2500}}}`)
	if !strings.Contains(text, "### Truncated") {
		t.Fatalf("this call was supposed to exceed the budget:\n%s", text)
	}
	body := answerBody(text)
	if !strings.Contains(body, "\n```bash\n") {
		t.Fatalf("the cut landed above the chunk with the unterminated fence, so it proves nothing:\n%s", body)
	}
	if strings.Count(body, "\n### ") < 2 {
		t.Fatalf("the cut kept no hit below the unterminated fence:\n%s", body)
	}

	// The last hit shown keeps its snippet and its citation.
	last := body[strings.LastIndex(body, "\n### "):]
	if !strings.Contains(last, "\nSource: ") {
		t.Errorf("the answer ends on a heading with no Source citation, so the hit cannot be quoted:\n%s", last)
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	// closeOpenFence terminates the block the first chunk's content left open, so
	// the notice below is not read as code. That closer belongs to the content; the
	// line before it is where the answer's own text ends.
	if tail := lines[len(lines)-1]; len(lines) > 1 && strings.Trim(tail, "`") == "" {
		lines = lines[:len(lines)-1]
	}
	// formatSemanticSearch wraps the citation in backticks, so the `#L1-L2` of a
	// whole hit is the last thing on the line before the closing one.
	citation := regexp.MustCompile("#L\\d+-L\\d+`?$")
	if tail := lines[len(lines)-1]; !citation.MatchString(tail) {
		t.Errorf("the answer ends mid-hit on %q, not on a citation line", tail)
	}

	// And the notice counts the hits that exist, not the ones above the content's
	// backtick run.
	notice := regexp.MustCompile(`- (\d+) of (\d+) result sections are included\.`).FindStringSubmatch(text)
	if notice == nil {
		t.Fatalf("the notice does not say how many results were sent:\n%s", text)
	}
	found := regexp.MustCompile(`Matches: (\d+) ·`).FindStringSubmatch(body)
	if found == nil {
		t.Fatalf("the answer does not state how many hits the search found:\n%s", body)
	}
	if notice[2] != found[1] {
		t.Errorf("the notice says %s result sections exist, but the search matched %s hits", notice[2], found[1])
	}
	if sent := strings.Count(body, "\n### "); notice[1] != fmt.Sprint(sent) {
		t.Errorf("the notice says %s result sections were included, but %d whole hits were sent", notice[1], sent)
	}
}

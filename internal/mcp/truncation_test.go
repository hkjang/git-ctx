package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A truncated answer has to be most of the budget it says it was cut to.
//
// The cut prefers a boundary a reader can see — a section start, a paragraph,
// a line — but the paragraph and line rules took whatever they found. A file
// with no blank line after its first few, which is every minified bundle, CSV
// and densely packed source file, has its last blank line right before the
// content begins. Cutting there answered a twelve-thousand-byte request with
// six hundred bytes: a header, and a notice saying the answer had been cut to
// the budget.
func TestATruncatedAnswerUsesTheBudgetItWasGiven(t *testing.T) {
	var dense strings.Builder
	dense.WriteString("## internal/settlement/big.go\n\n`/gitlab~core/api` · ref `main`\n\n```go\n")
	for i := 0; i < 400; i++ {
		dense.WriteString("func handler(order Order) error { return reconcile(order) }  // 정산 처리\n")
	}
	dense.WriteString("```\n\n### Notes\n- index: reassembled from the stored chunks of this ref.\n")
	answer := dense.String()

	for _, budget := range []int{3000, 8000, 12000, 20000} {
		cut := clampResponse(answer, budget)
		if len(cut) > budget+responseNoticeBytes {
			t.Errorf("budget %d produced %d bytes", budget, len(cut))
		}
		if len(answer) > budget && len(cut)*10 < budget*6 {
			t.Errorf("budget %d was answered with %d bytes; most of what was asked for was thrown away to find a prettier boundary",
				budget, len(cut))
		}
		if !utf8.ValidString(cut) {
			t.Errorf("budget %d cut through a character", budget)
		}
		// The notice and the notes must not end up inside the code block.
		if strings.Count(cut, "```")%2 != 0 {
			t.Errorf("budget %d left the code fence open, so everything after it reads as file content:\n%s",
				budget, cut[max(0, len(cut)-200):])
		}
		if len(answer) > budget && !strings.Contains(cut, "### Truncated") {
			t.Errorf("budget %d cut the answer without saying so", budget)
		}
		if !strings.Contains(cut, "### Notes") {
			t.Errorf("budget %d dropped the notes", budget)
		}
	}
}

// An answer made of result sections still ends on a section, so the last result
// is whole — as long as that keeps most of the room.
func TestASectionedAnswerStillEndsOnASection(t *testing.T) {
	var results strings.Builder
	results.WriteString("## Code Search\n\nNormalized query: `settleInvoice`\n")
	for i := 0; i < 40; i++ {
		results.WriteString("\n### /gitlab~core/api · internal/settlement/handler.go\n\nfunc settleInvoice(order Order) error { return reconcile(order) }\n\nSource: gitlab://core/api@c0ffee/internal/settlement/handler.go#L1-L9\n")
	}
	results.WriteString("\n### Notes\n- acl: unrestricted.\n")
	answer := results.String()

	cut := clampResponse(answer, 4000)
	body := cut[:strings.Index(cut, "### Truncated")]
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), "#L1-L9") {
		t.Errorf("the answer does not end on a whole result:\n%s", body[max(0, len(body)-200):])
	}
	if !strings.Contains(cut, "result sections are included") {
		t.Error("the notice does not say how many results were sent")
	}
}

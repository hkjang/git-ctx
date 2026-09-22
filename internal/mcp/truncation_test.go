package mcp

import (
	"encoding/json"
	"fmt"
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

// Every byte offset includes cuts inside each multi-byte encoding, as well as
// exact-fit and larger budgets that must leave the original untouched.
func TestCutAtBoundaryPreservesUTF8(t *testing.T) {
	for _, unit := range []string{"ASCII", "é", "가", "😀", "aé가😀Z"} {
		t.Run(unit, func(t *testing.T) {
			original := strings.Repeat(unit, 20)
			for limit := 0; limit <= len(original)+1; limit++ {
				got := cutAtBoundary(original, limit)
				if !utf8.ValidString(got) || !strings.HasPrefix(original, got) {
					t.Errorf("limit %d: invalid original prefix %q", limit, got)
				}
				wantLen := 0
				for at := range original {
					if at <= limit {
						wantLen = at
					}
				}
				if limit >= len(original) {
					wantLen = len(original)
				}
				if len(got) != wantLen {
					t.Errorf("limit %d: got %d bytes, want %d", limit, len(got), wantLen)
				}
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				var decoded string
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				if strings.ContainsRune(decoded, '\uFFFD') || decoded != got {
					t.Errorf("limit %d: JSON replaced a source character", limit)
				}
			}
		})
	}
}

func TestCutAtBoundaryKeepsBoundaryPreferences(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       int
	}{
		{"section before later paragraph and line", strings.Repeat("x", 60) + "\n### section\n\nparagraph\n" + strings.Repeat("y", 100), 60},
		{"paragraph before later line", strings.Repeat("x", 60) + "\n\nparagraph\n" + strings.Repeat("y", 100), 60},
		{"line at sixty percent", strings.Repeat("x", 60) + "\n" + strings.Repeat("y", 100), 60},
		{"early section falls through to paragraph", strings.Repeat("x", 51) + "\n### xxxx\n\n" + strings.Repeat("y", 100), 60},
		{"early paragraph falls through to line", strings.Repeat("x", 55) + "\n\nxxx\n" + strings.Repeat("y", 100), 60},
		{"early line cannot waste the budget", strings.Repeat("x", 59) + "\n" + strings.Repeat("y", 100), 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if got := cutAtBoundary(tc.text, 100); got != tc.text[:want] {
				t.Fatalf("kept %d bytes, want %d", len(got), want)
			}
		})
	}
}

func TestSingleLineTruncationKeepsFenceAndNotes(t *testing.T) {
	const notes = "\n### Notes\n- index: stored chunks.\n"
	for _, unit := range []string{"é", "가", "😀", "aé가😀Z"} {
		for budget := 3000; budget < 3012; budget++ {
			t.Run(fmt.Sprintf("%s/%d", unit, budget), func(t *testing.T) {
				original := "## File\n\n```txt\n" + strings.Repeat(unit, 4000) + "\n```\n" + notes
				got := clampResponse(original, budget)
				body, _, found := strings.Cut(got, "\n\n### Truncated\n")
				if !found || !strings.HasSuffix(body, "\n```") || strings.Count(got, "```") != 2 {
					t.Fatal("missing truncation notice or closed fence")
				}
				kept := strings.TrimSuffix(body, "\n```")
				if !utf8.ValidString(got) || !strings.HasPrefix(original, kept) || strings.ContainsRune(got, '\uFFFD') {
					t.Fatal("truncation damaged a source character")
				}
				if !strings.HasSuffix(got, notes) {
					t.Fatal("small Notes section was lost")
				}
				if len(kept)*10 < (budget-len(notes)-responseNoticeBytes)*6 {
					t.Fatal("too little content retained")
				}
			})
		}
	}
}

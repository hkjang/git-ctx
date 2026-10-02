package mcp

import (
	"fmt"
	"strconv"
	"strings"
)

// Response budgeting. An answer is cut at a section or line boundary so a
// truncated reply still reads as the document it was.

const (
	// DefaultResponseBytes bounds one tool answer when the operator set no
	// per-tool budget. Roughly six thousand tokens: large enough for a full
	// search page, small enough to leave the agent room to work.
	DefaultResponseBytes = 24 << 10
	// MinResponseBytes keeps a budget from becoming a header with no content.
	MinResponseBytes = 2000
	// MaxResponseBytes is the ceiling an operator or caller may raise a tool to.
	MaxResponseBytes = 256 << 10
)

// clampResponse trims one answer to the byte budget without hiding that it did
// so. Two properties matter for an agent reading the result:
//
//   - the cut lands on a result boundary, so the last entry is whole rather than
//     ending mid-line, and
//   - the trailing Notes section survives, because that is where the tool
//     explains which retrieval path ran and what the ACL filtered. Losing it
//     would turn "indexing, answered live" into an unexplained short answer.
func clampResponse(text string, budget int) string {
	if budget <= 0 || len(text) <= budget {
		return text
	}
	body, notes := text, ""
	if at := strings.LastIndex(text, "\n### Notes\n"); at >= 0 {
		body, notes = text[:at], text[at:]
	}
	// The notes only keep their reservation while they stay a small part of the
	// budget; an oversized tail would leave no room for actual results.
	reserved := len(notes)
	if reserved > budget/3 {
		body, notes, reserved = text, "", 0
	}
	total := sectionCount(body)
	room := budget - reserved - responseNoticeBytes
	if room < MinResponseBytes/2 {
		room = budget / 2
	}
	kept := cutAtBoundary(body, room)
	shown := sectionCount(kept)
	notice := fmt.Sprintf("\n\n### Truncated\n- This answer was cut to the %s byte budget of this tool; %s bytes were produced.\n",
		thousands(budget), thousands(len(text)))
	if total > 0 {
		notice += fmt.Sprintf("- %d of %d result sections are included. The rest are not lost, only unsent.\n", shown, total)
	}
	notice += "- Narrow the next call instead of retrying the same one: add libraryId or path, lower limit, or read a line range with read-file.\n"
	return closeOpenFence(strings.TrimRight(kept, "\n")) + notice + notes
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// responseNoticeBytes reserves room for the truncation notice itself, so adding
// it can never push the answer back over the budget.
const responseNoticeBytes = 320

// sectionCount counts the result entries of a formatted answer. The formatters
// use a `### ` heading per result, and the list formatters use a `- ` item. One
// exception counts deeper: formatCodeSearch (format.go:118) writes a `#### `
// heading per source hit beneath the `### Repository Matches` / `### Source
// Matches` / `### Notes` structure, so counting its `### ` headings reported
// three results whatever the number of hits.
//
// Only the `### ` and `- ` rules skip what sits inside a code fence, because
// only the formatters they count for put content in one. formatCodeSearch writes
// its snippets as prose, so the hit region holds no fence of the answer's own
// and a snippet that happens to show one would otherwise hide every hit under
// it from the count.
func sectionCount(text string) int {
	if at := codeSearchHits(text); at >= 0 {
		if count := strings.Count(text[at:], "\n#### "); count > 0 {
			return count
		}
	}
	if count := countUnfenced(text, "### "); count > 0 {
		return count
	}
	return countUnfenced(text, "- ")
}

// unfencedLines visits the lines of text that lie outside a code fence, passing
// the offset of the newline before each one — the position the boundary rules
// below search for. The first line has no preceding newline and so is never a
// boundary, which is also how the formatters write an answer: every one of them
// opens with its own `## ` title.
//
// Everything a formatter puts inside a fence is the content being shown, not the
// structure of the answer, and reading the two as one thing cost an agent both
// bytes and an honest count. A document with a `### ` heading of its own had the
// cut stop there — a twelve-thousand-byte read-file answered with eight
// thousand, the notice still saying it had been cut to the budget — and had that
// heading audited as a result section. The `- ` fallback read the content the
// same way: a symbol body listing its steps as `- ` items recorded one result
// per step.
//
// The scan is the one closeOpenFence uses to find an unterminated fence, so the
// layer that closes a fence and the layer that counts and cuts now agree on
// where the fences are.
func unfencedLines(text string, visit func(at int, line string)) {
	fence, start := 0, 0
	for {
		line, next := text[start:], len(text)+1
		if end := strings.IndexByte(line, '\n'); end >= 0 {
			line, next = line[:end], start+end+1
		}
		if fence == 0 && start > 0 {
			visit(start-1, line)
		}
		fence = lineFence(fence, line)
		if next > len(text) {
			return
		}
		start = next
	}
}

// countUnfenced counts the lines of text that start with prefix outside a code
// fence.
func countUnfenced(text, prefix string) int {
	count := 0
	unfencedLines(text, func(_ int, line string) {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	})
	return count
}

// lastUnfenced returns the offset of the last line of text that starts with
// prefix outside a code fence, or -1 when there is none.
func lastUnfenced(text, prefix string) int {
	last := -1
	unfencedLines(text, func(at int, line string) {
		if strings.HasPrefix(line, prefix) {
			last = at
		}
	})
	return last
}

// codeSearchHits locates the hit region of a code search — everything from the
// `### Source Matches (` heading on — and returns -1 for every other answer.
//
// The `#### ` rules belong to that region alone. A `#### ` line anywhere else is
// a heading of the *content being shown*: read-file, query-docs and
// export-context hand back markdown that uses subsections of its own, and
// treating those as result boundaries both miscounted them and cut them short.
// A documentation file with `#### ` subsections mid-window and a `### ` heading
// later lost a fifth of its delivered bytes at the same budget, because the cut
// preferred an earlier content subsection over the later heading.
func codeSearchHits(text string) int {
	if !strings.HasPrefix(text, "## Code Search\n") {
		return -1
	}
	return strings.Index(text, "\n### Source Matches (")
}

// cutAtBoundary keeps as much of the answer as the room allows, ending it
// somewhere a reader can see the seam.
//
// Every candidate boundary has to keep most of the room, which the paragraph
// and line rules used to skip. A file with no blank line after its first few —
// a minified bundle, a CSV, densely packed code — has its last "\n\n" right
// before the content starts, and cutting there returned a header and nothing
// else: read-file asked for twelve thousand bytes and answered with six
// hundred, while the notice said the answer had been cut to the budget.
func cutAtBoundary(text string, limit int) string {
	if limit >= len(text) {
		return text
	}
	window := text[:limit]
	enough := func(at int) bool { return at > 0 && at*10 >= limit*6 }
	// A result boundary first. Inside a code search's hit region that boundary is
	// a `#### ` heading: cutting at the `### Source Matches` heading above it
	// dropped every hit, while cutting mid-hit left a heading with no snippet and
	// no Source citation. Only a code search gets this rule — see codeSearchHits
	// for what a `#### ` line means in any other answer.
	if hits := codeSearchHits(window); hits >= 0 {
		if at := strings.LastIndex(window[hits:], "\n#### "); at >= 0 && enough(hits+at) {
			return text[:hits+at]
		}
	}
	// A `### ` line inside a fence is a heading of the content being shown —
	// read-file and get-symbol-context wrap a whole file or symbol body in one —
	// so it is not a seam between results. Cutting there handed back a third of
	// the budget less than the caller asked for. See sectionCount.
	if at := lastUnfenced(window, "### "); enough(at) {
		return text[:at]
	}
	if at := strings.LastIndex(window, "\n\n"); enough(at) {
		return text[:at]
	}
	if at := strings.LastIndexByte(window, '\n'); enough(at) {
		return text[:at]
	}
	return runeSafeCut(text, limit)
}

// closeOpenFence terminates a code fence the cut landed inside. Without it the
// notice explaining the truncation, and the notes after it, are read as part of
// the file that was being shown.
func closeOpenFence(text string) string {
	fence := 0
	for _, line := range strings.Split(text, "\n") {
		fence = lineFence(fence, line)
	}
	if fence > 0 {
		return text + "\n" + strings.Repeat("`", fence)
	}
	return text
}

// lineFence applies one line to the fence state and returns the new one: the
// length of the backtick run holding a block open, or zero outside one.
//
// The formatters emit backtick fences. Up to three leading spaces are allowed;
// inline backticks and shorter nested fences are content.
func lineFence(fence int, line string) int {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return fence
	}
	run := 0
	for run < len(trimmed) && trimmed[run] == '`' {
		run++
	}
	if fence == 0 {
		if run >= 3 && !strings.Contains(trimmed[run:], "`") {
			return run
		}
		return 0
	}
	if run >= fence && strings.Trim(trimmed[run:], " \t\r") == "" {
		return 0
	}
	return fence
}

// thousands formats a byte count the way the notice reads best.
func thousands(value int) string {
	digits := strconv.Itoa(value)
	var b strings.Builder
	for index, char := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(char)
	}
	return b.String()
}

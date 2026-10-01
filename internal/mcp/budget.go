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
func sectionCount(text string) int {
	if at := codeSearchHits(text); at >= 0 {
		if count := strings.Count(text[at:], "\n#### "); count > 0 {
			return count
		}
	}
	if count := strings.Count(text, "\n### "); count > 0 {
		return count
	}
	return strings.Count(text, "\n- ")
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
	if at := strings.LastIndex(window, "\n### "); enough(at) {
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
		// The formatters emit backtick fences. Up to three leading spaces
		// are allowed; inline backticks and shorter nested fences are content.
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 {
			continue
		}
		run := 0
		for run < len(trimmed) && trimmed[run] == '`' {
			run++
		}
		if fence == 0 {
			if run >= 3 && !strings.Contains(trimmed[run:], "`") {
				fence = run
			}
		} else if run >= fence && strings.Trim(trimmed[run:], " \t\r") == "" {
			fence = 0
		}
	}
	if fence > 0 {
		return text + "\n" + strings.Repeat("`", fence)
	}
	return text
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

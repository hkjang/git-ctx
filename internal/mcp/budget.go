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
// use a `### ` heading per result, and the list formatters use a `- ` item.
func sectionCount(text string) int {
	if count := strings.Count(text, "\n### "); count > 0 {
		return count
	}
	return strings.Count(text, "\n- ")
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
	if at := strings.LastIndex(window, "\n### "); enough(at) {
		return text[:at]
	}
	if at := strings.LastIndex(window, "\n\n"); enough(at) {
		return text[:at]
	}
	if at := strings.LastIndexByte(window, '\n'); enough(at) {
		return text[:at]
	}
	return runeSafeCut(window, len(window))
}

// closeOpenFence terminates a code fence the cut landed inside. Without it the
// notice explaining the truncation, and the notes after it, are read as part of
// the file that was being shown.
func closeOpenFence(text string) string {
	if strings.Count(text, "\n```")%2 == 0 && !strings.HasPrefix(text, "```") {
		return text
	}
	if strings.Count(text, "```")%2 == 0 {
		return text
	}
	return text + "\n```"
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

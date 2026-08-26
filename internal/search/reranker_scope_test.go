package search

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The reranker runs for query-docs and for nothing else.
//
// The platform used to describe it as reordering "every answer", and
// explain-search-result — the tool whose whole job is to say how an answer was
// ordered — named rerank in a path that never reranks. An operator who
// configures a reranker and then wonders why search-code looks identical is
// being misled by the product, not confused.
//
// This test is the place that notices when that stops being true. If the
// reranker starts running elsewhere, it fails, and the sentences that promise
// where it applies have to be revisited with it.
func TestTheRerankerRunsForOneToolOnly(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	// Every call site of the configured reranker.
	uses := regexp.MustCompile(`s\.reranker\b`).FindAllStringIndex(text, -1)
	var callers []string
	for _, at := range uses {
		callers = append(callers, enclosingFunction(text, at[0]))
	}
	for _, caller := range callers {
		switch caller {
		case "SetRerankerLoader", "Query":
		default:
			t.Errorf("the reranker is now used by %s as well; the health view, the console guide and "+
				"explain-search-result all say it applies to query-docs alone, and have to be corrected with it", caller)
		}
	}

	// And the explanation of a search must not claim it.
	explain := text[strings.Index(text, "func (s *Service) ExplainSearch("):]
	explain = explain[:strings.Index(explain, "\nfunc ")]
	if strings.Contains(strippedComments(explain), "rerank") {
		t.Errorf("explain-search-result names reranking in a path that does not rerank")
	}
}

// strippedComments removes // lines so prose about the reranker is not mistaken
// for a call to it.
func strippedComments(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// enclosingFunction names the function a byte offset falls inside.
func enclosingFunction(text string, at int) string {
	head := text[:at]
	start := strings.LastIndex(head, "\nfunc ")
	if start < 0 {
		return "?"
	}
	line := text[start+len("\nfunc "):]
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	// "(s *Service) Query(ctx ..." -> "Query"
	if close := strings.Index(line, ") "); close >= 0 && strings.HasPrefix(line, "(") {
		line = line[close+2:]
	}
	if open := strings.IndexByte(line, '('); open >= 0 {
		line = line[:open]
	}
	return strings.TrimSpace(line)
}

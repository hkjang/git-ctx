package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every tool schema declares additionalProperties:false, and nothing enforced
// it. An agent that sent maxResults instead of limit, or library_id instead of
// libraryId, was answered as though it had asked for neither: the wrong number
// of results, or an error about a required argument it believed it had sent.
// Nothing in the reply said an argument had been dropped.
//
// The call is still answered — refusing it would break an agent that passes a
// harmless extra — but the drop is written down, together with the arguments
// the tool does accept, so the next call can be right.
func TestAnArgumentTheToolDoesNotHaveIsReportedBack(t *testing.T) {
	s := fixture(t)
	answer := func(body string) string {
		t.Helper()
		raw, err := json.Marshal(call(t, s, body))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	// A misspelled optional argument: the answer is computed without it and looks
	// entirely normal, which is what makes the silence expensive.
	text := answer(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","maxResults":50}}}`)
	for _, want := range []string{"maxResults is not an argument of search-code", "It accepts", "limit"} {
		if !strings.Contains(text, want) {
			t.Errorf("a dropped argument is not reported: %q missing from\n%s", want, text)
		}
	}

	// A misspelled required argument fails, and the failure has to name the real
	// reason: the error alone sends an agent looking for a value it did send.
	text = answer(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query-docs","arguments":{"library_id":"/kcb/clustara","query":"GPU"}}}`)
	if !strings.Contains(text, "library_id is not an argument of query-docs") {
		t.Errorf("an error caused by a misspelled argument does not say so:\n%s", text)
	}
	if !strings.Contains(text, "libraryId") {
		t.Errorf("the reply does not name the argument that was meant:\n%s", text)
	}

	// A correct call stays quiet. A note on every answer would be noise, and
	// noise is how a real one gets missed.
	text = answer(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","limit":3}}}`)
	if strings.Contains(text, "is not an argument of") {
		t.Errorf("a call that used only real arguments was told otherwise:\n%s", text)
	}

	// maxBytes is added to the served schema by the catalog rather than the
	// registry, and _meta belongs to the protocol. Neither is the tool's to
	// reject.
	text = answer(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","maxBytes":4000,"_meta":{"progressToken":1}}}}`)
	if strings.Contains(text, "is not an argument of") {
		t.Errorf("maxBytes or _meta was reported as unknown:\n%s", text)
	}
}

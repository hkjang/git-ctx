package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A number sent as a string is a number.
//
// additionalProperties:false is enforced, so a misspelled argument is written
// down. A correctly named argument of the wrong JSON type was still dropped in
// silence: "startLine": "400" is what a gateway that form-encodes its arguments
// sends and what a model writes when it quotes its numbers, and read-file
// answered it with the whole file, cut at the budget, saying nothing about the
// range it had ignored.
func TestAQuotedNumberIsReadAsTheNumber(t *testing.T) {
	s := fixture(t)
	if _, err := s.store.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
VALUES('c4','r1','main','4fa21bd','lines.go',1,6,'lines','code',?,'h4')`,
		"package gpu\n\nfunc first() {}\n\nfunc second() {}\n\nfunc third() {}"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id)
VALUES('r1','main','lines.go','lines.go',70,1,'4fa21bd')`); err != nil {
		t.Fatal(err)
	}
	read := func(id int, lines string) string {
		t.Helper()
		return answerText(t, s, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"read-file","arguments":{"path":"lines.go"%s}}}`, id, lines))
	}

	whole := read(1, "")
	numbered := read(2, `,"startLine":3,"endLine":5`)
	quoted := read(3, `,"startLine":"3","endLine":"5"`)

	// Without this the rest of the test passes whatever the range does.
	if whole == numbered {
		t.Fatalf("the line range changes nothing in this fixture, so it cannot show that dropping it matters:\n%s", whole)
	}
	if !strings.HasPrefix(quoted, numbered) {
		t.Errorf("a quoted line range was not answered like the range it spells:\nquoted:\n%s\nnumbered:\n%s", quoted, numbered)
	}
	for _, want := range []string{
		`startLine was sent as the string "3" and read as the number 3`,
		`endLine was sent as the string "5" and read as the number 5`,
	} {
		if !strings.Contains(quoted, want) {
			t.Errorf("the reply does not say how the argument was read: %q missing from\n%s", want, quoted)
		}
	}
	if strings.Contains(numbered, "read as declared") {
		t.Errorf("a call that sent the declared types was told its arguments had been converted:\n%s", numbered)
	}
}

// A list sent as one string is that list. export-context refused a call naming
// one repository unless the ID arrived inside an array, which is a distinction
// the client's transport makes rather than the caller.
func TestAListSentAsOneStringIsRead(t *testing.T) {
	s := fixture(t)
	text := answerText(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"export-context","arguments":{"libraryIds":"/kcb/clustara","query":"GPU metrics"}}}`)
	if !strings.Contains(text, "/kcb/clustara") || !strings.Contains(text, "untrusted reference data") {
		t.Fatalf("a single library ID sent as a string exported nothing:\n%s", text)
	}
	if !strings.Contains(text, `libraryIds was sent as the string "/kcb/clustara" and read as a list of 1`) {
		t.Errorf("the reply does not say how the list was read:\n%s", text)
	}
}

// A value that spells nothing is still dropped — there is no number in "many" —
// but the answer no longer pretends the caller asked for the default.
func TestAValueThatCannotBeReadIsReportedBack(t *testing.T) {
	s := fixture(t)
	text := answerText(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","limit":"many"}}}`)
	for _, want := range []string{
		`limit was sent as the string "many", which does not spell a whole number`,
		"so this answer was produced without it",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("an unreadable argument is not reported: %q missing from\n%s", want, text)
		}
	}

	text = answerText(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search-code","arguments":{"query":"GPU","limit":{"max":5}}}}`)
	if !strings.Contains(text, "limit was sent as an object where a whole number was expected") {
		t.Errorf("an argument of the wrong shape is not reported:\n%s", text)
	}
}

// The conversions the note describes are the ones the argument rules perform,
// which is what makes the note true.
func TestArgumentValuesAreDecodedTheWayTheNoteSays(t *testing.T) {
	for _, testCase := range []struct {
		raw  string
		want int
	}{
		{`{"limit":5}`, 5},
		{`{"limit":"5"}`, 5},
		{`{"limit":" 5 "}`, 5},
		{`{"limit":"5.0"}`, 5},
		{`{"limit":-3}`, -3},
		{`{"limit":"many"}`, 20},
		{`{"limit":5.5}`, 20},
		{`{"limit":true}`, 20},
		{`{"limit":1e30}`, 20},
		{`{"limit":"99999999999999999999"}`, 20},
		{`{}`, 20},
	} {
		if got := intArg(decode(t, testCase.raw), "limit", 20); got != testCase.want {
			t.Errorf("%s read as limit=%d, want %d", testCase.raw, got, testCase.want)
		}
	}

	for _, testCase := range []struct {
		raw  string
		want []string
	}{
		{`{"libraryIds":["/a/b","/c/d"]}`, []string{"/a/b", "/c/d"}},
		{`{"libraryIds":"/a/b"}`, []string{"/a/b"}},
		{`{"libraryIds":"/a/b, /c/d"}`, []string{"/a/b", "/c/d"}},
		{`{"libraryIds":"  "}`, nil},
		{`{"libraryIds":7}`, nil},
	} {
		got := stringSliceArg(decode(t, testCase.raw), "libraryIds")
		if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
			t.Errorf("%s read as %v, want %v", testCase.raw, got, testCase.want)
		}
	}

	// A query that is a number is a query. It reached the search as "".
	if got := stringArg(decode(t, `{"query":2024}`), "query"); got != "2024" {
		t.Errorf("a numeric query read as %q", got)
	}
	if got := stringArg(decode(t, `{"query":true}`), "query"); got != "" {
		t.Errorf("a boolean query read as %q", got)
	}
}

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func answerText(t *testing.T, s *Server, body string) string {
	t.Helper()
	return call(t, s, body)["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
}

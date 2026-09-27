package mcp

import (
	"strings"
	"testing"
)

// A file that shows a fenced block of its own must not end the block the answer
// opened around it.
//
// read-file escalated to four backticks when the content held three, and
// get-symbol-context did not escalate at all, so a Python module whose docstring
// documents its usage came back with the fence closed in the middle of the
// docstring: the function body, the citation and the notes after it read as
// prose, and the last fence opened a block that never closes.
func TestFencedContentStaysInsideOneBlock(t *testing.T) {
	s := fixture(t)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A module docstring that shows how the module is used. The example fence sits
	// at column zero, which is where a docstring puts it.
	module := strings.Join([]string{
		`"""GPU probe helpers.`,
		``,
		`Example:`,
		``,
		"```python",
		`from gpu import probe`,
		"```",
		`"""`,
		``,
		`def probe():`,
		`    return None`,
	}, "\n")
	// A README that documents how to write a fenced block has to wrap that block
	// in a longer fence, which the fixed escalation to four backticks cannot hold.
	readme := strings.Join([]string{
		`# Fences`,
		``,
		`Show a Python block like this:`,
		``,
		"````markdown",
		"```python",
		`probe()`,
		"```",
		"````",
	}, "\n")
	must(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash)
		VALUES('s9','r1','main','4fa21bd','gpu/probe.py','probe','probe','function','python','def probe():','Probes the GPU.',10,11,'sh9')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
		VALUES('c9','r1','main','4fa21bd','gpu/probe.py',1,11,'probe','code',?,'h9')`, module)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash)
		VALUES('c10','r1','main','4fa21bd','docs/fences.md',1,9,'Fences','document',?,'h10')`, readme)
	must(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id)
		VALUES('r1','main','docs/fences.md','fences.md',?,1,'4fa21bd')`, len(readme))

	for _, tc := range []struct {
		name, request, wantLine string
	}{
		{
			name:     "get-symbol-context",
			request:  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-symbol-context","arguments":{"libraryId":"/kcb/clustara","ref":"main","symbol":"probe"}}}`,
			wantLine: "def probe():",
		},
		{
			name:     "read-file",
			request:  `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read-file","arguments":{"libraryId":"/kcb/clustara","ref":"main","path":"docs/fences.md"}}}`,
			wantLine: "probe()",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := call(t, s, tc.request)
			result, ok := out["result"].(map[string]any)
			if !ok {
				t.Fatalf("call failed: %#v", out)
			}
			text := result["content"].([]any)[0].(map[string]any)["text"].(string)
			blocks, unterminated := markdownBlocks(text)
			if unterminated {
				t.Errorf("the answer ends inside a code fence that never closes, so everything after it is read as file content:\n%s", text)
			}
			if len(blocks) != 1 {
				t.Errorf("the content was split across %d code blocks, want 1:\n%s", len(blocks), text)
			}
			if len(blocks) == 0 || !strings.Contains(blocks[0], tc.wantLine) {
				t.Errorf("%q is outside the code block the answer opened:\n%s", tc.wantLine, text)
			}
		})
	}
}

// markdownBlocks reads the fenced blocks of an answer the way a client renders
// them: a fence opens on a line of three or more backticks and closes only on a
// later line of at least as many with nothing else on it. The second return
// value reports a block left open at the end of the answer.
func markdownBlocks(text string) ([]string, bool) {
	var blocks []string
	var body []string
	fence := 0
	for _, line := range strings.Split(text, "\n") {
		run := backtickRun(line)
		if fence == 0 {
			if run >= 3 {
				fence = run
			}
			continue
		}
		if run >= fence && strings.TrimRight(line, "`") == "" {
			blocks, body, fence = append(blocks, strings.Join(body, "\n")), nil, 0
			continue
		}
		body = append(body, line)
	}
	if fence == 0 {
		return blocks, false
	}
	return append(blocks, strings.Join(body, "\n")), true
}

func backtickRun(line string) int {
	count := 0
	for count < len(line) && line[count] == '`' {
		count++
	}
	return count
}

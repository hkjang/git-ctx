package app

import (
	"fmt"
	"strings"
	"testing"
)

// A repository holds files this platform does not index — a lock file, an
// image, a bundled script. Their paths are still recorded, so find-file answers
// for them, and read-file fetches them live from the source. Two things about
// that were wrong.
//
// find-file told the reader to use search-code for such a file. search-code
// searches indexed content, so it is the one tool that cannot answer for a file
// whose content was not indexed, and the tool that can — read-file — was not
// mentioned.
//
// When the live read failed, the platform knew why: the source is not
// configured, the adapter would not start, the server refused the connection.
// It worked that reason out, put it in the diagnostics, and then returned an
// error that said only that the read had not worked.

func unindexedFixture() map[string]string {
	files := agreementFixture()
	files["assets/logo.png"] = "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00\xff", 400)
	files["yarn.lock"] = strings.Repeat("# yarn lockfile v1\nexpress@^4.18.0:\n  version \"4.18.2\"\n", 40)
	return files
}

func TestFindFileNamesTheToolThatCanReadAnUnindexedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(unindexedFixture())
	defer source.Close()
	a := indexedApp(t, source.URL, model.URL, "unindexed")

	answer := toolAnswer(t, a, "find-file", `{"libraryId":"/gitlab~core/api","pattern":"*"}`)
	if !strings.Contains(answer, "assets/logo.png") {
		t.Fatalf("the file listing does not carry files that were not indexed: %s", answer)
	}
	for _, line := range strings.Split(answer, "\n") {
		if !strings.Contains(line, "content not indexed") {
			continue
		}
		if strings.Contains(line, "search-code") {
			t.Errorf("an unindexed file points at the one tool that cannot read it: %s", strings.TrimSpace(line))
		}
		if !strings.Contains(line, "read-file") {
			t.Errorf("an unindexed file does not name the tool that can read it: %s", strings.TrimSpace(line))
		}
	}

	// The live read works while the source is up.
	body := toolAnswer(t, a, "read-file", `{"libraryId":"/gitlab~core/api","path":"yarn.lock"}`)
	if !strings.Contains(body, "yarn lockfile") {
		t.Fatalf("an unindexed file was not read live: %s", first(body))
	}
}

func TestAFailedLiveReadSaysWhyItFailed(t *testing.T) {
	if testing.Short() {
		t.Skip("indexing waits on the background worker")
	}
	model := newFakeModelServer()
	defer model.Close()
	source := newFakeGitLab(unindexedFixture())
	a := indexedApp(t, source.URL, model.URL, "sourcedown")
	source.Close()

	answer := toolAnswer(t, a, "read-file", `{"libraryId":"/gitlab~core/api","path":"assets/logo.png"}`)
	for _, expected := range []string{
		// what the platform knows and used to keep to itself
		"never indexed",
		"remote:",
	} {
		if !strings.Contains(answer, expected) {
			t.Errorf("the failure does not say %q: %s", expected, first(answer))
		}
	}
	// An indexed file still answers, from the chunks, and says what they are.
	indexed := toolAnswer(t, a, "read-file", `{"libraryId":"/gitlab~core/api","path":"README.md"}`)
	if !strings.Contains(indexed, "결제") {
		t.Fatalf("an indexed file stopped being readable when the source went away:\n%s", indexed)
	}
	if !strings.Contains(indexed, "not the whole file") {
		t.Fatalf("the chunk reassembly was handed over as if it were the file:\n%s", indexed)
	}
	_ = fmt.Sprint
}

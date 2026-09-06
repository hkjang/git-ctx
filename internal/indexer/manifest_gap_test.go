package indexer

import (
	"strings"
	"testing"

	"git-ctx/internal/manifest"
)

// The index run is the only place that knows a manifest was skipped. If it does
// not carry the parser's note into the job warning, the repository simply has
// fewer dependencies than it has, and find-dependency-usage reports it as one
// that never used the library.
func TestAppendPackagesReportsWhatItCouldNotRead(t *testing.T) {
	whole := `{"dependencies":{"lodash":"4.17.21"}}`
	packages, notes := appendPackages(nil, "package.json", whole)
	if len(packages) != 1 || len(notes) != 0 {
		t.Fatalf("a manifest read whole: %d packages, notes %v", len(packages), notes)
	}

	oversized := whole + strings.Repeat(" ", manifest.MaxManifestBytes)
	packages, notes = appendPackages(nil, "package.json", oversized)
	if len(packages) != 0 {
		t.Fatalf("an oversized manifest contributed %d packages", len(packages))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "package.json") {
		t.Fatalf("notes %v do not name the skipped manifest", notes)
	}

	// A path that is neither a manifest nor a lock file is not a gap.
	if _, notes = appendPackages(nil, "docs/guide.md", "text"); len(notes) != 0 {
		t.Fatalf("unexpected notes for an ordinary file: %v", notes)
	}
}

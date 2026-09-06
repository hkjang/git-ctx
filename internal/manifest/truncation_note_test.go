package manifest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Every bound in this package removes declarations from the inventory, and an
// inventory that is missing a repository answers an advisory the same way a
// repository that never used the library does. The bounds stay; what they
// dropped has to be sayable.

func TestOversizedManifestSaysWhatItDropped(t *testing.T) {
	content := `{"dependencies":{"lodash":"4.17.21"}}` + strings.Repeat(" ", MaxManifestBytes)
	packages, note := ParseNoted("services/api/package.json", content)
	if packages != nil {
		t.Fatalf("an oversized manifest must contribute nothing, got %d packages", len(packages))
	}
	for _, want := range []string{"services/api/package.json", strconv.Itoa(len(content)), strconv.Itoa(MaxManifestBytes), "none of its dependencies are in the inventory"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q does not state %q", note, want)
		}
	}
	// The bound itself is unchanged: the same file still yields no packages.
	if Parse("services/api/package.json", content) != nil {
		t.Fatal("Parse must still skip an oversized manifest")
	}
}

func TestOversizedLockSaysWhatItDropped(t *testing.T) {
	packages, note := ParseLockNoted("go.sum", strings.Repeat("x", MaxLockBytes+1))
	if packages != nil {
		t.Fatalf("an oversized lock must contribute nothing, got %d packages", len(packages))
	}
	for _, want := range []string{"go.sum", strconv.Itoa(MaxLockBytes), "none of its resolved versions are in the inventory"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q does not state %q", note, want)
		}
	}
}

// go.sum is sorted by module path, so the packages a truncation drops are a
// contiguous alphabetical tail — every module late in the alphabet loses its
// resolved version at once, which is the strongest evidence an advisory has.
func TestTruncatedLockSaysHowManyItDropped(t *testing.T) {
	var builder strings.Builder
	extra := 50
	for index := 0; index < MaxLockPackages+extra; index++ {
		fmt.Fprintf(&builder, "example.com/mod%06d v1.0.0 h1:abc=\n", index)
	}
	packages, note := ParseLockNoted("go.sum", builder.String())
	if len(packages) != MaxLockPackages {
		t.Fatalf("kept %d packages, want the bound %d", len(packages), MaxLockPackages)
	}
	for _, want := range []string{"go.sum", strconv.Itoa(MaxLockPackages), strconv.Itoa(extra), "not in the inventory"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q does not state %q", note, want)
		}
	}
}

// A note means "this answer is short of the file". A file read whole must not
// carry one, or an operator learns to ignore the warning that matters.
func TestAFileReadWholeCarriesNoNote(t *testing.T) {
	for _, item := range []struct{ path, content string }{
		{"package.json", `{"dependencies":{"lodash":"4.17.21"}}`},
		{"go.mod", "module x\n\nrequire github.com/gin-gonic/gin v1.9.1\n"},
		{"go.sum", "example.com/mod v1.0.0 h1:abc=\n"},
		{"README.md", "not a manifest"},
		{"src/main.go", "package main"},
	} {
		if _, note := ParseNoted(item.path, item.content); note != "" {
			t.Fatalf("%s: unexpected manifest note %q", item.path, note)
		}
		if _, note := ParseLockNoted(item.path, item.content); note != "" {
			t.Fatalf("%s: unexpected lock note %q", item.path, note)
		}
	}
}

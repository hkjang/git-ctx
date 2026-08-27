package app

import (
	"sort"
	"strings"
	"testing"

	"git-ctx/internal/mcp"
)

// Every tool a key holder can call is either exercised against a restricted key
// or has a recorded reason why not.
//
// Four of the twenty-nine were in neither sweep — get-context-pack, which a
// database fixture can answer perfectly well, and three that need a source
// server — and nothing anywhere said so. A tool nobody has asked the question of
// is not a tool known to answer it correctly, and the gap was invisible from
// either list because neither list knows what the other holds.
func TestEveryCallableToolIsSweptForKeyRestrictions(t *testing.T) {
	swept := map[string]bool{}
	for _, ask := range restrictionTools {
		swept[ask.tool] = true
	}
	for _, ask := range libraryScopedTools {
		swept[ask.tool] = true
	}

	callable := map[string]bool{}
	for _, tool := range mcp.Catalog() {
		name, _ := tool["name"].(string)
		if name != "" {
			callable[name] = true
		}
	}
	if len(callable) < 20 {
		t.Fatalf("the catalogue reports only %d tools, so this is not reading it", len(callable))
	}

	var unchecked []string
	for name := range callable {
		if !swept[name] && sweptElsewhere[name] == "" {
			unchecked = append(unchecked, name)
		}
	}
	sort.Strings(unchecked)
	for _, name := range unchecked {
		t.Errorf("%s is callable with an API key, is in neither restriction sweep, and no reason is recorded for leaving it out", name)
	}

	// A reason for a tool that is swept anyway, or for one that no longer exists,
	// is a comment nobody will check against reality again.
	for name, reason := range sweptElsewhere {
		switch {
		case !callable[name]:
			t.Errorf("%s is excused from the restriction sweep but is not in the catalogue", name)
		case swept[name]:
			t.Errorf("%s is both swept and excused; the excuse is stale: %s", name, reason)
		case len(strings.TrimSpace(reason)) < 40:
			t.Errorf("%s is excused with too little to check: %q", name, reason)
		}
	}
}

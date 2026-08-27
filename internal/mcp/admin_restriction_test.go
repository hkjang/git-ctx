package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"git-ctx/internal/auth"
)

// The administrative tools were tested for the gate in front of them — a role
// the caller must hold — and not for the one inside them. A key can carry an
// administrator role and a repository restriction at the same time, and the
// restriction is the whole reason the key was narrowed: an operator who issues
// a source-admin key scoped to one repository does not expect it to reindex
// another, or to list another's jobs.
//
// Nothing had ever asked. The check is there; this is what asking looks like.
func TestAnAdminKeyIsStillBoundByItsRepositoryRestriction(t *testing.T) {
	s := fixture(t)
	scopes := []string{"get-platform-status", "list-index-jobs", "reindex-repository"}
	unrestricted := auth.Principal{UserID: "u1", Subject: "alice", ACLPrincipal: "alice", KeyID: "key",
		Roles: []string{"source-admin"}, Scopes: scopes}
	restricted := unrestricted
	restricted.AllowedRepositories = []string{"/other/repo"}

	answer := func(p auth.Principal, body string) string {
		t.Helper()
		raw, err := json.Marshal(callAs(t, s, p, body))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	for _, c := range []struct{ name, body string }{
		{"reindex-repository", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"reindex-repository","arguments":{"libraryId":"/kcb/clustara"}}}`},
		{"list-index-jobs", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list-index-jobs","arguments":{}}}`},
	} {
		// The unrestricted key proves the tool can reach this repository at all,
		// so the restricted key finding nothing means something.
		if open := answer(unrestricted, c.body); !strings.Contains(open, "clustara") {
			t.Errorf("%s does not reach the repository even for a key that may: %s", c.name, open)
			continue
		}
		if guarded := answer(restricted, c.body); strings.Contains(guarded, "clustara") {
			t.Errorf("%s served a repository outside the key's restriction: %s", c.name, guarded)
		}
	}

	// The refusal must not say whether the repository exists: an administrator
	// role does not change what a narrowed key is allowed to learn.
	refused := answer(restricted, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"reindex-repository","arguments":{"libraryId":"/kcb/clustara"}}}`)
	if !strings.Contains(refused, "unavailable or access is denied") {
		t.Errorf("the refusal distinguishes a repository that exists from one that does not: %s", refused)
	}

	// Platform status is a whole-installation summary and names no repository;
	// if that ever changes it becomes a leak through an administrative tool.
	status := answer(restricted, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get-platform-status","arguments":{}}}`)
	if strings.Contains(status, "clustara") {
		t.Errorf("get-platform-status names a repository outside the key's restriction: %s", status)
	}
}

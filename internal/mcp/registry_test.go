package mcp

import (
	"testing"

	"git-ctx/internal/auth"
)

// The registry exists so a tool's schema, authorisation and handler cannot drift
// apart. These checks fail the moment an entry is added with a piece missing.
func TestRegistryEntriesAreComplete(t *testing.T) {
	seen := make(map[string]bool, len(registry))
	for i := range registry {
		entry := &registry[i]
		switch {
		case entry.name == "":
			t.Errorf("registry[%d] has no name", i)
		case entry.description == "":
			t.Errorf("%s has no description", entry.name)
		case entry.schema == nil:
			t.Errorf("%s has no input schema", entry.name)
		case entry.handler == nil:
			t.Errorf("%s has no handler", entry.name)
		}
		if seen[entry.name] {
			t.Errorf("%s is registered twice", entry.name)
		}
		seen[entry.name] = true
	}
	if len(registry) == 0 {
		t.Fatal("registry is empty")
	}
}

func TestRegistryIndexResolvesEveryTool(t *testing.T) {
	for i := range registry {
		found, ok := lookupTool(registry[i].name)
		if !ok {
			t.Errorf("%s is not resolvable through the index", registry[i].name)
			continue
		}
		if found != &registry[i] {
			t.Errorf("%s resolves to a different entry", registry[i].name)
		}
	}
	if _, ok := lookupTool("no-such-tool"); ok {
		t.Error("an unknown tool resolved")
	}
}

// Catalog adds a per-call maxBytes property to what it serves. It must copy the
// schema first: the registry entry is shared for the process lifetime, so a
// mutation there would leak into every later call.
func TestCatalogDoesNotMutateRegistrySchemas(t *testing.T) {
	first := Catalog()
	for i := range registry {
		properties, ok := registry[i].schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		if _, leaked := properties["maxBytes"]; leaked {
			t.Fatalf("%s: Catalog wrote maxBytes back into the shared schema", registry[i].name)
		}
	}
	if second := Catalog(); len(second) != len(first) {
		t.Errorf("Catalog is not stable across calls: %d then %d", len(first), len(second))
	}
	for _, tool := range first {
		schema, _ := tool["inputSchema"].(map[string]any)
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		if _, ok := properties["maxBytes"]; !ok {
			t.Errorf("%v: served schema is missing maxBytes", tool["name"])
		}
	}
}

func TestAdminToolsRequireOneOfTheirRoles(t *testing.T) {
	entry, ok := lookupTool("reindex-repository")
	if !ok {
		t.Fatal("reindex-repository is not registered")
	}
	if entry.allowed(auth.Principal{}) {
		t.Error("an anonymous caller was allowed an administrative tool")
	}
	if entry.allowed(auth.Principal{KeyID: "k", Roles: []string{"auditor"}}) {
		t.Error("an unrelated role was allowed an administrative tool")
	}
	if !entry.allowed(auth.Principal{KeyID: "k", Roles: []string{"source-admin"}}) {
		t.Error("source-admin was denied reindex-repository")
	}

	search, _ := lookupTool("search-code")
	if !search.allowed(auth.Principal{}) {
		t.Error("a non-administrative tool was gated on roles")
	}
}

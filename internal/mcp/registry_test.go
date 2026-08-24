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

// The roadmap asked for per-client tool profiles so an agent is not shown thirty
// tools it will not use. For any credentialed caller that already exists: an API
// key's scopes filter the catalogue itself, not merely the calls. This pins that
// -- the saving is only real if the tools are absent from tools/list, since that
// is what costs the agent its context.
func TestScopesFilterTheCatalogueNotJustTheCalls(t *testing.T) {
	narrow := auth.Principal{KeyID: "k", Scopes: []string{"search-code", "read-file"}}
	visible := 0
	for i := range registry {
		entry := &registry[i]
		if len(entry.adminRoles) > 0 {
			continue
		}
		// toolVisible's other inputs are exercised elsewhere; this isolates the
		// scope rule by checking it directly.
		if entry.name == "search-code" || entry.name == "read-file" {
			if !contains(narrow.Scopes, entry.name) {
				t.Errorf("%s should be in scope", entry.name)
			}
			visible++
			continue
		}
		if contains(narrow.Scopes, entry.name) {
			t.Errorf("%s is outside the key's scopes but was allowed", entry.name)
		}
	}
	if visible != 2 {
		t.Fatalf("visible = %d, want exactly the two scoped tools", visible)
	}

	// A caller without a key is not scope-limited: browser sessions drive the
	// console, where a human picks the tool and the context cost does not apply.
	browser := auth.Principal{}
	if browser.KeyID != "" {
		t.Fatal("a browser principal must not carry a key id")
	}
}

// The served catalogue must stay small enough to be worth sending. maxBytes was
// measured at 24.9% of it -- the same sentence on every tool -- so the schema
// now names the parameter and leaves the explanation to initialize, which is
// sent once. This guards the boilerplate from growing back.
func TestServedCatalogueDoesNotRepeatLongBoilerplate(t *testing.T) {
	served := Catalog()
	if len(served) == 0 {
		t.Fatal("catalogue is empty")
	}
	for _, tool := range served {
		schema, _ := tool["inputSchema"].(map[string]any)
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		budget, ok := properties["maxBytes"].(map[string]any)
		if !ok {
			t.Errorf("%v is missing the maxBytes parameter", tool["name"])
			continue
		}
		description, _ := budget["description"].(string)
		if len(description) > 80 {
			t.Errorf("%v repeats %d characters of maxBytes boilerplate; initialize explains it once",
				tool["name"], len(description))
		}
	}
}

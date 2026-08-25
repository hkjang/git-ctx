package manifest

import (
	"strings"
	"testing"
)

// A lock file is what makes an undecidable range decidable, so every supported
// format has to yield the resolved version and mark it as such.
func TestParseLockResolvesVersions(t *testing.T) {
	cases := map[string]struct {
		path, content string
		want          map[string]string
	}{
		"go.sum": {
			path: "go.sum",
			content: `github.com/gin-gonic/gin v1.10.0 h1:abc=
github.com/gin-gonic/gin v1.10.0/go.mod h1:def=
golang.org/x/sync v0.10.0 h1:ghi=
`,
			want: map[string]string{"github.com/gin-gonic/gin": "v1.10.0", "golang.org/x/sync": "v0.10.0"},
		},
		"package-lock v3": {
			path: "web/package-lock.json",
			content: `{"lockfileVersion":3,"packages":{
              "": {"name":"console"},
              "node_modules/react": {"version":"18.3.1"},
              "node_modules/lodash": {"version":"4.17.21"},
              "node_modules/a/node_modules/lodash": {"version":"4.17.20"}
            }}`,
			// The nested copy is reported too: it is what the build would load for
			// that dependency, and an advisory has to see it.
			want: map[string]string{"react": "18.3.1", "lodash": "4.17.21"},
		},
		"package-lock v1": {
			path:    "package-lock.json",
			content: `{"lockfileVersion":1,"dependencies":{"react":{"version":"18.2.0","dependencies":{"scheduler":{"version":"0.23.0"}}}}}`,
			want:    map[string]string{"react": "18.2.0", "scheduler": "0.23.0"},
		},
		"yarn.lock": {
			path: "yarn.lock",
			content: `# yarn lockfile v1

"@scope/pkg@^1.0.0":
  version "1.2.3"
  resolved "https://registry/x"

lodash@^4.17.0, lodash@^4.17.21:
  version "4.17.21"
`,
			want: map[string]string{"@scope/pkg": "1.2.3", "lodash": "4.17.21"},
		},
		"Cargo.lock": {
			path: "Cargo.lock",
			content: `[[package]]
name = "serde"
version = "1.0.197"

[[package]]
name = "tokio"
version = "1.35.1"
`,
			want: map[string]string{"serde": "1.0.197", "tokio": "1.35.1"},
		},
		"poetry.lock": {
			path: "poetry.lock",
			content: `[[package]]
name = "httpx"
version = "0.27.0"
description = "client"
`,
			want: map[string]string{"httpx": "0.27.0"},
		},
	}
	for label, item := range cases {
		packages := ParseLock(item.path, item.content)
		// A nested copy at a different version is kept deliberately: during an
		// advisory a vulnerable transitive copy matters as much as the top-level
		// one, so the pair (name, version) is what must be present.
		found := map[string]bool{}
		for _, entry := range packages {
			if entry.Scope != "resolved" {
				t.Fatalf("%s: a lock entry must be marked resolved: %#v", label, entry)
			}
			if entry.Ecosystem == "" {
				t.Fatalf("%s: ecosystem missing: %#v", label, entry)
			}
			found[entry.Name+"@"+entry.Version] = true
		}
		for name, version := range item.want {
			if !found[name+"@"+version] {
				t.Fatalf("%s: %s@%s missing (got %#v)", label, name, version, packages)
			}
		}
	}
}

func TestRecognizeLockAndBounds(t *testing.T) {
	if _, ok := RecognizeLock("go.mod"); ok {
		t.Fatal("a manifest is not a lock file")
	}
	if ecosystem, ok := RecognizeLock("services/api/go.sum"); !ok || ecosystem != "go" {
		t.Fatalf("ecosystem=%s ok=%v", ecosystem, ok)
	}
	if ParseLock("go.sum", strings.Repeat("x", MaxLockBytes+1)) != nil {
		t.Fatal("an oversized lock file must be skipped")
	}
	// A very large lock contributes a bounded number of packages.
	var builder strings.Builder
	for index := 0; index < MaxLockPackages+50; index++ {
		builder.WriteString("example.com/mod")
		builder.WriteString(strings.Repeat("x", 1+index%3))
		builder.WriteString(" v1.0.")
		builder.WriteString(strings.TrimSpace(strings.Repeat("1", 1+index%4)))
		builder.WriteString(" h1:abc=\n")
	}
	if got := len(ParseLock("go.sum", builder.String())); got > MaxLockPackages {
		t.Fatalf("lock contributed %d packages", got)
	}
}

// A nested copy pinned to another version is a separate fact, not a duplicate:
// the top-level dependency may be patched while a transitive copy is not.
func TestPackageLockKeepsNestedCopies(t *testing.T) {
	packages := ParseLock("package-lock.json", `{"lockfileVersion":3,"packages":{
      "node_modules/lodash": {"version":"4.17.21"},
      "node_modules/a/node_modules/lodash": {"version":"4.17.20"}
    }}`)
	versions := map[string]bool{}
	for _, entry := range packages {
		if entry.Name == "lodash" {
			versions[entry.Version] = true
		}
	}
	if !versions["4.17.21"] || !versions["4.17.20"] {
		t.Fatalf("both copies must be reported: %#v", packages)
	}
}

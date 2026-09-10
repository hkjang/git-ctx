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

// Poetry is one of four resolvers a Python project picks from, and its lock was
// the only one read. A pyproject states ranges and a range is what an advisory
// cannot judge, so a repository resolving with uv, PDM or pipenv contributed the
// question and never the answer.
func TestThePythonLockFilesAProjectActuallyHas(t *testing.T) {
	cases := map[string]struct {
		path, content string
		want          map[string]string
		absent        []string
	}{
		"uv.lock": {
			path: "uv.lock",
			content: `version = 1
requires-python = ">=3.11"

[[package]]
name = "requests"
version = "2.31.0"
source = { registry = "https://pypi.org/simple" }
dependencies = [
    { name = "certifi" },
    { name = "urllib3" },
]

[package.metadata]
requires-dist = [{ name = "urllib3", specifier = ">=1.21.1" }]

[[package]]
name = "urllib3"
version = "2.2.1"
source = { registry = "https://pypi.org/simple" }
`,
			want: map[string]string{"requests": "2.31.0", "urllib3": "2.2.1"},
			// The names inside a dependencies array or a metadata table describe the
			// edges of the graph, not resolved packages of their own.
			absent: []string{"certifi"},
		},
		"pdm.lock": {
			path: "pdm.lock",
			content: `[metadata]
groups = ["default"]
lock_version = "4.4.1"
content_hash = "sha256:abc"

[[package]]
name = "asgiref"
version = "3.7.2"
requires_python = ">=3.7"

[[package]]
name = "django"
version = "4.2.11"
`,
			want: map[string]string{"asgiref": "3.7.2", "django": "4.2.11"},
		},
		"Pipfile.lock": {
			path: "Pipfile.lock",
			content: `{
  "_meta": {
    "hash": {"sha256": "abc"},
    "pipfile-spec": 6,
    "requires": {"python_version": "3.11"},
    "sources": [{"name": "pypi", "url": "https://pypi.org/simple", "verify_ssl": true}]
  },
  "default": {
    "flask": {"hashes": ["sha256:abc"], "index": "pypi", "version": "==3.0.2"},
    "internal-lib": {"git": "https://example.com/lib.git", "ref": "abc123"}
  },
  "develop": {
    "pytest": {"hashes": ["sha256:def"], "version": "==8.1.1"}
  }
}`,
			// _meta is not a set of packages, and decoding it as one would have cost
			// the whole file rather than that one key.
			want:   map[string]string{"flask": "3.0.2", "pytest": "8.1.1"},
			absent: []string{"_meta", "hash", "sources", "internal-lib"},
		},
	}
	for label, item := range cases {
		if ecosystem, ok := RecognizeLock(item.path); !ok || ecosystem != "pypi" {
			t.Fatalf("%s: ecosystem=%q ok=%v", label, ecosystem, ok)
		}
		found := map[string]string{}
		for _, entry := range ParseLock(item.path, item.content) {
			if entry.Scope != "resolved" || entry.Ecosystem != "pypi" {
				t.Fatalf("%s: %#v", label, entry)
			}
			found[entry.Name] = entry.Version
		}
		for name, version := range item.want {
			if found[name] != version {
				t.Fatalf("%s: want %s@%s, got %#v", label, name, version, found)
			}
		}
		for _, name := range item.absent {
			if _, ok := found[name]; ok {
				t.Fatalf("%s: %s is not a resolved package: %#v", label, name, found)
			}
		}
	}
}

// pipenv writes the pin as the requirement it would install. Left as written it
// groups apart from the same release resolved by any other tool, and nothing
// compares it against the version an advisory says the fix landed in.
func TestPipfileLockStatesTheVersionAlone(t *testing.T) {
	packages := ParseLock("Pipfile.lock", `{"default":{"requests":{"version":"==2.31.0"}}}`)
	if len(packages) != 1 || packages[0].Version != "2.31.0" {
		t.Fatalf("%#v", packages)
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

// pnpm has shipped three key layouts. Reading only the newest would leave every
// repository on an older lock file silently absent from the inventory, which is
// the failure this feature exists to prevent.
func TestPnpmLockLayouts(t *testing.T) {
	layouts := map[string]string{
		"v6 slash": `lockfileVersion: '6.0'

packages:

  /lodash/4.17.21:
    resolution: {integrity: sha512-abc}
    dev: false

  /@babel/core/7.24.0:
    resolution: {integrity: sha512-def}
    dev: true
`,
		"v9 at": `lockfileVersion: '9.0'

packages:

  lodash@4.17.21:
    resolution: {integrity: sha512-abc}

  '@babel/core@7.24.0':
    resolution: {integrity: sha512-def}
`,
		"peer suffix": `packages:

  /react-dom/18.2.0(react@18.2.0):
    resolution: {integrity: sha512-ghi}
`,
	}
	for label, content := range layouts {
		found := map[string]string{}
		for _, item := range ParseLock("pnpm-lock.yaml", content) {
			if item.Scope != "resolved" || item.Ecosystem != "npm" {
				t.Fatalf("%s: %#v", label, item)
			}
			found[item.Name] = item.Version
		}
		switch label {
		case "peer suffix":
			// The parenthesised peer context is not part of the version.
			if found["react-dom"] != "18.2.0" {
				t.Fatalf("%s: %#v", label, found)
			}
		default:
			if found["lodash"] != "4.17.21" || found["@babel/core"] != "7.24.0" {
				t.Fatalf("%s: %#v", label, found)
			}
		}
	}
}

// yarn berry writes a different header and version form than yarn v1, and both
// are in use. Reading only one would leave repositories on the other absent
// from the inventory without any sign.
func TestYarnBerryLayout(t *testing.T) {
	found := map[string]string{}
	for _, item := range ParseLock("yarn.lock", `# This file is generated by running "yarn install"
__metadata:
  version: 6

"lodash@npm:^4.17.21":
  version: 4.17.21
  resolution: "lodash@npm:4.17.21"

"@babel/core@npm:^7.24.0":
  version: 7.24.0
  resolution: "@babel/core@npm:7.24.0"
`) {
		found[item.Name] = item.Version
	}
	if found["lodash"] != "4.17.21" || found["@babel/core"] != "7.24.0" {
		t.Fatalf("berry=%#v", found)
	}
	// The metadata block is not a package.
	if _, ok := found["__metadata"]; ok {
		t.Fatalf("metadata was inventoried: %#v", found)
	}
}

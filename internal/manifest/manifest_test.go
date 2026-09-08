package manifest

import (
	"sort"
	"strings"
	"testing"
)

func find(packages []Package, name string) (Package, bool) {
	for _, item := range packages {
		if item.Name == name {
			return item, true
		}
	}
	return Package{}, false
}

// go.mod is the one manifest that states directly what a team chose and what it
// inherited, and an upgrade plan is only actionable with that distinction.
func TestGoModSeparatesDirectFromInherited(t *testing.T) {
	packages := Parse("go.mod", `module git-ctx

go 1.25

require (
	github.com/jackc/pgx/v5 v5.7.2
	github.com/mattn/go-sqlite3 v1.14.24
	golang.org/x/crypto v0.31.0 // indirect
)

require go.opentelemetry.io/otel v1.33.0

// require github.com/commented/out v1.0.0
`)
	if len(packages) != 4 {
		t.Fatalf("packages=%#v", packages)
	}
	direct, ok := find(packages, "github.com/jackc/pgx/v5")
	if !ok || direct.Version != "v5.7.2" || direct.Scope != "direct" {
		t.Fatalf("direct=%#v", direct)
	}
	indirect, _ := find(packages, "golang.org/x/crypto")
	if indirect.Scope != "transitive" {
		t.Fatalf("the indirect marker must be kept: %#v", indirect)
	}
	if single, ok := find(packages, "go.opentelemetry.io/otel"); !ok || single.Version != "v1.33.0" {
		t.Fatalf("a single-line require must be read: %#v", single)
	}
	if _, ok := find(packages, "github.com/commented/out"); ok {
		t.Fatal("a commented require must not be inventoried")
	}
}

func TestPackageJSONKeepsScopes(t *testing.T) {
	packages := Parse("web/package.json", `{
      "name": "console",
      "dependencies": {"react": "^18.2.0", "lodash": "4.17.21"},
      "devDependencies": {"vitest": "^1.0.0"},
      "peerDependencies": {"typescript": ">=5"}
    }`)
	if len(packages) != 4 {
		t.Fatalf("packages=%#v", packages)
	}
	if item, _ := find(packages, "lodash"); item.Version != "4.17.21" || item.Scope != "direct" {
		t.Fatalf("lodash=%#v", item)
	}
	if item, _ := find(packages, "vitest"); item.Scope != "dev" {
		t.Fatalf("vitest=%#v", item)
	}
	if Parse("package.json", "{not json") != nil {
		t.Fatal("a broken manifest must yield nothing rather than garbage")
	}
}

// A Maven version is usually a property reference, so an inventory that stores
// the placeholder would answer an advisory check with "${log4j.version}".
func TestPOMResolvesPropertyVersions(t *testing.T) {
	packages := Parse("pom.xml", `<project>
  <properties><log4j.version>2.17.1</log4j.version></properties>
  <dependencies>
    <dependency>
      <groupId>org.apache.logging.log4j</groupId>
      <artifactId>log4j-core</artifactId>
      <version>${log4j.version}</version>
    </dependency>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>5.10.0</version>
      <scope>test</scope>
    </dependency>
    <dependency>
      <groupId>com.example</groupId>
      <artifactId>managed</artifactId>
      <version>${undefined.version}</version>
    </dependency>
  </dependencies>
</project>`)
	if len(packages) != 3 {
		t.Fatalf("packages=%#v", packages)
	}
	if item, _ := find(packages, "org.apache.logging.log4j:log4j-core"); item.Version != "2.17.1" {
		t.Fatalf("property version unresolved: %#v", item)
	}
	if item, _ := find(packages, "org.junit.jupiter:junit-jupiter"); item.Scope != "test" {
		t.Fatalf("junit=%#v", item)
	}
	if item, _ := find(packages, "com.example:managed"); item.Version != "" {
		t.Fatalf("an unresolved placeholder must be reported as unknown, got %q", item.Version)
	}
}

// An exclusion is the opposite of a dependency, and the inventory has to read it
// that way round. Reading the block as a whole let the excluded coordinates win,
// so the advisory named the repository for the library it had removed and never
// mentioned the one it actually depends on.
func TestPOMExclusionIsNotADependency(t *testing.T) {
	packages := Parse("pom.xml", `<project>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter</artifactId>
      <version>2.7.18</version>
      <exclusions>
        <exclusion>
          <groupId>org.apache.logging.log4j</groupId>
          <artifactId>log4j-core</artifactId>
        </exclusion>
        <exclusion>
          <groupId>ch.qos.logback</groupId>
          <artifactId>logback-classic</artifactId>
        </exclusion>
      </exclusions>
    </dependency>
    <dependency>
      <groupId>com.example</groupId>
      <artifactId>tool</artifactId>
      <version>1.0.0</version>
      <exclusions>
        <exclusion>
          <groupId>commons-logging</groupId>
          <artifactId>commons-logging</artifactId>
        </exclusion>
      </exclusions>
      <optional>true</optional>
    </dependency>
  </dependencies>
</project>`)
	if len(packages) != 2 {
		t.Fatalf("packages=%#v", packages)
	}
	starter, ok := find(packages, "org.springframework.boot:spring-boot-starter")
	if !ok || starter.Version != "2.7.18" || starter.Scope != "direct" {
		t.Fatalf("the declared dependency must survive its exclusions: %#v", starter)
	}
	for _, excluded := range []string{
		"org.apache.logging.log4j:log4j-core",
		"ch.qos.logback:logback-classic",
		"commons-logging:commons-logging",
	} {
		if item, ok := find(packages, excluded); ok {
			t.Fatalf("%s is excluded, not depended on: %#v", excluded, item)
		}
	}
	if item, _ := find(packages, "com.example:tool"); item.Version != "1.0.0" || item.Scope != "optional" {
		t.Fatalf("a field after the exclusions must still be read: %#v", item)
	}
}

func TestGradleRequirementsCargoAndPyProject(t *testing.T) {
	gradle := Parse("build.gradle", `dependencies {
  implementation 'com.squareup.okhttp3:okhttp:4.12.0'
  testImplementation("org.mockito:mockito-core:5.7.0")
  implementation project(':shared')
}`)
	if len(gradle) != 2 {
		t.Fatalf("gradle=%#v", gradle)
	}
	if item, _ := find(gradle, "org.mockito:mockito-core"); item.Scope != "test" || item.Version != "5.7.0" {
		t.Fatalf("mockito=%#v", item)
	}

	requirements := Parse("requirements.txt", `# comment
Django==4.2.7
requests>=2.31.0  # inline comment
urllib3
-r other.txt
`)
	if len(requirements) != 3 {
		t.Fatalf("requirements=%#v", requirements)
	}
	if item, _ := find(requirements, "Django"); item.Version != "==4.2.7" {
		t.Fatalf("django=%#v", item)
	}
	if item, _ := find(requirements, "urllib3"); item.Version != "" {
		t.Fatalf("an unpinned requirement must have no version: %#v", item)
	}

	cargo := Parse("Cargo.toml", `[package]
name = "svc"
version = "0.1.0"

[dependencies]
serde = "1.0"
tokio = { version = "1.35", features = ["full"] }

[dev-dependencies]
criterion = "0.5"
`)
	if len(cargo) != 3 {
		t.Fatalf("cargo=%#v", cargo)
	}
	if item, _ := find(cargo, "tokio"); item.Version != "1.35" {
		t.Fatalf("an inline table version must be read: %#v", item)
	}
	if item, _ := find(cargo, "criterion"); item.Scope != "dev" {
		t.Fatalf("criterion=%#v", item)
	}
	// The package's own version is not a dependency.
	if _, ok := find(cargo, "svc"); ok {
		t.Fatal("the [package] section must not be inventoried")
	}

	pyproject := Parse("pyproject.toml", `[project]
name = "svc"
dependencies = ["fastapi>=0.110", "pydantic==2.6.0"]

[tool.poetry.dependencies]
python = "^3.11"
httpx = "0.27.0"
`)
	names := make([]string, 0, len(pyproject))
	for _, item := range pyproject {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "fastapi,httpx,pydantic" {
		t.Fatalf("pyproject=%v", names)
	}
}

// A dependency does not have to be a line in the [dependencies] table. Cargo
// gives one a section of its own, collects versions at the workspace root, and
// scopes them per target; Poetry 1.2 moved dev dependencies into named groups.
// Read as exact section names, all of those declarations were dropped, and a
// member crate's `serde.workspace = true` entered the inventory as an empty
// version for a package named "serde.workspace".
func TestTOMLDependenciesStatedUnderAPath(t *testing.T) {
	cargo := Parse("Cargo.toml", `[package]
name = "svc"
version = "0.1.0"

[workspace.dependencies]
serde = { version = "1.0.203", features = ["derive"] }

[dependencies]
tokio.workspace = true
anyhow.version = "1.0.86"
anyhow.features = ["backtrace"]

[dependencies.reqwest]
version = "0.12.4"
features = ["json"]
default-features = false

[target.'cfg(unix)'.dependencies]
nix = "0.29"

[dev-dependencies.criterion]
version = "0.5.1"
`)
	for _, want := range []Package{
		{Ecosystem: "cargo", Name: "serde", Version: "1.0.203", Scope: "direct"},
		{Ecosystem: "cargo", Name: "tokio", Version: "", Scope: "direct"},
		{Ecosystem: "cargo", Name: "anyhow", Version: "1.0.86", Scope: "direct"},
		{Ecosystem: "cargo", Name: "reqwest", Version: "0.12.4", Scope: "direct"},
		{Ecosystem: "cargo", Name: "nix", Version: "0.29", Scope: "direct"},
		{Ecosystem: "cargo", Name: "criterion", Version: "0.5.1", Scope: "dev"},
	} {
		item, ok := find(cargo, want.Name)
		if !ok || item != want {
			t.Fatalf("%s: got %#v want %#v", want.Name, item, want)
		}
	}
	// The fields of a dependency are not dependencies of their own.
	for _, excluded := range []string{"tokio.workspace", "anyhow.version", "version", "features", "default-features", "svc"} {
		if item, ok := find(cargo, excluded); ok {
			t.Fatalf("%s is not a package: %#v", excluded, item)
		}
	}
	if len(cargo) != 6 {
		t.Fatalf("cargo=%#v", cargo)
	}

	pyproject := Parse("pyproject.toml", `[tool.poetry.dependencies]
httpx = "0.27.0"

[tool.poetry.dependencies.urllib3]
version = "2.2.1"
extras = ["socks"]

[tool.poetry.group.dev.dependencies]
black = "24.4.2"

[tool.poetry.group.test.dependencies]
pytest = "^8.2"
`)
	for _, want := range []Package{
		{Ecosystem: "pypi", Name: "httpx", Version: "0.27.0", Scope: "direct"},
		{Ecosystem: "pypi", Name: "urllib3", Version: "2.2.1", Scope: "direct"},
		{Ecosystem: "pypi", Name: "black", Version: "24.4.2", Scope: "dev"},
		{Ecosystem: "pypi", Name: "pytest", Version: "^8.2", Scope: "test"},
	} {
		item, ok := find(pyproject, want.Name)
		if !ok || item != want {
			t.Fatalf("%s: got %#v want %#v", want.Name, item, want)
		}
	}
	if len(pyproject) != 4 {
		t.Fatalf("pyproject=%#v", pyproject)
	}
}

// A pyproject states dependencies in several arrays, and only one of them was
// read. Worse, that one ended at the first closing bracket, which a requirement
// carries itself when it names an extra — "black[jupyter]" swallowed the array
// and everything declared after it was silently absent from the inventory.
func TestPyProjectReadsEveryDependencyArray(t *testing.T) {
	packages := Parse("pyproject.toml", `[build-system]
requires = ["setuptools>=69"]

[project]
name = "svc"
classifiers = ["Programming Language :: Python"]
dependencies = [
  "black[jupyter]>=23.0",  # an extra is not the end of the array]
  "requests>=2.31.0",
  "urllib3",
]

[project.optional-dependencies]
docs = ["sphinx>=7.3"]
dev = ["pytest>=7.4", "mypy==1.9.0"]

[dependency-groups]
test = ["coverage>=7.5"]
lint = ["ruff>=0.4"]

[tool.pdm.dev-dependencies]
tooling = ["tox>=4"]
`)
	for _, want := range []Package{
		{Ecosystem: "pypi", Name: "black", Version: ">=23.0", Scope: "direct"},
		{Ecosystem: "pypi", Name: "requests", Version: ">=2.31.0", Scope: "direct"},
		{Ecosystem: "pypi", Name: "urllib3", Version: "", Scope: "direct"},
		{Ecosystem: "pypi", Name: "sphinx", Version: ">=7.3", Scope: "optional"},
		{Ecosystem: "pypi", Name: "pytest", Version: ">=7.4", Scope: "optional"},
		{Ecosystem: "pypi", Name: "mypy", Version: "==1.9.0", Scope: "optional"},
		{Ecosystem: "pypi", Name: "coverage", Version: ">=7.5", Scope: "test"},
		{Ecosystem: "pypi", Name: "ruff", Version: ">=0.4", Scope: "dev"},
		{Ecosystem: "pypi", Name: "tox", Version: ">=4", Scope: "dev"},
	} {
		item, ok := find(packages, want.Name)
		if !ok || item != want {
			t.Fatalf("%s: got %#v want %#v", want.Name, item, want)
		}
	}
	// Not every array in a pyproject holds dependencies of the repository.
	for _, excluded := range []string{"setuptools", "Programming", "svc"} {
		if item, ok := find(packages, excluded); ok {
			t.Fatalf("%s is not a declared dependency: %#v", excluded, item)
		}
	}
	if len(packages) != 9 {
		t.Fatalf("packages=%#v", packages)
	}
}

func TestRecognizeAndBounds(t *testing.T) {
	if _, ok := Recognize("internal/app/app.go"); ok {
		t.Fatal("source files are not manifests")
	}
	if ecosystem, ok := Recognize("services/api/go.mod"); !ok || ecosystem != "go" {
		t.Fatalf("ecosystem=%s ok=%v", ecosystem, ok)
	}
	// A lock file committed under a manifest name must not be parsed at length.
	if Parse("package.json", strings.Repeat("x", MaxManifestBytes+1)) != nil {
		t.Fatal("an oversized manifest must be skipped")
	}
}

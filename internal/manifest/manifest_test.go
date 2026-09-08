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

// A build script states a dependency in more than one shape, and the ones the
// parser used to miss are the ones an advisory needs most: the map notation
// older Groovy builds write, and the per-variant configurations the Android and
// Kotlin plugins generate. A repository that declares a library only that way
// was absent from the inventory, which reads exactly like a repository that
// does not use the library at all.
func TestGradleReadsEveryDeclarationShape(t *testing.T) {
	packages := Parse("build.gradle", `dependencies {
  implementation 'com.squareup.okhttp3:okhttp:4.12.0'
  implementation group: 'org.apache.logging.log4j', name: 'log4j-core', version: '2.14.1'
  androidTestImplementation 'androidx.test:runner:1.5.2'
  debugImplementation "com.squareup.leakcanary:leakcanary-android:2.9"
  kapt 'com.google.dagger:dagger-compiler:2.44'
  testRuntimeOnly("org.junit.jupiter:junit-jupiter-engine:5.9.1")
  compileOnlyApi 'org.projectlombok:lombok:1.18.30'
  testCompile group: 'junit', name: 'junit', version: '4.13.2'
}`)
	for _, expected := range []Package{
		{Ecosystem: "gradle", Name: "com.squareup.okhttp3:okhttp", Version: "4.12.0", Scope: "direct"},
		{Ecosystem: "gradle", Name: "org.apache.logging.log4j:log4j-core", Version: "2.14.1", Scope: "direct"},
		{Ecosystem: "gradle", Name: "androidx.test:runner", Version: "1.5.2", Scope: "test"},
		{Ecosystem: "gradle", Name: "com.squareup.leakcanary:leakcanary-android", Version: "2.9", Scope: "direct"},
		{Ecosystem: "gradle", Name: "com.google.dagger:dagger-compiler", Version: "2.44", Scope: "direct"},
		{Ecosystem: "gradle", Name: "org.junit.jupiter:junit-jupiter-engine", Version: "5.9.1", Scope: "test"},
		{Ecosystem: "gradle", Name: "org.projectlombok:lombok", Version: "1.18.30", Scope: "direct"},
		{Ecosystem: "gradle", Name: "junit:junit", Version: "4.13.2", Scope: "test"},
	} {
		item, ok := find(packages, expected.Name)
		if !ok {
			t.Fatalf("%s was not read: %#v", expected.Name, packages)
		}
		if item != expected {
			t.Fatalf("%s=%#v, want %#v", expected.Name, item, expected)
		}
	}
	if len(packages) != 8 {
		t.Fatalf("packages=%#v", packages)
	}
}

// The inventory answers "which third party do you depend on", so what a build
// script says about itself, about a library it removes, or about a coordinate
// it never spells out must stay out of it.
func TestGradleLeavesOutWhatIsNotADeclaredLibrary(t *testing.T) {
	packages := Parse("build.gradle", `plugins {
  id 'org.springframework.boot' version '2.7.18'
}
repositories {
  mavenCentral()
}
dependencies {
  implementation project(':shared')
  implementation platform('org.junit:junit-bom:5.9.1')
  implementation libs.spring.boot.starter
  implementation 'org.hibernate:hibernate-core:6.2.7.Final', {
    exclude group: 'org.jboss.logging', name: 'jboss-logging'
  }
  api "org.springframework:spring-core:$springVersion"
}
tasks.named('compileJava') {
  options.encoding = 'UTF-8'
}`)
	if len(packages) != 2 {
		t.Fatalf("packages=%#v", packages)
	}
	if item, _ := find(packages, "org.hibernate:hibernate-core"); item.Version != "6.2.7.Final" {
		t.Fatalf("a coordinate with a trailing exclusion closure must still be read: %#v", item)
	}
	if _, ok := find(packages, "org.jboss.logging:jboss-logging"); ok {
		t.Fatalf("an excluded library is not a dependency: %#v", packages)
	}
	// An interpolated version names a variable, not a release, and every
	// repository writing a different variable name would otherwise land in its
	// own version group and answer nothing.
	if item, _ := find(packages, "org.springframework:spring-core"); item.Version != "" {
		t.Fatalf("an interpolated version must be left empty: %#v", item)
	}
}

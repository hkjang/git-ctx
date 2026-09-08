// Package manifest reads the dependency manifests of a repository.
//
// The platform already indexes source code and the imports inside it, which
// answers "who calls this function". It could not answer the question an
// operator asks in an incident or an upgrade: "which repositories depend on
// this library, and at which version". That answer lives in files the platform
// already downloads — go.mod, package.json, pom.xml — and nowhere else, because
// an import line names a package but never its version.
//
// The parsers here are deliberately tolerant: a manifest that cannot be read
// completely still yields the dependencies it did state. Reporting nothing
// because one line was unusual would make the inventory quietly incomplete,
// which is worse than a partial answer that says so.
package manifest

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// Package is one declared dependency.
type Package struct {
	// Ecosystem is the package manager: go, npm, maven, pypi, cargo, gradle.
	Ecosystem string
	// Name is the package identifier as the ecosystem writes it, so it can be
	// matched against an advisory: "github.com/gin-gonic/gin", "lodash",
	// "org.apache.logging.log4j:log4j-core".
	Name string
	// Version is the declared version or range, empty when the manifest leaves
	// it to a lock file or a parent POM.
	Version string
	// Scope separates what ships from what only builds or tests: "direct",
	// "transitive", "dev", "test", "optional".
	Scope string
}

// Recognize reports the ecosystem of a manifest path, if it is one.
func Recognize(filePath string) (string, bool) {
	switch strings.ToLower(path.Base(filePath)) {
	case "go.mod":
		return "go", true
	case "package.json":
		return "npm", true
	case "pom.xml":
		return "maven", true
	case "build.gradle", "build.gradle.kts":
		return "gradle", true
	case "pyproject.toml":
		return "pypi", true
	case "cargo.toml":
		return "cargo", true
	}
	if _, ok := requirementsFile(filePath); ok {
		return "pypi", true
	}
	return "", false
}

// requirementsFile reports whether a path names a pip requirements file, and
// what that file is for. A project rarely has exactly one: pip-tools writes
// requirements.in beside the requirements.txt it compiles from it, teams split
// the set by environment, and the layout most Django projects start from puts
// one file per environment under requirements/. Recognizing two exact names
// left every one of those repositories out of the inventory, where a repository
// whose pins were never read looks exactly like one that does not use the
// library at all.
func requirementsFile(filePath string) (string, bool) {
	base := strings.ToLower(path.Base(filePath))
	stem, ok := strings.CutSuffix(base, ".txt")
	if !ok {
		if stem, ok = strings.CutSuffix(base, ".in"); !ok {
			return "", false
		}
	}
	named := stem == "requirements" ||
		strings.HasPrefix(stem, "requirements-") ||
		strings.HasPrefix(stem, "requirements_") ||
		strings.HasPrefix(stem, "requirements.") ||
		strings.HasSuffix(stem, "-requirements") ||
		strings.HasSuffix(stem, "_requirements")
	if !named && strings.ToLower(path.Base(path.Dir(filePath))) != "requirements" {
		return "", false
	}
	return requirementsScope(stem), true
}

// requirementsScope reads what the file name says the set is for. Only the name
// separates the tooling a repository tests with from what its service ships,
// and an upgrade plan needs that distinction the same way go.mod's `// indirect`
// marker gives it.
func requirementsScope(stem string) string {
	for _, part := range strings.FieldsFunc(stem, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		switch part {
		case "test", "tests", "testing":
			return "test"
		case "dev", "develop", "development", "local":
			return "dev"
		}
	}
	return "direct"
}

// MaxManifestBytes bounds one manifest read. A lock file committed as a
// manifest can be megabytes, and nothing useful is beyond this.
const MaxManifestBytes = 1 << 20

// Parse extracts the dependencies declared by one manifest.
func Parse(filePath, content string) []Package {
	ecosystem, ok := Recognize(filePath)
	if !ok || len(content) > MaxManifestBytes {
		return nil
	}
	switch ecosystem {
	case "go":
		return parseGoMod(content)
	case "npm":
		return parsePackageJSON(content)
	case "maven":
		return parsePOM(content)
	case "gradle":
		return parseGradle(content)
	case "pypi":
		if scope, ok := requirementsFile(filePath); ok {
			return parseRequirements(content, scope)
		}
		return parsePyProject(content)
	case "cargo":
		return parseCargo(content)
	default:
		return nil
	}
}

var goRequireLine = regexp.MustCompile(`^\s*([^\s/]+(?:/[^\s]+)*)\s+(v[^\s]+)(.*)$`)

// parseGoMod reads require blocks and single-line requires. The `// indirect`
// marker is what separates a dependency a team chose from one it inherited, and
// an upgrade plan needs that distinction.
func parseGoMod(content string) []Package {
	var out []Package
	inBlock := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "//"):
			continue
		case strings.HasPrefix(line, "require ("):
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
		case !inBlock:
			continue
		}
		match := goRequireLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		scope := "direct"
		if strings.Contains(match[3], "indirect") {
			scope = "transitive"
		}
		out = append(out, Package{Ecosystem: "go", Name: match[1], Version: match[2], Scope: scope})
	}
	return out
}

func parsePackageJSON(content string) []Package {
	var document struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if json.Unmarshal([]byte(content), &document) != nil {
		return nil
	}
	var out []Package
	for scope, set := range map[string]map[string]string{
		"direct":   document.Dependencies,
		"dev":      document.DevDependencies,
		"peer":     document.PeerDependencies,
		"optional": document.OptionalDependencies,
	} {
		for name, version := range set {
			out = append(out, Package{Ecosystem: "npm", Name: name, Version: version, Scope: scope})
		}
	}
	return out
}

var (
	pomDependency = regexp.MustCompile(`(?s)<dependency>(.*?)</dependency>`)
	pomField      = regexp.MustCompile(`(?s)<(groupId|artifactId|version|scope|optional)>\s*(.*?)\s*</`)
	pomProperty   = regexp.MustCompile(`(?s)<properties>(.*?)</properties>`)
	pomPropEntry  = regexp.MustCompile(`(?s)<([^\s/>]+)>\s*([^<]*?)\s*</`)
	pomVariable   = regexp.MustCompile(`\$\{([^}]+)\}`)
	// An <exclusions> block names artifacts the dependency must NOT bring in, in
	// the same <groupId>/<artifactId> elements the dependency itself uses. Read
	// as part of the block they sit in, the last exclusion overwrote the
	// coordinates being read, so a spring-boot-starter that excludes log4j-core
	// entered the inventory as log4j-core at the starter's version — the
	// repository was named by the advisory for the one library it had gone out of
	// its way to remove, and its actual dependency was not listed at all.
	pomExclusions = regexp.MustCompile(`(?s)<exclusions>.*?</exclusions>`)
)

// parsePOM reads dependency blocks and resolves ${property} versions from the
// same file. A version left as an unresolved variable is reported empty rather
// than as the literal placeholder, so an inventory never claims a repository
// runs "${spring.version}".
func parsePOM(content string) []Package {
	properties := map[string]string{}
	if block := pomProperty.FindStringSubmatch(content); block != nil {
		for _, entry := range pomPropEntry.FindAllStringSubmatch(block[1], -1) {
			properties[entry[1]] = entry[2]
		}
	}
	var out []Package
	for _, block := range pomDependency.FindAllStringSubmatch(content, -1) {
		fields := map[string]string{}
		for _, field := range pomField.FindAllStringSubmatch(pomExclusions.ReplaceAllString(block[1], ""), -1) {
			fields[field[1]] = field[2]
		}
		group, artifact := fields["groupId"], fields["artifactId"]
		if artifact == "" {
			continue
		}
		version := pomVariable.ReplaceAllStringFunc(fields["version"], func(reference string) string {
			return properties[strings.Trim(reference, "${}")]
		})
		if strings.Contains(version, "${") {
			version = ""
		}
		name := artifact
		if group != "" {
			name = group + ":" + artifact
		}
		scope := strings.ToLower(fields["scope"])
		switch scope {
		case "", "compile", "runtime":
			scope = "direct"
		case "provided", "system":
			scope = "direct"
		}
		if strings.EqualFold(fields["optional"], "true") {
			scope = "optional"
		}
		out = append(out, Package{Ecosystem: "maven", Name: name, Version: version, Scope: scope})
	}
	return out
}

var gradleDependency = regexp.MustCompile(`(?m)^\s*(implementation|api|compileOnly|runtimeOnly|testImplementation|testCompileOnly|annotationProcessor)\s*[( ]\s*['"]([^'"]+)['"]`)

func parseGradle(content string) []Package {
	var out []Package
	for _, match := range gradleDependency.FindAllStringSubmatch(content, -1) {
		coordinate := strings.Split(match[2], ":")
		if len(coordinate) < 2 {
			continue
		}
		version := ""
		if len(coordinate) > 2 {
			version = coordinate[2]
		}
		scope := "direct"
		if strings.HasPrefix(match[1], "test") {
			scope = "test"
		}
		out = append(out, Package{Ecosystem: "gradle", Name: coordinate[0] + ":" + coordinate[1], Version: version, Scope: scope})
	}
	return out
}

// requirementLine reads one PEP 508 requirement. The operator alternation lists
// the longer forms first, because the shorter ones are prefixes of them.
var requirementLine = regexp.MustCompile(`^\s*([A-Za-z0-9._-]+)\s*(?:\[[^\]]*\])?\s*(===|==|>=|<=|~=|!=|>|<)?\s*([A-Za-z0-9._*+!-]+)?`)

// parseRequirements reads a requirements file, or one requirement out of a
// pyproject array. An exclusion states which release the project will not take,
// not which one it runs, so "urllib3!=1.25.0" leaves the version to the lock
// file the way an unpinned requirement does — without the operator in the list
// the "!" was read as the version itself and became a group of its own in the
// inventory.
func parseRequirements(content, scope string) []Package {
	var out []Package
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if at := strings.Index(line, "#"); at >= 0 {
			line = strings.TrimSpace(line[:at])
		}
		match := requirementLine.FindStringSubmatch(line)
		if match == nil || match[1] == "" {
			continue
		}
		version := ""
		switch {
		case match[3] == "":
		case match[2] == "":
			// No requirement states a version without an operator, so a bare word
			// after the name means the line is prose. Read as a version it made a
			// sentence in a text file into a package at version "directory".
			continue
		case match[2] != "!=":
			version = strings.TrimSpace(match[2] + match[3])
		}
		out = append(out, Package{Ecosystem: "pypi", Name: match[1], Version: version, Scope: scope})
	}
	return out
}

var (
	tomlSection = regexp.MustCompile(`(?m)^\s*\[([^\]]+)\]\s*$`)
	tomlEntry   = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9._-]+)\s*=\s*(.+)$`)
	tomlVersion = regexp.MustCompile(`version\s*=\s*"([^"]+)"`)
)

// parseTOMLDependencies walks the [dependency] style sections of a TOML file.
// It is shared by Cargo.toml and pyproject.toml, which express the same idea
// with different section names. scopeOf decides whether a section path holds
// dependencies; a path one element longer than one it accepts is a single
// dependency given a section of its own — [dependencies.serde] — which is how
// a declaration with several fields is usually written.
func parseTOMLDependencies(content, ecosystem string, scopeOf func(string) (string, bool)) []Package {
	var out []Package
	for _, span := range tomlSections(content) {
		if scope, ok := scopeOf(span.name); ok {
			out = append(out, tomlTable(span.body, ecosystem, scope)...)
			continue
		}
		at := strings.LastIndex(span.name, ".")
		if at < 0 {
			continue
		}
		scope, ok := scopeOf(span.name[:at])
		if !ok {
			continue
		}
		name := strings.Trim(span.name[at+1:], `"'`)
		if name == "" {
			continue
		}
		version := ""
		if inline := tomlVersion.FindStringSubmatch(span.body); inline != nil {
			version = inline[1]
		}
		out = append(out, Package{Ecosystem: ecosystem, Name: name, Version: version, Scope: scope})
	}
	return out
}

// tomlSpan is one section header and everything written under it.
type tomlSpan struct {
	name string
	body string
}

func tomlSections(content string) []tomlSpan {
	positions := tomlSection.FindAllStringSubmatchIndex(content, -1)
	spans := make([]tomlSpan, 0, len(positions))
	for index, position := range positions {
		end := len(content)
		if index+1 < len(positions) {
			end = positions[index+1][0]
		}
		spans = append(spans, tomlSpan{
			name: strings.TrimSpace(content[position[2]:position[3]]),
			body: content[position[1]:end],
		})
	}
	return spans
}

// tomlTable reads one table of dependencies. A key states either the whole
// dependency (serde = "1.0") or one field of it (serde.workspace = true), and
// reading the second shape as a name gave the inventory a package called
// "serde.workspace" at no version instead of serde — every member crate of a
// workspace that pins its versions centrally declares its dependencies that way.
func tomlTable(body, ecosystem, scope string) []Package {
	var out []Package
	seen := map[string]int{}
	for _, entry := range tomlEntry.FindAllStringSubmatch(body, -1) {
		name, field, dotted := strings.Cut(entry[1], ".")
		if name == "" {
			continue
		}
		value := strings.TrimSpace(entry[2])
		version := ""
		switch {
		case dotted:
			// serde.version = "1.0" states one; serde.workspace = true and
			// serde.features = [...] leave the number to the workspace root.
			if field == "version" && strings.HasPrefix(value, `"`) {
				version = strings.Trim(value, `"`)
			}
		case strings.HasPrefix(value, `"`):
			version = strings.Trim(value, `"`)
		default:
			if inline := tomlVersion.FindStringSubmatch(value); inline != nil {
				version = inline[1]
			}
		}
		index, repeated := seen[name]
		if !repeated {
			seen[name] = len(out)
			out = append(out, Package{Ecosystem: ecosystem, Name: name, Version: version, Scope: scope})
			continue
		}
		if out[index].Version == "" {
			out[index].Version = version
		}
	}
	return out
}

// tomlArray returns the items of the array whose opening bracket is at open.
// A requirement carries brackets of its own — "black[jupyter]>=23" names an
// extra — so ending the array at the first closing bracket cut it off inside a
// string and dropped every dependency declared after that line.
func tomlArray(body string, open int) []string {
	var (
		items []string
		item  strings.Builder
		depth int
		quote byte
	)
	for at := open; at < len(body); at++ {
		char := body[at]
		if quote != 0 {
			if char == '\\' && quote == '"' && at+1 < len(body) {
				item.WriteByte(char)
				at++
				char = body[at]
			} else if char == quote {
				quote = 0
			}
			item.WriteByte(char)
			continue
		}
		switch char {
		case '"', '\'':
			quote = char
			item.WriteByte(char)
		case '#':
			for at+1 < len(body) && body[at+1] != '\n' {
				at++
			}
		case '[':
			if depth++; depth > 1 {
				item.WriteByte(char)
			}
		case ']':
			if depth--; depth == 0 {
				return append(items, item.String())
			}
			item.WriteByte(char)
		case ',':
			if depth == 1 {
				items = append(items, item.String())
				item.Reset()
				continue
			}
			item.WriteByte(char)
		default:
			item.WriteByte(char)
		}
	}
	return append(items, item.String())
}

func parseCargo(content string) []Package {
	return parseTOMLDependencies(content, "cargo", cargoScope)
}

// cargoScope maps a Cargo section to a scope. The three dependency tables also
// appear under a path: [workspace.dependencies] holds the versions a monorepo
// pins once for every member, and [target.'cfg(unix)'.dependencies] the ones
// only one platform builds. Both are real dependencies of the repository.
func cargoScope(section string) (string, bool) {
	switch section[strings.LastIndex(section, ".")+1:] {
	case "dependencies":
		return "direct", true
	case "dev-dependencies", "build-dependencies":
		return "dev", true
	}
	return "", false
}

var tomlArrayKey = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9._-]+)\s*=\s*\[`)

// pyProjectScope maps a pyproject section to a scope. Poetry 1.2 replaced the
// single [tool.poetry.dev-dependencies] table with named groups, so a project
// on any current Poetry states its dev and test dependencies under a path that
// names the group instead.
func pyProjectScope(section string) (string, bool) {
	switch section {
	case "project.dependencies", "tool.poetry.dependencies":
		return "direct", true
	case "tool.poetry.dev-dependencies":
		return "dev", true
	}
	group, ok := strings.CutPrefix(section, "tool.poetry.group.")
	if !ok {
		return "", false
	}
	group, ok = strings.CutSuffix(group, ".dependencies")
	if !ok || strings.Contains(group, ".") {
		return "", false
	}
	return groupScope(group), true
}

// pyArrayScope maps a "name = [requirement, ...]" array to a scope. Only the
// PEP 621 array under [project] states what the package installs; the other
// sections give a name to a set a developer opts into — an extra, a PEP 735
// dependency group, a PDM development set — and each entry there is a package
// the repository builds or tests with. Reading none of them left every project
// on setuptools, hatch or PDM stating its test tooling in the standard place
// absent from the inventory for those libraries.
func pyArrayScope(section, key string) (string, bool) {
	switch section {
	case "project":
		return "direct", key == "dependencies"
	case "project.optional-dependencies":
		return "optional", true
	case "dependency-groups", "tool.pdm.dev-dependencies":
		return groupScope(key), true
	}
	return "", false
}

// groupScope reads the name a project gave a set of dependencies. Only what a
// group calls itself separates test tooling from the rest of development.
func groupScope(group string) string {
	if strings.EqualFold(group, "test") {
		return "test"
	}
	return "dev"
}

// parsePyProject reads both shapes in use: the PEP 621 dependencies arrays and
// Poetry's [tool.poetry.dependencies] table.
func parsePyProject(content string) []Package {
	out := parseTOMLDependencies(content, "pypi", pyProjectScope)
	for _, span := range tomlSections(content) {
		for _, key := range tomlArrayKey.FindAllStringSubmatchIndex(span.body, -1) {
			scope, ok := pyArrayScope(span.name, strings.TrimSpace(span.body[key[2]:key[3]]))
			if !ok {
				continue
			}
			for _, item := range tomlArray(span.body, key[1]-1) {
				requirement := strings.Trim(strings.TrimSpace(item), `"'`)
				if requirement == "" {
					continue
				}
				out = append(out, parseRequirements(requirement, scope)...)
			}
		}
	}
	// Poetry states the interpreter as a dependency; it is not a package.
	filtered := out[:0]
	for _, item := range out {
		if strings.EqualFold(item.Name, "python") {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

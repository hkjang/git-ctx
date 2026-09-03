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
	case "requirements.txt", "requirements-dev.txt", "pyproject.toml":
		return "pypi", true
	case "cargo.toml":
		return "cargo", true
	default:
		return "", false
	}
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
		if strings.HasSuffix(strings.ToLower(path.Base(filePath)), ".toml") {
			return parsePyProject(content)
		}
		return parseRequirements(content)
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

var requirementLine = regexp.MustCompile(`^\s*([A-Za-z0-9._-]+)\s*(?:\[[^\]]*\])?\s*(==|>=|<=|~=|>|<)?\s*([A-Za-z0-9._*+!-]+)?`)

func parseRequirements(content string) []Package {
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
		if match[3] != "" {
			version = strings.TrimSpace(match[2] + match[3])
		}
		out = append(out, Package{Ecosystem: "pypi", Name: match[1], Version: version, Scope: "direct"})
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
// with different section names.
func parseTOMLDependencies(content, ecosystem string, sections map[string]string) []Package {
	var out []Package
	positions := tomlSection.FindAllStringSubmatchIndex(content, -1)
	for index, position := range positions {
		name := strings.TrimSpace(content[position[2]:position[3]])
		scope, ok := sections[name]
		if !ok {
			continue
		}
		end := len(content)
		if index+1 < len(positions) {
			end = positions[index+1][0]
		}
		for _, entry := range tomlEntry.FindAllStringSubmatch(content[position[1]:end], -1) {
			value := strings.TrimSpace(entry[2])
			version := ""
			switch {
			case strings.HasPrefix(value, `"`):
				version = strings.Trim(value, `"`)
			default:
				if inline := tomlVersion.FindStringSubmatch(value); inline != nil {
					version = inline[1]
				}
			}
			out = append(out, Package{Ecosystem: ecosystem, Name: entry[1], Version: version, Scope: scope})
		}
	}
	return out
}

func parseCargo(content string) []Package {
	return parseTOMLDependencies(content, "cargo", map[string]string{
		"dependencies": "direct", "dev-dependencies": "dev", "build-dependencies": "dev",
	})
}

var pyProjectArray = regexp.MustCompile(`(?s)dependencies\s*=\s*\[(.*?)\]`)

// parsePyProject reads both shapes in use: the PEP 621 dependencies array and
// Poetry's [tool.poetry.dependencies] table.
func parsePyProject(content string) []Package {
	out := parseTOMLDependencies(content, "pypi", map[string]string{
		"tool.poetry.dependencies":     "direct",
		"tool.poetry.dev-dependencies": "dev",
		"project.dependencies":         "direct",
	})
	if array := pyProjectArray.FindStringSubmatch(content); array != nil {
		for _, item := range strings.Split(array[1], ",") {
			requirement := strings.Trim(strings.TrimSpace(item), `"'`)
			if requirement == "" {
				continue
			}
			out = append(out, parseRequirements(requirement)...)
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

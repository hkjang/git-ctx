package manifest

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// A manifest states intent — "^18.2.0", ">=2.31.0" — and a lock file states the
// result. During an advisory only the second one answers the question: a caret
// range cannot be judged, while the version the build actually resolved can.
//
// Lock files are therefore read alongside manifests and stored as resolved
// declarations, which the advisory judgement prefers over the range that
// produced them.

// RecognizeLock reports the ecosystem of a lock file, if it is one.
func RecognizeLock(filePath string) (string, bool) {
	switch strings.ToLower(path.Base(filePath)) {
	case "go.sum":
		return "go", true
	case "package-lock.json", "npm-shrinkwrap.json":
		return "npm", true
	case "yarn.lock", "pnpm-lock.yaml":
		return "npm", true
	case "cargo.lock":
		return "cargo", true
	case "poetry.lock":
		return "pypi", true
	default:
		return "", false
	}
}

// MaxLockBytes bounds one lock file read. Lock files are large by nature; this
// keeps a monorepo's lock out of memory while covering ordinary ones.
const MaxLockBytes = 8 << 20

// MaxLockPackages bounds how many resolved packages one lock file contributes.
// A go.sum with ten thousand lines is real, and the inventory is a catalogue of
// what is used, not a copy of the lock.
const MaxLockPackages = 4000

// ParseLock extracts the resolved packages of one lock file. Every returned
// package has Scope "resolved", which is what marks it as authoritative for an
// advisory.
func ParseLock(filePath, content string) []Package {
	ecosystem, ok := RecognizeLock(filePath)
	if !ok || len(content) > MaxLockBytes {
		return nil
	}
	base := strings.ToLower(path.Base(filePath))
	var packages []Package
	switch {
	case base == "go.sum":
		packages = parseGoSum(content)
	case base == "package-lock.json" || base == "npm-shrinkwrap.json":
		packages = parsePackageLock(content)
	case base == "yarn.lock":
		packages = parseYarnLock(content)
	case base == "pnpm-lock.yaml":
		packages = parsePnpmLock(content)
	case base == "cargo.lock" || base == "poetry.lock":
		packages = parseTOMLLock(content, ecosystem)
	}
	for index := range packages {
		packages[index].Ecosystem = ecosystem
		packages[index].Scope = "resolved"
	}
	if len(packages) > MaxLockPackages {
		packages = packages[:MaxLockPackages]
	}
	return packages
}

// parseGoSum reads module versions. Each module appears twice — once for the
// zip and once for the go.mod — and the /go.mod suffix is stripped so both
// collapse to one entry.
func parseGoSum(content string) []Package {
	seen := map[string]bool{}
	var out []Package
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		module, version := fields[0], strings.TrimSuffix(fields[1], "/go.mod")
		key := module + "\x00" + version
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Package{Name: module, Version: version})
	}
	return out
}

// parsePackageLock reads both lockfile layouts npm has shipped: the v2/v3
// "packages" map keyed by path, and the v1 nested "dependencies" tree.
func parsePackageLock(content string) []Package {
	var document struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}
	if json.Unmarshal([]byte(content), &document) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Package
	add := func(name, version string) {
		if name == "" || version == "" || seen[name+"\x00"+version] {
			return
		}
		seen[name+"\x00"+version] = true
		out = append(out, Package{Name: name, Version: version})
	}
	for key, entry := range document.Packages {
		// "" is the project itself; "node_modules/x" and nested paths are deps.
		at := strings.LastIndex(key, "node_modules/")
		if at < 0 {
			continue
		}
		add(key[at+len("node_modules/"):], entry.Version)
	}
	var walk func(map[string]json.RawMessage)
	walk = func(tree map[string]json.RawMessage) {
		for name, raw := range tree {
			var node struct {
				Version      string                     `json:"version"`
				Dependencies map[string]json.RawMessage `json:"dependencies"`
			}
			if json.Unmarshal(raw, &node) != nil {
				continue
			}
			add(name, node.Version)
			if node.Dependencies != nil {
				walk(node.Dependencies)
			}
		}
	}
	walk(document.Dependencies)
	return out
}

var (
	yarnEntry   = regexp.MustCompile(`(?m)^"?([^"\s].*?)"?:\s*$`)
	yarnVersion = regexp.MustCompile(`(?m)^\s+version:?\s+"?([^"\s]+)"?\s*$`)
)

// parseYarnLock walks entry headers and the version line that follows each one.
// The header lists the ranges that resolved to this entry, so the package name
// is the part before the last @ of the first range.
func parseYarnLock(content string) []Package {
	lines := strings.Split(content, "\n")
	var out []Package
	seen := map[string]bool{}
	for index, line := range lines {
		header := yarnEntry.FindStringSubmatch(line)
		if header == nil || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		first := strings.TrimSpace(strings.Split(header[1], ",")[0])
		first = strings.Trim(first, `"`)
		at := strings.LastIndex(first, "@")
		if at <= 0 {
			continue
		}
		name := first[:at]
		for offset := index + 1; offset < len(lines) && offset < index+8; offset++ {
			if strings.TrimSpace(lines[offset]) == "" {
				break
			}
			if version := yarnVersion.FindStringSubmatch(lines[offset]); version != nil {
				if !seen[name+"\x00"+version[1]] {
					seen[name+"\x00"+version[1]] = true
					out = append(out, Package{Name: name, Version: version[1]})
				}
				break
			}
		}
	}
	return out
}

// pnpm has shipped three key layouts and an inventory that reads only the
// newest silently omits every repository still on an older one — the failure
// mode this feature exists to avoid. Both shapes are matched:
//
//	/lodash/4.17.21:        (v5, v6, v7)
//	/@babel/core/7.24.0:
//	lodash@4.17.21:         (v9)
//	'@babel/core@7.24.0':
var (
	// A peer-resolution suffix — /react-dom/18.2.0(react@18.2.0): — may follow the
	// version and is not part of it.
	pnpmSlashEntry = regexp.MustCompile(`(?m)^\s+/?'?(@[^/'\s]+/[^/'\s]+|[^/@'\s][^/'\s]*)/([0-9][^:'\s(]*)(?:\([^)]*\))?'?:`)
	// The name may not contain a slash or a parenthesis: those belong to the
	// older slash layout and to the peer-resolution suffix respectively, and
	// letting them through produced entries like "react-dom/18.2.0(react".
	pnpmAtEntry = regexp.MustCompile(`(?m)^\s+'?(@[^/'\s]+/[^@'\s(/]+|[^@'\s(/][^@'\s(/]*)@([0-9][^:'\s(]*)(?:\([^)]*\))?'?:`)
)

func parsePnpmLock(content string) []Package {
	seen := map[string]bool{}
	var out []Package
	add := func(name, version string) {
		// A peer-dependency suffix — lodash@4.17.21(react@18.0.0) — describes the
		// resolution context, not the version.
		if at := strings.IndexByte(version, '('); at >= 0 {
			version = version[:at]
		}
		version = strings.TrimSuffix(version, ":")
		if name == "" || version == "" || seen[name+"\x00"+version] {
			return
		}
		seen[name+"\x00"+version] = true
		out = append(out, Package{Name: name, Version: version})
	}
	for _, match := range pnpmSlashEntry.FindAllStringSubmatch(content, -1) {
		add(match[1], match[2])
	}
	for _, match := range pnpmAtEntry.FindAllStringSubmatch(content, -1) {
		add(match[1], match[2])
	}
	return out
}

var (
	lockPackageBlock = regexp.MustCompile(`(?m)^\[\[package\]\]\s*$`)
	lockName         = regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
	lockVersion      = regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"`)
)

// parseTOMLLock reads the [[package]] blocks Cargo and Poetry both use.
func parseTOMLLock(content, ecosystem string) []Package {
	positions := lockPackageBlock.FindAllStringIndex(content, -1)
	var out []Package
	for index, position := range positions {
		end := len(content)
		if index+1 < len(positions) {
			end = positions[index+1][0]
		}
		block := content[position[1]:end]
		name := lockName.FindStringSubmatch(block)
		version := lockVersion.FindStringSubmatch(block)
		if name == nil || version == nil {
			continue
		}
		out = append(out, Package{Name: name[1], Version: version[1], Ecosystem: ecosystem})
	}
	return out
}

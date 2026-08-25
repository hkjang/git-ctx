package manifest

import (
	"strconv"
	"strings"
)

// Comparison of declared versions is deliberately conservative.
//
// A manifest states a version the way its ecosystem does: "v1.10.0", "^18.2.0",
// ">=2.31.0", "2.17.1", "1.0.0-rc.1". An advisory states a fixed version.
// Answering "is this repository affected" is a range question, and pretending a
// caret range or a floating "latest" can be compared exactly would produce the
// one answer nobody can afford: a false all-clear. So a version is compared only
// when a concrete release can be read out of it, and everything else is reported
// as undecidable instead of guessed.

// Comparable reports whether a declared version can be compared at all.
func Comparable(declared string) bool {
	_, ok := parseVersion(declared)
	return ok
}

// Compare orders two declared versions. The second return value is false when
// either side cannot be read as a concrete release, in which case the caller
// must treat the pair as undecided rather than equal.
func Compare(left, right string) (int, bool) {
	first, ok := parseVersion(left)
	if !ok {
		return 0, false
	}
	second, ok := parseVersion(right)
	if !ok {
		return 0, false
	}
	return first.compare(second), true
}

// Below reports whether a declared version is strictly below a fixed release,
// which is how an advisory is phrased: "fixed in 2.17.1".
func Below(declared, fixed string) (bool, bool) {
	order, ok := Compare(declared, fixed)
	if !ok {
		return false, false
	}
	return order < 0, true
}

type version struct {
	numbers    []int
	prerelease string
}

// rangeOperators are the prefixes a manifest uses to express "this or newer".
// The lower bound of such a range is still a concrete release and is what an
// advisory check has to look at: a repository pinned to ">=2.14.1" may resolve
// to anything at or above it, so its floor decides whether it can be affected.
var rangeOperators = []string{">=", "<=", "==", "~=", "^", "~", ">", "<", "=", "v"}

// parseVersion reads a concrete release out of a declared version, or reports
// that there is none. Wildcards, ranges with no numeric floor, git references
// and "latest" are all undecidable by design.
func parseVersion(declared string) (version, bool) {
	value := strings.TrimSpace(declared)
	if value == "" {
		return version{}, false
	}
	// A compound range ("&gt;=1.2,&lt;2.0") has no single floor worth trusting.
	if strings.ContainsAny(value, ",|") || strings.Contains(value, " - ") {
		return version{}, false
	}
	lower := strings.ToLower(value)
	for _, word := range []string{"latest", "*", "x", "main", "master", "file:", "git+", "http", "workspace:", "link:"} {
		if strings.HasPrefix(lower, word) {
			return version{}, false
		}
	}
	for {
		trimmed := value
		for _, operator := range rangeOperators {
			trimmed = strings.TrimPrefix(trimmed, operator)
		}
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == value {
			break
		}
		value = trimmed
	}
	if value == "" {
		return version{}, false
	}
	core, prerelease := value, ""
	if at := strings.IndexAny(core, "-+"); at >= 0 {
		core, prerelease = core[:at], core[at+1:]
	}
	parts := strings.Split(core, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return version{}, false
		}
		// A wildcard segment ("1.2.x") leaves the release unknown.
		number, err := strconv.Atoi(part)
		if err != nil {
			return version{}, false
		}
		numbers = append(numbers, number)
	}
	if len(numbers) == 0 {
		return version{}, false
	}
	return version{numbers: numbers, prerelease: prerelease}, true
}

func (v version) compare(other version) int {
	length := max(len(v.numbers), len(other.numbers))
	for index := 0; index < length; index++ {
		left, right := 0, 0
		if index < len(v.numbers) {
			left = v.numbers[index]
		}
		if index < len(other.numbers) {
			right = other.numbers[index]
		}
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	// A pre-release precedes its own release, as every ecosystem agrees.
	switch {
	case v.prerelease == "" && other.prerelease == "":
		return 0
	case v.prerelease == "":
		return 1
	case other.prerelease == "":
		return -1
	case v.prerelease < other.prerelease:
		return -1
	case v.prerelease > other.prerelease:
		return 1
	default:
		return 0
	}
}

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
//
// A range is only decidable in one direction. ">=2.17.1" cannot resolve below
// the fix, so it is safe. "^18.2.0" may resolve to 18.2.0 or to 18.9.9, so a
// floor below the fix decides nothing — calling it affected would flood an
// advisory with repositories that are probably already patched, and calling it
// safe would hide the ones that are not. Only an exact pin below the fix is
// reported as affected.
func Below(declared, fixed string) (bool, bool) {
	left, isRange, ok := parseDeclared(declared)
	if !ok {
		return false, false
	}
	right, _, ok := parseDeclared(fixed)
	if !ok {
		return false, false
	}
	order := left.compare(right)
	if order >= 0 {
		// At or above the fix: neither an exact pin nor a range floor can be
		// affected, because a range only resolves upwards.
		return false, true
	}
	if isRange {
		return false, false
	}
	return true, true
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
	parsed, _, ok := parseDeclared(declared)
	return parsed, ok
}

// parseDeclared also reports whether the declaration was a range rather than an
// exact pin, which decides whether a comparison below the fix means anything.
func parseDeclared(declared string) (version, bool, bool) {
	value := strings.TrimSpace(declared)
	if value == "" {
		return version{}, false, false
	}
	// A compound range ("&gt;=1.2,&lt;2.0") has no single floor worth trusting.
	if strings.ContainsAny(value, ",|") || strings.Contains(value, " - ") {
		return version{}, false, false
	}
	lower := strings.ToLower(value)
	for _, word := range []string{"latest", "*", "x", "main", "master", "file:", "git+", "http", "workspace:", "link:"} {
		if strings.HasPrefix(lower, word) {
			return version{}, false, false
		}
	}
	// "==1.2.3" and "v1.2.3" pin exactly; "^", "~", ">=" and ">" are ranges that
	// resolve upwards from the number that follows them.
	isRange := false
	for _, operator := range []string{"^", "~", ">=", ">", "~="} {
		if strings.HasPrefix(value, operator) {
			isRange = true
			break
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
		return version{}, false, false
	}
	core, prerelease := value, ""
	if at := strings.IndexAny(core, "-+"); at >= 0 {
		core, prerelease = core[:at], core[at+1:]
	}
	parts := strings.Split(core, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return version{}, false, false
		}
		// A wildcard segment ("1.2.x") leaves the release unknown.
		number, err := strconv.Atoi(part)
		if err != nil {
			return version{}, false, false
		}
		numbers = append(numbers, number)
	}
	if len(numbers) == 0 {
		return version{}, false, false
	}
	return version{numbers: numbers, prerelease: prerelease}, isRange, true
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

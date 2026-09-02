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
// A range is only decidable in the direction it resolves. ">=2.17.1" cannot
// resolve below the fix, so it is safe. "^18.2.0" may resolve to 18.2.0 or to
// 18.9.9, so a floor below the fix decides nothing — calling it affected would
// flood an advisory with repositories that are probably already patched, and
// calling it safe would hide the ones that are not.
//
// A ceiling resolves the other way. "requests<3" cannot resolve to anything at
// or above 3, so against a fix in 3.0.0 every release it can pick is affected,
// and reading it as an exact pin on 3 answered the advisory with a false
// all-clear. Above the fix a ceiling decides nothing: "<3.0.0" against a fix in
// 2.0.0 may resolve to 2.5.0 or to 1.4.0.
func Below(declared, fixed string) (bool, bool) {
	left, kind, ok := parseDeclared(declared)
	if !ok {
		return false, false
	}
	right, _, ok := parseDeclared(fixed)
	if !ok {
		return false, false
	}
	order := left.compare(right)
	switch kind {
	case boundExact:
		return order < 0, true
	case boundFloor:
		// At or above the fix a floor is safe, because it only resolves upwards.
		// Below it, the range spans both answers.
		return false, order >= 0
	default:
		// A ceiling at or below the fix leaves nothing above it to resolve to;
		// "<=2.0.0" against a fix in 2.0.0 still reaches the fix itself.
		if order < 0 || (kind == boundBelow && order == 0) {
			return true, true
		}
		return false, false
	}
}

// boundKind is the direction a declaration can resolve in, which is what decides
// whether a comparison against the fix means anything.
type boundKind int

const (
	boundExact boundKind = iota
	boundFloor
	boundBelow
	boundAtMost
)

type version struct {
	numbers    []int
	prerelease string
}

// rangeOperators are the prefixes a manifest puts in front of a version. The
// bound of such a range is still a concrete release and is what an advisory
// check has to look at: a repository pinned to ">=2.14.1" may resolve to
// anything at or above it, so its floor decides whether it can be affected.
var rangeOperators = []string{">=", "<=", "==", "~=", "^", "~", ">", "<", "=", "v"}

// parseVersion reads a concrete release out of a declared version, or reports
// that there is none. Wildcards, ranges with no numeric floor, git references
// and "latest" are all undecidable by design.
func parseVersion(declared string) (version, bool) {
	parsed, _, ok := parseDeclared(declared)
	return parsed, ok
}

// parseDeclared also reports which way the declaration can resolve, which
// decides whether a comparison against the fix means anything.
func parseDeclared(declared string) (version, boundKind, bool) {
	value := strings.TrimSpace(declared)
	if value == "" {
		return version{}, boundExact, false
	}
	// A compound range ("&gt;=1.2,&lt;2.0") has no single floor worth trusting.
	if strings.ContainsAny(value, ",|") || strings.Contains(value, " - ") {
		return version{}, boundExact, false
	}
	lower := strings.ToLower(value)
	for _, word := range []string{"latest", "*", "x", "main", "master", "file:", "git+", "http", "workspace:", "link:"} {
		if strings.HasPrefix(lower, word) {
			return version{}, boundExact, false
		}
	}
	// "==1.2.3" and "v1.2.3" pin exactly; "^", "~", ">=" and ">" resolve upwards
	// from the number that follows them, "<" and "<=" downwards from it.
	kind := boundExact
	for _, operator := range []struct {
		prefix string
		kind   boundKind
	}{{"^", boundFloor}, {"~=", boundFloor}, {"~", boundFloor}, {">=", boundFloor}, {">", boundFloor}, {"<=", boundAtMost}, {"<", boundBelow}} {
		if strings.HasPrefix(value, operator.prefix) {
			kind = operator.kind
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
		return version{}, boundExact, false
	}
	core, prerelease := value, ""
	// Build metadata is not part of precedence, and reading it as a pre-release
	// made every Go module pinned to "v2.0.0+incompatible" rank below the plain
	// 2.0.0 an advisory names as the fix — the whole ecosystem reported affected
	// by the release it is already on.
	if at := strings.IndexByte(core, '+'); at >= 0 {
		core = core[:at]
	}
	if at := strings.IndexByte(core, '-'); at >= 0 {
		core, prerelease = core[:at], core[at+1:]
	}
	parts := strings.Split(core, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return version{}, boundExact, false
		}
		// A wildcard segment ("1.2.x") leaves the release unknown.
		number, err := strconv.Atoi(part)
		if err != nil {
			return version{}, boundExact, false
		}
		numbers = append(numbers, number)
	}
	if len(numbers) == 0 {
		return version{}, boundExact, false
	}
	return version{numbers: numbers, prerelease: prerelease}, kind, true
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
	}
	return comparePrerelease(v.prerelease, other.prerelease)
}

// comparePrerelease orders two pre-release tails field by field.
//
// Comparing them as plain strings put "rc.9" above "rc.10", which is the answer
// an advisory cannot afford in either direction: a repository on rc.9 with the
// fix in rc.10 was reported safe. Numeric fields are therefore compared as
// numbers, a numeric field ranks below an alphanumeric one, and a longer tail
// wins a tie ("alpha" precedes "alpha.1") — the ordering semantic versioning
// specifies and the ecosystems implement.
func comparePrerelease(left, right string) int {
	first, second := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < len(first) && index < len(second); index++ {
		if order := compareIdentifier(first[index], second[index]); order != 0 {
			return order
		}
	}
	switch {
	case len(first) < len(second):
		return -1
	case len(first) > len(second):
		return 1
	}
	return 0
}

func compareIdentifier(left, right string) int {
	leftNumber, leftNumeric := numericIdentifier(left)
	rightNumber, rightNumeric := numericIdentifier(right)
	switch {
	case leftNumeric && rightNumeric:
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		}
		return 0
	case leftNumeric:
		return -1
	case rightNumeric:
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	}
	return 0
}

// numericIdentifier reads a pre-release field as a number, rejecting anything a
// number cannot hold so a hundred-digit field falls back to text rather than
// comparing as an overflowed value.
func numericIdentifier(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return number, true
}

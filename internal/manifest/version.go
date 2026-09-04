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
// resolve below the fix, so it is safe. ">=2.14.1" may resolve to anything above
// its floor, so a floor below the fix decides nothing — calling it affected
// would flood an advisory with repositories that are probably already patched,
// and calling it safe would hide the ones that are not.
//
// A caret or tilde floor is not open, though. "~2.14.0" cannot reach 2.15.0 and
// "^1.2.3" cannot reach 2.0.0, so when the whole range sits below the fix no
// release it can pick carries it. Reading those as open-ended answered the two
// shapes npm, Cargo and PEP 440 manifests are mostly written in with "cannot be
// decided from the manifest", which is the sentence an operator has to resolve
// by hand for every repository in the report.
//
// A ceiling resolves the other way. "requests<3" cannot resolve to anything at
// or above 3, so against a fix in 3.0.0 every release it can pick is affected,
// and reading it as an exact pin on 3 answered the advisory with a false
// all-clear. Above the fix a ceiling decides nothing: "<3.0.0" against a fix in
// 2.0.0 may resolve to 2.5.0 or to 1.4.0.
func Below(declared, fixed string) (bool, bool) {
	left, ok := parseDeclared(declared)
	if !ok {
		return false, false
	}
	right, ok := parseDeclared(fixed)
	if !ok {
		return false, false
	}
	order := left.version.compare(right.version)
	switch left.kind {
	case boundExact:
		return order < 0, true
	case boundFloor:
		// At or above the fix a floor is safe, because it only resolves upwards.
		if order >= 0 {
			return false, true
		}
		// Below it, an open floor spans both answers; a bounded one does not once
		// its ceiling — the first release it cannot reach — is at or below the fix.
		if left.bounded && left.ceiling.compare(right.version) <= 0 {
			return true, true
		}
		return false, false
	default:
		// A ceiling at or below the fix leaves nothing above it to resolve to;
		// "<=2.0.0" against a fix in 2.0.0 still reaches the fix itself.
		if order < 0 || (left.kind == boundBelow && order == 0) {
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

// bound is one declaration read as a range: the concrete release it names, the
// direction it can move from there, and — for the shapes that have one — the
// first release it cannot reach.
type bound struct {
	version version
	kind    boundKind
	// ceiling is exclusive: the range covers everything below it, never it.
	ceiling version
	bounded bool
}

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
	parsed, ok := parseDeclared(declared)
	return parsed.version, ok
}

// parseDeclared also reports which way the declaration can resolve, which
// decides whether a comparison against the fix means anything.
func parseDeclared(declared string) (bound, bool) {
	value := strings.TrimSpace(declared)
	if value == "" {
		return bound{}, false
	}
	// A compound range ("&gt;=1.2,&lt;2.0") has no single floor worth trusting.
	if strings.ContainsAny(value, ",|") || strings.Contains(value, " - ") {
		return bound{}, false
	}
	lower := strings.ToLower(value)
	for _, word := range []string{"latest", "*", "x", "main", "master", "file:", "git+", "http", "workspace:", "link:"} {
		if strings.HasPrefix(lower, word) {
			return bound{}, false
		}
	}
	// "==1.2.3" and "v1.2.3" pin exactly; "^", "~", ">=" and ">" resolve upwards
	// from the number that follows them, "<" and "<=" downwards from it. The
	// caret and the tilde also stop somewhere, which the rest do not.
	kind, ceiling := boundExact, ceilingNone
	for _, operator := range []struct {
		prefix  string
		kind    boundKind
		ceiling ceilingRule
	}{
		{"^", boundFloor, ceilingCaret},
		{"~=", boundFloor, ceilingTilde},
		{"~", boundFloor, ceilingTilde},
		{">=", boundFloor, ceilingNone},
		{">", boundFloor, ceilingNone},
		{"<=", boundAtMost, ceilingNone},
		{"<", boundBelow, ceilingNone},
	} {
		if strings.HasPrefix(value, operator.prefix) {
			kind, ceiling = operator.kind, operator.ceiling
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
		return bound{}, false
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
			return bound{}, false
		}
		// A wildcard segment ("1.2.x") leaves the release unknown.
		number, err := strconv.Atoi(part)
		if err != nil {
			return bound{}, false
		}
		numbers = append(numbers, number)
	}
	if len(numbers) == 0 {
		return bound{}, false
	}
	result := bound{version: version{numbers: numbers, prerelease: prerelease}, kind: kind}
	result.ceiling, result.bounded = rangeCeiling(ceiling, numbers)
	return result, true
}

// ceilingRule is how an operator bounds the range above its floor.
type ceilingRule int

const (
	ceilingNone ceilingRule = iota
	ceilingCaret
	ceilingTilde
)

// rangeCeiling returns the first release a caret or tilde range cannot reach.
//
// The caret keeps the leftmost non-zero component — "^1.2.3" stops at 2.0.0,
// "^0.2.3" at 0.3.0, "^0.0.3" at 0.0.4 — and the tilde keeps major and minor,
// so "~1.2.3" and PEP 440's "~=1.2.3" both stop at 1.3.0.
//
// A declaration that states fewer components says less about where it stops,
// and the ecosystems disagree there: npm reads "~1.2" as stopping at 1.3.0 and
// "^0.2" as stopping at 0.3.0, while PEP 440 reads "~=1.2" as stopping at
// 2.0.0. The widest of the readings is used,
// because a ceiling is only ever used to call a repository affected and an
// over-tight one would invent that verdict.
func rangeCeiling(rule ceilingRule, numbers []int) (version, bool) {
	if rule == ceilingNone || len(numbers) == 0 {
		return version{}, false
	}
	index := 0
	if len(numbers) >= 3 {
		switch rule {
		case ceilingCaret:
			// "^0.0.0" has no non-zero component to keep; it stops at 0.0.1.
			index = len(numbers) - 1
			for at, number := range numbers {
				if number != 0 {
					index = at
					break
				}
			}
		case ceilingTilde:
			index = 1
		}
	}
	raised := append([]int(nil), numbers[:index+1]...)
	raised[index]++
	return version{numbers: raised}, true
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

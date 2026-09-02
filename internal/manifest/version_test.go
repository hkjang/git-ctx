package manifest

import "testing"

// The comparison exists to answer an advisory, where a wrong "safe" is the
// expensive mistake. Anything that cannot be read as a concrete release must be
// reported as undecidable rather than guessed.
func TestCompareOnlyDecidesConcreteVersions(t *testing.T) {
	decidable := []struct {
		left, right string
		want        int
	}{
		{"v1.10.0", "1.9.9", 1},
		{"2.14.1", "2.17.1", -1},
		{"2.17.1", "2.17.1", 0},
		{"^18.2.0", "18.3.0", -1},
		{">=2.31.0", "2.31.0", 0},
		{"==4.2.7", "4.2.8", -1},
		{"1.10", "1.9", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"4.17.21", "4.17.5", 1},
	}
	for _, item := range decidable {
		got, ok := Compare(item.left, item.right)
		if !ok {
			t.Fatalf("Compare(%q,%q) must be decidable", item.left, item.right)
		}
		if got != item.want {
			t.Fatalf("Compare(%q,%q)=%d want %d", item.left, item.right, got, item.want)
		}
	}
	for _, undecidable := range []string{"", "latest", "*", "1.2.x", ">=1.2,<2.0", "^1 || ^2", "git+https://host/x.git", "workspace:*", "main", "file:../shared"} {
		if _, ok := Compare(undecidable, "2.0.0"); ok {
			t.Fatalf("%q must not be comparable", undecidable)
		}
		if Comparable(undecidable) {
			t.Fatalf("%q reported comparable", undecidable)
		}
	}
}

func TestBelowAnswersTheAdvisoryQuestion(t *testing.T) {
	affected, ok := Below("2.14.1", "2.17.1")
	if !ok || !affected {
		t.Fatalf("affected=%v ok=%v", affected, ok)
	}
	safe, ok := Below("2.17.1", "2.17.1")
	if !ok || safe {
		t.Fatalf("a version at the fix must not be affected: %v %v", safe, ok)
	}
	if _, ok := Below("latest", "2.17.1"); ok {
		t.Fatal("an unreadable version must stay undecided")
	}
	// A range floor still decides the question: pinned at or above the fix, the
	// repository cannot resolve to an affected release.
	if affected, ok := Below(">=2.17.1", "2.17.1"); !ok || affected {
		t.Fatalf("range floor at the fix: affected=%v ok=%v", affected, ok)
	}
}

// A range only resolves upwards, so it decides the advisory in one direction
// only. Treating "^18.2.0" as affected because its floor is below the fix would
// flood the report with repositories that are probably already patched.
func TestRangeBelowTheFixIsUndecided(t *testing.T) {
	if _, decided := Below("^18.2.0", "18.3.0"); decided {
		t.Fatal("a caret range below the fix cannot be decided")
	}
	if _, decided := Below("~2.14.0", "2.17.1"); decided {
		t.Fatal("a tilde range below the fix cannot be decided")
	}
	if _, decided := Below(">=2.14.1", "2.17.1"); decided {
		t.Fatal("an open lower bound below the fix cannot be decided")
	}
	// Above or at the fix a range is safe: it cannot resolve downwards.
	if affected, decided := Below("^18.4.0", "18.3.0"); !decided || affected {
		t.Fatalf("affected=%v decided=%v", affected, decided)
	}
	// An exact pin is decided in both directions.
	if affected, decided := Below("2.14.1", "2.17.1"); !decided || !affected {
		t.Fatalf("an exact pin below the fix is affected: %v %v", affected, decided)
	}
	if affected, decided := Below("==4.2.7", "4.2.8"); !decided || !affected {
		t.Fatalf("a pinned requirement is exact: %v %v", affected, decided)
	}
	if affected, decided := Below("v1.10.0", "1.11.0"); !decided || !affected {
		t.Fatalf("a go version is exact: %v %v", affected, decided)
	}
}

// Build metadata does not order a release. Reading "+incompatible" as a
// pre-release ranked every Go module below the plain release an advisory names
// as the fix, so a repository already on the fixed version was reported
// affected by it.
func TestBuildMetadataDoesNotOrderARelease(t *testing.T) {
	for _, declared := range []string{"v2.0.0+incompatible", "2.0.0+build.5", "v2.0.0"} {
		order, ok := Compare(declared, "2.0.0")
		if !ok || order != 0 {
			t.Fatalf("Compare(%q,\"2.0.0\")=%d ok=%v want 0", declared, order, ok)
		}
		if affected, decided := Below(declared, "2.0.0"); !decided || affected {
			t.Fatalf("%q is the fix, not a release below it: affected=%v decided=%v", declared, affected, decided)
		}
	}
	// The metadata is dropped, not the pre-release it follows.
	if order, ok := Compare("1.0.0-rc.1+build.7", "1.0.0"); !ok || order != -1 {
		t.Fatalf("a pre-release with build metadata still precedes its release: %d %v", order, ok)
	}
}

// A pre-release tail is compared field by field, numbers as numbers. As plain
// strings "rc.9" sorted above "rc.10", which answered the advisory with a false
// all-clear for the repository that had not taken the fix.
func TestPrereleaseFieldsCompareNumerically(t *testing.T) {
	ordered := []struct {
		left, right string
		want        int
	}{
		{"1.0.0-rc.9", "1.0.0-rc.10", -1},
		{"1.0.0-rc.10", "1.0.0-rc.9", 1},
		{"1.0.0-rc.2", "1.0.0-rc.2", 0},
		// A numeric field ranks below an alphanumeric one, and a longer tail wins
		// a tie, as semantic versioning specifies.
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-beta", -1},
	}
	for _, item := range ordered {
		got, ok := Compare(item.left, item.right)
		if !ok || got != item.want {
			t.Fatalf("Compare(%q,%q)=%d ok=%v want %d", item.left, item.right, got, ok, item.want)
		}
	}
	if affected, decided := Below("1.0.0-rc.9", "1.0.0-rc.10"); !decided || !affected {
		t.Fatalf("rc.9 is below a fix in rc.10: affected=%v decided=%v", affected, decided)
	}
	if affected, decided := Below("1.0.0-rc.10", "1.0.0-rc.9"); !decided || affected {
		t.Fatalf("rc.10 has the fix from rc.9: affected=%v decided=%v", affected, decided)
	}
}

// A ceiling resolves downwards, so it decides the advisory in the direction a
// floor cannot. "requests<3" was read as a pin on 3 and reported safe against a
// fix in 3.0.0, though nothing it can resolve to carries the fix.
func TestCeilingResolvesDownwards(t *testing.T) {
	if affected, decided := Below("<3", "3.0.0"); !decided || !affected {
		t.Fatalf("a ceiling at the fix cannot reach it: affected=%v decided=%v", affected, decided)
	}
	if affected, decided := Below("<=1.5.0", "2.0.0"); !decided || !affected {
		t.Fatalf("a ceiling below the fix is affected: affected=%v decided=%v", affected, decided)
	}
	// At or above the fix the range spans both answers.
	if _, decided := Below("<3.0.0", "2.0.0"); decided {
		t.Fatal("a ceiling above the fix may resolve either side of it")
	}
	if _, decided := Below("<=2.0.0", "2.0.0"); decided {
		t.Fatal("an inclusive ceiling still reaches the fix itself")
	}
}

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

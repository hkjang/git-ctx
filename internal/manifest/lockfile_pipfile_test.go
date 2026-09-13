package manifest

import "testing"

// pipenv writes a package into both default and develop when it is resolved for
// both, at the same version. Emitted twice it is not a cosmetic duplicate: the
// indexer batches packages into one INSERT ... ON CONFLICT DO UPDATE, and
// Postgres refuses a statement that would update the same row twice, so the
// whole repository fails to index.
func TestAPackageInBothSectionsIsEmittedOnce(t *testing.T) {
	const lock = `{
	  "default": {"requests": {"version": "==2.31.0"}, "urllib3": {"version": "==2.0.7"}},
	  "develop": {"requests": {"version": "==2.31.0"}, "pytest": {"version": "==8.0.0"}}
	}`
	packages := parsePipfileLock(lock)
	seen := map[string]int{}
	for _, p := range packages {
		seen[p.Name+" "+p.Version]++
	}
	if seen["requests 2.31.0"] != 1 {
		t.Errorf("requests 2.31.0 appears %d times; the batch insert refuses a repeated row", seen["requests 2.31.0"])
	}
	for _, want := range []string{"urllib3 2.0.7", "pytest 8.0.0"} {
		if seen[want] != 1 {
			t.Errorf("%s appears %d times, want 1", want, seen[want])
		}
	}
}

// The same name at two versions is two packages, not a duplicate.
func TestTheSameNameAtTwoVersionsStaysTwoPackages(t *testing.T) {
	const lock = `{
	  "default": {"requests": {"version": "==2.31.0"}},
	  "develop": {"requests": {"version": "==2.32.0"}}
	}`
	if got := len(parsePipfileLock(lock)); got != 2 {
		t.Errorf("got %d packages, want 2", got)
	}
}

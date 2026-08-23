package search

import (
	"strings"
	"testing"
	"time"
)

// The whole point of weighting is that volume alone is misleading: someone who
// wrote most of a file three years ago and left cannot answer questions about
// it, and a plain commit count would rank them first.
func TestRecencyWeightDecaysByHalfLife(t *testing.T) {
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		days float64
		want float64
	}{
		{"오늘", 0, 1.0},
		{"반감기 1회", ownershipHalfLifeDays, 0.5},
		{"반감기 2회", ownershipHalfLifeDays * 2, 0.25},
		{"1년 전", 365, 0.06},
	}
	for _, c := range cases {
		got := recencyWeight(now.Add(-time.Duration(c.days*24)*time.Hour), now)
		if got < c.want*0.9 || got > c.want*1.1+0.01 {
			t.Errorf("%s: weight = %.3f, want about %.3f", c.name, got, c.want)
		}
	}
	// A commit with no date, or one dated in the future because a machine clock
	// was wrong, must not be dropped or given a negative weight.
	if got := recencyWeight(time.Time{}, now); got != 1 {
		t.Errorf("zero time weight = %v, want 1", got)
	}
	if got := recencyWeight(now.Add(time.Hour), now); got != 1 {
		t.Errorf("future commit weight = %v, want 1", got)
	}
}

func TestRankingPrefersSustainedRecentWorkOverVolume(t *testing.T) {
	now := time.Now().UTC()
	commits := []CommitEntry{}
	// Someone who wrote most of it two years ago.
	for i := 0; i < 40; i++ {
		commits = append(commits, CommitEntry{Author: "떠난사람", AuthorEmail: "gone@example.com",
			AuthoredAt: now.AddDate(-2, 0, -i)})
	}
	// Someone who has been maintaining it this quarter.
	for i := 0; i < 8; i++ {
		commits = append(commits, CommitEntry{Author: "현재유지보수", AuthorEmail: "current@example.com",
			AuthoredAt: now.AddDate(0, 0, -i*5)})
	}

	owners := rankOwners(commits, now, 5)
	if len(owners) != 2 {
		t.Fatalf("owners = %#v", owners)
	}
	if owners[0].Email != "current@example.com" {
		t.Errorf("ranked %s first; volume outranked recency", owners[0].Email)
	}
	// The evidence has to travel with the name, or a reader cannot tell that the
	// second person is historical.
	if owners[1].Commits != 40 || owners[1].LastSeen.After(now.AddDate(-1, 0, 0)) {
		t.Errorf("historical author lost their evidence: %#v", owners[1])
	}
}

// A person commits from a laptop and from CI under different display names.
// Email is what identifies them.
func TestOwnersAreGroupedByEmailNotDisplayName(t *testing.T) {
	now := time.Now().UTC()
	owners := rankOwners([]CommitEntry{
		{Author: "Hong Gil-dong", AuthorEmail: "hong@example.com", AuthoredAt: now},
		{Author: "honggildong", AuthorEmail: "HONG@example.com", AuthoredAt: now.AddDate(0, 0, -1)},
		{Author: "hong", AuthorEmail: "hong@example.com", AuthoredAt: now.AddDate(0, 0, -2)},
	}, now, 5)
	if len(owners) != 1 || owners[0].Commits != 3 {
		t.Fatalf("owners = %#v, want one person with three commits", owners)
	}
}

func TestCommitsWithoutAnAuthorAreSkippedNotCounted(t *testing.T) {
	now := time.Now().UTC()
	owners := rankOwners([]CommitEntry{
		{Author: "", AuthorEmail: "", AuthoredAt: now},
		{Author: "실명", AuthorEmail: "real@example.com", AuthoredAt: now},
	}, now, 5)
	if len(owners) != 1 || owners[0].Email != "real@example.com" {
		t.Fatalf("owners = %#v", owners)
	}
}

func TestFormatOwnersCarriesTheEvidence(t *testing.T) {
	rendered := FormatOwners(OwnershipResult{
		LibraryID: "/core/order", Ref: "main", Path: "internal/order", Examined: 120,
		Owners: []OwnerResult{{Author: "홍길동", Email: "hong@example.com", Commits: 42, Score: 18.5,
			FirstSeen: time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC),
			LastSeen:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}},
	})
	for _, want := range []string{"홍길동", "hong@example.com", "커밋 42건", "2026-08-01", "2024-01-05", "120"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output is missing %q:\n%s", want, rendered)
		}
	}
}

func TestFormatOwnersSaysWhenThereIsNothingToRank(t *testing.T) {
	rendered := FormatOwners(OwnershipResult{Path: "does/not/exist", Diagnostics: []string{"커밋 이력이 없습니다."}})
	if !strings.Contains(rendered, "순위를 매길 커밋 이력이 없습니다") || !strings.Contains(rendered, "커밋 이력이 없습니다.") {
		t.Errorf("an empty ranking must explain itself:\n%s", rendered)
	}
}

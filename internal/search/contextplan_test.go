package search

import (
	"strings"
	"testing"
)

// The planner grounds the question in the symbol index rather than parsing
// language, so what matters is which tokens it is willing to look up.
func TestCandidateIdentifiersKeepsSymbolsAndDropsQuestionWords(t *testing.T) {
	got := candidateIdentifiers("OrderService 수정하려는데 영향 범위 알려줘")
	if len(got) == 0 || got[0] != "OrderService" {
		t.Fatalf("candidates = %v, want OrderService first", got)
	}

	// Longest first, so a qualified name is tried before its last segment.
	ordered := candidateIdentifiers("who calls billing.OrderService and Order")
	if len(ordered) < 2 || ordered[0] != "billing.OrderService" {
		t.Fatalf("candidates = %v, want the qualified name first", ordered)
	}
	for _, word := range []string{"who", "calls", "and"} {
		for _, candidate := range ordered {
			if strings.EqualFold(candidate, word) {
				t.Errorf("question word %q was kept as a candidate", word)
			}
		}
	}

	if got := candidateIdentifiers("무엇이 바뀌나요?"); len(got) != 0 {
		t.Errorf("candidates = %v, want none when nothing names a symbol", got)
	}
}

func TestLooksLikeTestCoversTheEcosystemsInThisIndex(t *testing.T) {
	for _, path := range []string{
		"internal/search/service_test.go",
		"src/test/java/com/acme/OrderServiceTest.java",
		"app/__tests__/order.test.ts",
		"spec/models/order_spec.rb",
		"tests/test_order.py",
		"web/order.spec.js",
	} {
		if !looksLikeTest(path) {
			t.Errorf("looksLikeTest(%q) = false, want true", path)
		}
	}
	for _, path := range []string{
		"internal/search/service.go",
		"src/main/java/com/acme/OrderService.java",
		"web/app.js",
		"docs/testing-guide.md",
	} {
		if looksLikeTest(path) {
			t.Errorf("looksLikeTest(%q) = true, want false", path)
		}
	}
}

// The budget has to be spent where the question is, and a section that is cut
// has to say so: a silently shortened list of callers reads as "this is
// everything", which is the wrong thing to believe about a blast radius.
func TestAllocateSpendsTheBudgetAndReportsWhatWasDropped(t *testing.T) {
	many := make([]string, 200)
	for i := range many {
		many[i] = "- `/core/order` src/main/java/Caller.java:12 — call"
	}
	gathered := map[string]sectionData{
		"dependents":   {candidates: 200, body: strings.Join(many, "\n")},
		"tests":        {candidates: 0, note: "규약에 맞는 테스트 파일 없음"},
		"symbol":       {candidates: 1, body: "`OrderService` class"},
		"dependencies": {candidates: 2, body: "- a\n- b"},
		"history":      {candidates: 1, body: "- abc123"},
	}
	sections := allocate(4000, gathered)

	byName := map[string]ContextSection{}
	for _, section := range sections {
		byName[section.Name] = section
	}

	dependents := byName["dependents"]
	if dependents.Included == 0 || dependents.Included >= 200 {
		t.Fatalf("dependents included = %d of 200, want a partial list", dependents.Included)
	}
	if !strings.Contains(dependents.Note, "생략") {
		t.Errorf("a truncated section must say what it dropped: %q", dependents.Note)
	}

	// An empty section keeps its explanation rather than looking like an absence.
	if !strings.Contains(byName["tests"].Note, "테스트 파일 없음") {
		t.Errorf("tests note = %q, want the reason preserved", byName["tests"].Note)
	}

	// Sections that fit are untouched.
	if byName["dependencies"].Included != 2 || byName["symbol"].Included != 1 {
		t.Errorf("sections that fit were altered: %#v", byName)
	}
	for _, section := range sections {
		if section.Candidates > 0 && section.Included == 0 && section.Note == "" {
			t.Errorf("%s dropped everything without saying why", section.Name)
		}
	}
}

// Unused room moves down the priority order instead of being wasted.
func TestAllocateRedistributesUnusedRoom(t *testing.T) {
	long := strings.Repeat("- caller line\n", 300)
	// The later sections need more content than their own share so that any
	// extra room actually shows up as extra lines.
	dependencies := sectionData{candidates: 300, body: strings.Repeat("- dependency line\n", 300)}
	symbol := sectionData{candidates: 300, body: strings.Repeat("- symbol line\n", 300)}

	withTests := allocate(6000, map[string]sectionData{
		"dependents":   {candidates: 300, body: long},
		"tests":        {candidates: 50, body: strings.Repeat("- test\n", 50)},
		"symbol":       symbol,
		"dependencies": dependencies,
	})
	withoutTests := allocate(6000, map[string]sectionData{
		"dependents":   {candidates: 300, body: long},
		"tests":        {candidates: 0, note: "테스트 규약 미탐지"},
		"symbol":       symbol,
		"dependencies": dependencies,
	})

	find := func(sections []ContextSection, name string) ContextSection {
		for _, section := range sections {
			if section.Name == name {
				return section
			}
		}
		return ContextSection{}
	}
	// dependents is allocated before tests, so its own share is unchanged; the
	// room tests did not use has to reach the sections after it.
	after := find(withoutTests, "dependencies").Included + find(withoutTests, "symbol").Included
	before := find(withTests, "dependencies").Included + find(withTests, "symbol").Included
	if after <= before {
		t.Errorf("unused test budget was not redistributed: %d then %d", before, after)
	}
}

// An unresolved target is answered with the candidates, never with a bundle
// built around a guess.
func TestRenderAsksRatherThanGuessingWhenTheTargetIsAmbiguous(t *testing.T) {
	bundle := ContextBundle{
		Intent: "change-impact",
		Ambiguous: []SymbolResult{
			{QualifiedName: "billing.OrderService", Kind: "class", Language: "java", LibraryID: "/billing/api", FilePath: "a.java", LineStart: 3},
			{QualifiedName: "orders.OrderService", Kind: "class", Language: "java", LibraryID: "/orders/core", FilePath: "b.java", LineStart: 9},
		},
	}
	rendered := bundle.Render()
	for _, want := range []string{"billing.OrderService", "orders.OrderService", "libraryId"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "## 이 심볼을 사용하는 곳") {
		t.Error("an ambiguous target must not render a bundle")
	}
}

func TestRenderCarriesThePlanAndTheAccounting(t *testing.T) {
	bundle := ContextBundle{
		Target: SymbolResult{QualifiedName: "OrderService", Kind: "class"},
		Plan:   []string{"대상 확정", "교차 저장소 사용처 조회"},
		Sections: []ContextSection{
			{Name: "dependents", Title: "이 심볼을 사용하는 곳 (교차 저장소)", Candidates: 47, Included: 12, Body: "- x", Note: "47건 중 12건만 포함했습니다(35건 생략)."},
		},
	}
	rendered := bundle.Render()
	for _, want := range []string{"수집 계획", "교차 저장소 사용처 조회", "35건 생략", "참고 데이터"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output is missing %q:\n%s", want, rendered)
		}
	}
}

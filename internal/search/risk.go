package search

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Listing what changed is not the same as saying whether it is safe to ship.
// get-change-impact returned the diff and the callers inside the same
// repository, leaving the judgement to the reader -- and leaving out the
// consumers in other repositories, who are the ones that break without knowing.
//
// This assesses the change instead. It reports named factors, each with the
// evidence behind it, rather than a single score. A number like "risk 82/100"
// cannot be argued with and invites more confidence than the inputs support;
// a reader who disagrees with one factor can discount it and keep the rest.

// RiskLevel is how much attention a factor or a change deserves.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// RiskFactor is one reason a change is or is not risky.
type RiskFactor struct {
	Name    string
	Level   RiskLevel
	Summary string
	// Evidence is what the factor was derived from, so the reader can check it
	// rather than take the level on trust.
	Evidence []string
}

// ChangeAssessment is a change, what it touches, and what that implies.
type ChangeAssessment struct {
	LibraryID, BaseRef, HeadRef string
	Comparison                  RefComparison
	Factors                     []RiskFactor
	Level                       RiskLevel
	// Assessed is how many changed symbols were followed. A large diff is capped,
	// and a capped assessment must not read as a complete one.
	Assessed    int
	Diagnostics []string
}

// maxAssessedSymbols bounds the work: each symbol costs a cross-repository
// dependent search and a test search, so a thousand-symbol refactor would
// otherwise spend the whole tool budget before answering.
const maxAssessedSymbols = 25

// AssessChange follows a ref comparison through the graph and reports what the
// change puts at risk.
func (s *Service) AssessChange(ctx context.Context, principals []string, libraryID, baseRef, headRef string) (ChangeAssessment, error) {
	if len(principals) == 0 {
		return ChangeAssessment{}, fmt.Errorf("no repository permissions are available for this caller")
	}
	comparison, err := s.CompareRefs(ctx, principals, libraryID, baseRef, headRef)
	if err != nil {
		return ChangeAssessment{}, err
	}
	result := ChangeAssessment{
		LibraryID: comparison.LibraryID, BaseRef: comparison.BaseRef, HeadRef: comparison.HeadRef,
		Comparison: comparison, Level: RiskLow,
	}
	if len(comparison.Changes) == 0 {
		result.Diagnostics = append(result.Diagnostics, "두 ref 사이에 심볼 수준의 변화가 없습니다.")
		return result, nil
	}

	assessed := comparison.Changes
	if len(assessed) > maxAssessedSymbols {
		assessed = assessed[:maxAssessedSymbols]
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("변경된 심볼 %d개 중 %d개만 추적했습니다. 나머지는 평가에 포함되지 않았습니다.",
				len(comparison.Changes), maxAssessedSymbols))
	}
	result.Assessed = len(assessed)

	result.Factors = append(result.Factors, contractFactor(comparison.Changes))
	result.Factors = append(result.Factors, s.consumerFactor(ctx, principals, comparison.LibraryID, assessed))
	result.Factors = append(result.Factors, s.coverageFactor(ctx, principals, comparison.LibraryID, headRef, assessed))
	result.Factors = append(result.Factors, schemaFactor(comparison.Changes))
	if factor, ok := s.freshnessFactor(ctx, principals, comparison.LibraryID); ok {
		result.Factors = append(result.Factors, factor)
	}

	// The overall level is the highest factor, stated rather than computed from
	// weights nobody can check. One high factor is enough: a removed symbol with
	// consumers is dangerous however good the test coverage is.
	for _, factor := range result.Factors {
		if factor.Level == RiskHigh {
			result.Level = RiskHigh
			break
		}
		if factor.Level == RiskMedium {
			result.Level = RiskMedium
		}
	}
	return result, nil
}

// contractFactor judges the diff itself. A removed symbol breaks every caller;
// a changed signature breaks the ones passing what no longer fits.
func contractFactor(changes []RefChange) RiskFactor {
	var removed, resigned []string
	for _, change := range changes {
		switch {
		case change.Type == "removed":
			removed = append(removed, change.Name)
		case change.Type == "modified" && change.BeforeSignature != "" &&
			change.AfterSignature != "" && change.BeforeSignature != change.AfterSignature:
			resigned = append(resigned, fmt.Sprintf("%s: %s → %s", change.Name, change.BeforeSignature, change.AfterSignature))
		}
	}
	factor := RiskFactor{Name: "api-contract"}
	switch {
	case len(removed) > 0:
		factor.Level = RiskHigh
		factor.Summary = fmt.Sprintf("심볼 %d개가 제거됐습니다. 이를 호출하던 코드는 모두 깨집니다.", len(removed))
		factor.Evidence = capped(removed, 5)
	case len(resigned) > 0:
		factor.Level = RiskMedium
		factor.Summary = fmt.Sprintf("심볼 %d개의 시그니처가 바뀌었습니다. 기존 호출부와 맞지 않을 수 있습니다.", len(resigned))
		factor.Evidence = capped(resigned, 5)
	default:
		factor.Level = RiskLow
		factor.Summary = "제거되거나 시그니처가 바뀐 심볼이 없습니다."
	}
	return factor
}

// consumerFactor is the one get-change-impact was missing: who outside this
// repository uses what changed. They are the consumers who do not know.
func (s *Service) consumerFactor(ctx context.Context, principals []string, libraryID string, changes []RefChange) RiskFactor {
	factor := RiskFactor{Name: "cross-repository-consumers"}
	external := map[string]int{}
	for _, change := range changes {
		found, err := s.FindDependents(ctx, principals, change.Name, "", 100)
		if err != nil {
			continue
		}
		for _, dependent := range found.Dependents {
			if dependent.LibraryID == libraryID {
				continue
			}
			external[dependent.LibraryID]++
		}
	}
	if len(external) == 0 {
		factor.Level = RiskLow
		factor.Summary = "다른 저장소에서 이 심볼들을 참조하는 색인된 코드가 없습니다."
		return factor
	}
	names := make([]string, 0, len(external))
	for libraryID, count := range external {
		names = append(names, fmt.Sprintf("%s (%d건)", libraryID, count))
	}
	sort.Strings(names)
	factor.Level = RiskHigh
	if len(external) == 1 {
		factor.Level = RiskMedium
	}
	factor.Summary = fmt.Sprintf("다른 저장소 %d곳이 변경된 심볼을 참조합니다.", len(external))
	factor.Evidence = capped(names, 5)
	return factor
}

// coverageFactor asks whether anything would catch a mistake. A change to code
// no test references will fail in production or not at all.
func (s *Service) coverageFactor(ctx context.Context, principals []string, libraryID, ref string, changes []RefChange) RiskFactor {
	factor := RiskFactor{Name: "test-coverage"}
	var uncovered []string
	covered := 0
	for _, change := range changes {
		if change.Type == "removed" {
			continue
		}
		tests, err := s.FindTests(ctx, principals, change.Name, libraryID, ref, 5)
		if err != nil {
			continue
		}
		referencing := 0
		for _, test := range tests.Tests {
			if test.Origin == TestReferences {
				referencing++
			}
		}
		if referencing > 0 {
			covered++
			continue
		}
		uncovered = append(uncovered, change.Name)
	}
	switch {
	case len(uncovered) == 0 && covered > 0:
		factor.Level = RiskLow
		factor.Summary = fmt.Sprintf("변경된 심볼 %d개 모두 이를 참조하는 테스트가 있습니다.", covered)
	case covered == 0 && len(uncovered) > 0:
		factor.Level = RiskHigh
		factor.Summary = fmt.Sprintf("변경된 심볼 %d개 중 어느 것도 참조하는 테스트가 없습니다.", len(uncovered))
		factor.Evidence = capped(uncovered, 5)
	case len(uncovered) > 0:
		factor.Level = RiskMedium
		factor.Summary = fmt.Sprintf("변경된 심볼 %d개는 참조하는 테스트가 없습니다(%d개는 있음).", len(uncovered), covered)
		factor.Evidence = capped(uncovered, 5)
	default:
		factor.Level = RiskLow
		factor.Summary = "테스트 커버리지를 판단할 변경이 없습니다."
	}
	return factor
}

// schemaFactor flags changes to files that define storage. A schema change
// cannot be rolled back by reverting a deploy.
func schemaFactor(changes []RefChange) RiskFactor {
	factor := RiskFactor{Name: "schema"}
	var files []string
	seen := map[string]bool{}
	for _, change := range changes {
		extension := strings.ToLower(path.Ext(change.FilePath))
		if extension != ".sql" && extension != ".ddl" {
			continue
		}
		if seen[change.FilePath] {
			continue
		}
		seen[change.FilePath] = true
		files = append(files, change.FilePath)
	}
	if len(files) == 0 {
		factor.Level = RiskLow
		factor.Summary = "스키마 파일 변경이 없습니다."
		return factor
	}
	sort.Strings(files)
	factor.Level = RiskHigh
	factor.Summary = fmt.Sprintf("스키마 파일 %d개가 변경됐습니다. 배포를 되돌려도 스키마는 되돌아가지 않습니다.", len(files))
	factor.Evidence = capped(files, 5)
	return factor
}

// freshnessFactor says when the consumer search was drawn from a stale index.
// A short list of consumers means little if the index has not seen the last six
// weeks of other teams' code.
func (s *Service) freshnessFactor(ctx context.Context, principals []string, libraryID string) (RiskFactor, bool) {
	ages, err := s.IndexAges(ctx, principals, []string{libraryID}, time.Now().UTC())
	if err != nil || len(ages) == 0 {
		return RiskFactor{}, false
	}
	oldest := ages[0]
	if oldest.Age < staleIndexAfter {
		return RiskFactor{}, false
	}
	return RiskFactor{
		Name:     "index-freshness",
		Level:    RiskMedium,
		Summary:  fmt.Sprintf("색인이 %s 전 것입니다. 그 이후 생긴 사용처는 위 목록에 없습니다.", humanDays(oldest.Age)),
		Evidence: []string{fmt.Sprintf("%s @ %s", oldest.LibraryID, oldest.CommitID)},
	}, true
}

func capped(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	out := append([]string{}, values[:limit]...)
	return append(out, fmt.Sprintf("외 %d건", len(values)-limit))
}

// FormatAssessment renders the factors with their evidence. The overall level
// comes last, after what it was derived from, so a reader meets the reasoning
// before the verdict rather than the other way round.
func FormatAssessment(result ChangeAssessment) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# 변경 위험 평가: %s %s → %s\n\n", result.LibraryID, result.BaseRef, result.HeadRef)
	fmt.Fprintf(&out, "심볼 변경 %d건", len(result.Comparison.Changes))
	if result.Assessed > 0 && result.Assessed < len(result.Comparison.Changes) {
		fmt.Fprintf(&out, " (그중 %d건 추적)", result.Assessed)
	}
	out.WriteString("\n\n")

	for _, factor := range result.Factors {
		fmt.Fprintf(&out, "## %s — %s\n\n%s\n", factor.Name, strings.ToUpper(string(factor.Level)), factor.Summary)
		for _, evidence := range factor.Evidence {
			fmt.Fprintf(&out, "- %s\n", evidence)
		}
		out.WriteString("\n")
	}

	fmt.Fprintf(&out, "## 종합: %s\n\n", strings.ToUpper(string(result.Level)))
	out.WriteString("가장 높은 개별 항목을 그대로 종합으로 삼았습니다. 가중 합산이 아니라, 항목 하나가 위험하면 나머지가 안전해도 위험하기 때문입니다.\n")

	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(&out, "\n_%s_\n", diagnostic)
	}
	return strings.TrimSpace(out.String()) + "\n"
}

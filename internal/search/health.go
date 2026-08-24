package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// "How healthy is this repository" is usually answered with a set of scores --
// documentation 91, complexity 66. Those numbers cannot be argued with and imply
// a precision the inputs do not have: counting README files does not measure
// documentation quality, and nothing here measures complexity at all.
//
// This reports measurements with what they were counted from, and flags the
// conditions it can actually detect. A reader who disagrees with one measure can
// discount it; a score would have hidden it inside an average.

// HealthMeasure is one thing that was counted, and what it was counted over.
type HealthMeasure struct {
	Name string
	// Value and Total are the raw counts. A ratio is left to the reader, who can
	// see both numbers and judge whether the denominator is large enough to mean
	// anything.
	Value, Total int
	Detail       string
}

// HealthFlag is a condition worth an operator's attention, with examples.
type HealthFlag struct {
	Name     string
	Summary  string
	Examples []string
}

type RepositoryHealth struct {
	LibraryID, Ref string
	Measures       []HealthMeasure
	Flags          []HealthFlag
	// NotMeasured names what this deliberately does not claim, so its absence is
	// not mistaken for a clean result.
	NotMeasured []string
	Diagnostics []string
}

// RepositoryHealthReport counts what the index can support for one repository.
func (s *Service) RepositoryHealthReport(ctx context.Context, principals []string, libraryID, requestedRef string) (RepositoryHealth, error) {
	repositoryID, baseID, ref, err := s.authorizedRepository(ctx, principals, libraryID, requestedRef)
	if err != nil {
		return RepositoryHealth{}, err
	}
	result := RepositoryHealth{LibraryID: baseID, Ref: ref}

	symbols, testedSymbols, untested, err := s.symbolCoverage(ctx, repositoryID, ref)
	if err != nil {
		return RepositoryHealth{}, err
	}
	if symbols == 0 {
		result.Diagnostics = append(result.Diagnostics,
			"이 ref 에 색인된 심볼이 없습니다. 아직 색인되지 않았거나, 심볼 그래프가 없는 언어일 수 있습니다.")
	}
	result.Measures = append(result.Measures, HealthMeasure{
		Name: "tests-referencing-symbols", Value: testedSymbols, Total: symbols,
		Detail: "테스트 파일이 참조하는 심볼 수 / 전체 심볼 수. 파일 이름이 비슷한 테스트는 세지 않습니다.",
	})
	if len(untested) > 0 && symbols > 0 {
		result.Flags = append(result.Flags, HealthFlag{
			Name:     "untested-symbols",
			Summary:  fmt.Sprintf("심볼 %d개를 참조하는 테스트가 없습니다.", len(untested)),
			Examples: capped(untested, 5),
		})
	}

	unreferenced, err := s.unreferencedSymbols(ctx, repositoryID, ref)
	if err != nil {
		return RepositoryHealth{}, err
	}
	result.Measures = append(result.Measures, HealthMeasure{
		Name: "symbols-referenced-somewhere", Value: symbols - len(unreferenced), Total: symbols,
		Detail: "다른 코드가 참조하는 심볼 수. 참조가 없다고 해서 죽은 코드라는 뜻은 아닙니다 — 진입점, 핸들러, 리플렉션으로 호출되는 코드는 여기 잡히지 않습니다.",
	})
	if len(unreferenced) > 0 {
		result.Flags = append(result.Flags, HealthFlag{
			Name:     "unreferenced-symbols",
			Summary:  fmt.Sprintf("심볼 %d개는 색인된 코드 어디에서도 참조되지 않습니다. 확인해 볼 후보이지, 삭제 목록이 아닙니다.", len(unreferenced)),
			Examples: capped(unreferenced, 5),
		})
	}

	conventions := s.conventionFiles(ctx, repositoryID, ref)
	result.Measures = append(result.Measures, HealthMeasure{
		Name: "convention-files", Value: len(conventions), Total: 0,
		Detail: "README·CLAUDE.md·AGENTS.md·ADR 등 기여 방식을 설명하는 파일 수. 내용의 품질은 보지 않습니다.",
	})
	if len(conventions) == 0 {
		result.Flags = append(result.Flags, HealthFlag{
			Name:    "no-conventions",
			Summary: "기여 방식을 설명하는 파일을 찾지 못했습니다. 이 저장소에 처음 오는 사람은 코드만 보고 관례를 추측해야 합니다.",
		})
	}

	if ages, ageErr := s.IndexAges(ctx, principals, []string{baseID}, time.Now().UTC()); ageErr == nil && len(ages) > 0 {
		result.Measures = append(result.Measures, HealthMeasure{
			Name: "index-age-days", Value: int(ages[0].Age.Hours() / 24), Total: 0,
			Detail: "마지막 색인 이후 경과일. 위 수치는 모두 그 시점의 코드에 대한 것입니다.",
		})
		if ages[0].Age >= staleIndexAfter {
			result.Flags = append(result.Flags, HealthFlag{
				Name:    "stale-index",
				Summary: fmt.Sprintf("색인이 %s 전 것이라 위 수치가 현재 코드와 다를 수 있습니다.", humanDays(ages[0].Age)),
			})
		}
	}

	// Naming what was not looked at keeps a short report from reading as a clean
	// bill of health.
	result.NotMeasured = []string{
		"복잡도 — 색인기가 순환복잡도를 추출하지 않습니다.",
		"변경 빈도(핫스팟)와 지식 편중 — 파일별 커밋 이력이 필요하고, 저장소 전체에 대해서는 소스 API 호출 비용이 큽니다. find-code-owner 로 경로 단위 확인은 가능합니다.",
		"순환 의존 — 의존 대상이 문자열로 저장돼 심볼로 확정 해소되지 않아, 오탐 없이 판정할 수 없습니다.",
		"문서의 최신성 — 소스가 주는 문서 수정 시각을 아직 저장하지 않습니다.",
	}
	return result, nil
}

// symbolCoverage counts symbols and how many a test file references.
func (s *Service) symbolCoverage(ctx context.Context, repositoryID, ref string) (total, tested int, untested []string, err error) {
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(
		`SELECT name,qualified_name FROM code_symbols WHERE repository_id=? AND ref_name=? ORDER BY name`), repositoryID, ref)
	if err != nil {
		return 0, 0, nil, err
	}
	names := map[string]string{}
	for rows.Next() {
		var name, qualified string
		if err = rows.Scan(&name, &qualified); err != nil {
			rows.Close()
			return 0, 0, nil, err
		}
		if _, present := names[name]; !present {
			names[name] = qualified
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, 0, nil, err
	}

	// One pass over the dependencies, keeping only the ones written from a test
	// file. Asking per symbol would be one query per symbol.
	depRows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(
		`SELECT file_path,target FROM code_dependencies WHERE repository_id=? AND ref_name=?`), repositoryID, ref)
	if err != nil {
		return 0, 0, nil, err
	}
	defer depRows.Close()
	referencedByTest := map[string]bool{}
	for depRows.Next() {
		var filePath, target string
		if err = depRows.Scan(&filePath, &target); err != nil {
			return 0, 0, nil, err
		}
		if looksLikeTest(filePath) {
			referencedByTest[target] = true
		}
	}
	if err = depRows.Err(); err != nil {
		return 0, 0, nil, err
	}

	for name, qualified := range names {
		if referencedByTest[name] || referencedByTest[qualified] {
			tested++
			continue
		}
		untested = append(untested, name)
	}
	sort.Strings(untested)
	return len(names), tested, untested, nil
}

// unreferencedSymbols lists symbols nothing in the index depends on. It is a
// list of candidates to look at, not of code to delete: entry points, handlers
// and anything reached by reflection have no recorded caller.
func (s *Service) unreferencedSymbols(ctx context.Context, repositoryID, ref string) ([]string, error) {
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(
		`SELECT s.name FROM code_symbols s
WHERE s.repository_id=? AND s.ref_name=?
AND NOT EXISTS (SELECT 1 FROM code_dependencies d WHERE d.target=s.name OR d.target=s.qualified_name)
ORDER BY s.name`), repositoryID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, rows.Err()
}

// FormatHealth renders the measurements with their basis, and what was not
// looked at. A short report with nothing flagged should not read as a clean bill
// of health when most of the checks were never run.
func FormatHealth(result RepositoryHealth) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# 저장소 상태: %s (%s)\n\n", result.LibraryID, result.Ref)
	out.WriteString("> 점수 대신 센 값과 그 근거를 그대로 냅니다. 분모를 보고 그 수치가 의미 있는지 직접 판단하세요.\n\n")

	out.WriteString("## 측정\n\n")
	for _, measure := range result.Measures {
		if measure.Total > 0 {
			fmt.Fprintf(&out, "- **%s**: %d / %d\n  %s\n", measure.Name, measure.Value, measure.Total, measure.Detail)
			continue
		}
		fmt.Fprintf(&out, "- **%s**: %d\n  %s\n", measure.Name, measure.Value, measure.Detail)
	}
	out.WriteString("\n")

	if len(result.Flags) > 0 {
		out.WriteString("## 확인할 것\n\n")
		for _, flag := range result.Flags {
			fmt.Fprintf(&out, "- **%s** — %s\n", flag.Name, flag.Summary)
			for _, example := range flag.Examples {
				fmt.Fprintf(&out, "  - %s\n", example)
			}
		}
		out.WriteString("\n")
	}

	out.WriteString("## 보지 않은 것\n\n")
	for _, item := range result.NotMeasured {
		fmt.Fprintf(&out, "- %s\n", item)
	}
	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(&out, "\n_%s_\n", diagnostic)
	}
	return strings.TrimSpace(out.String()) + "\n"
}

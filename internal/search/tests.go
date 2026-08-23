package search

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// "Which tests should I run for this change" is answered badly by filename
// proximity alone. A test named after a type is a good guess, but the thing that
// actually exercises a symbol is a test that references it -- and that is an
// edge already stored in code_dependencies.
//
// Both signals are used and both are labelled, because they mean different
// things. A test that calls the symbol will fail if the change is wrong. A test
// that merely lives beside it might not touch it at all.

// TestOrigin says why a test is in the list, so the caller can judge it.
type TestOrigin string

const (
	// TestReferences means the dependency graph records this test calling or
	// importing the symbol. It is the strong signal.
	TestReferences TestOrigin = "references"
	// TestNearby means the file is a test living beside the code. It is a guess,
	// and it is the only signal available for languages with no symbol graph.
	TestNearby TestOrigin = "nearby"
)

type TestResult struct {
	LibraryID, Ref, FilePath, FromSymbol string
	LineNumber                           int
	Origin                               TestOrigin
}

type TestSearch struct {
	Target      string
	Tests       []TestResult
	Diagnostics []string
}

// FindTests returns the tests that exercise a symbol, strongest signal first.
func (s *Service) FindTests(ctx context.Context, principals []string, symbol, libraryID, ref string, limit int) (TestSearch, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return TestSearch{}, fmt.Errorf("symbol is required")
	}
	if len(principals) == 0 {
		return TestSearch{}, fmt.Errorf("no repository permissions are available for this caller")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	result := TestSearch{Target: symbol}

	referencing, err := s.testsReferencing(ctx, principals, symbol, libraryID, limit)
	if err != nil {
		return TestSearch{}, err
	}
	result.Tests = referencing

	// Filename proximity fills the gap for languages the symbol graph does not
	// cover, and for tests that exercise a symbol indirectly.
	if len(result.Tests) < limit {
		nearby := s.testsNearby(ctx, principals, symbol, libraryID, ref, limit-len(result.Tests), result.Tests)
		result.Tests = append(result.Tests, nearby...)
	}

	if len(result.Tests) == 0 {
		result.Diagnostics = append(result.Diagnostics,
			"이 심볼을 참조하는 테스트도, 이름이 맞는 테스트 파일도 찾지 못했습니다. 해당 언어에 심볼 그래프가 없거나 테스트가 없을 수 있습니다.")
		return result, nil
	}
	strong := 0
	for _, test := range result.Tests {
		if test.Origin == TestReferences {
			strong++
		}
	}
	if strong == 0 {
		result.Diagnostics = append(result.Diagnostics,
			"이 심볼을 직접 참조하는 테스트는 없습니다. 아래는 파일 이름과 위치로 추정한 것이라 실제로 이 코드를 다루지 않을 수 있습니다.")
	}
	return result, nil
}

// testsReferencing finds tests through the dependency graph: a test file whose
// symbols call or import the target.
func (s *Service) testsReferencing(ctx context.Context, principals []string, symbol, libraryID string, limit int) ([]TestResult, error) {
	join, predicate, args := repositoryACL(principals)
	statement := `SELECT r.library_id,d.ref_name,d.file_path,d.from_symbol,d.line_number
FROM code_dependencies d JOIN repositories r ON r.id=d.repository_id ` + join + `
WHERE r.enabled=1 AND ` + predicate + ` AND LOWER(d.target)=LOWER(?)`
	args = append(args, symbol)
	if strings.TrimSpace(libraryID) != "" {
		// Resolving through authorizedRepository keeps the scope check on the
		// same path every other tool uses, rather than trusting the argument.
		if _, baseID, _, err := s.authorizedRepository(ctx, principals, libraryID, ""); err == nil && baseID != "" {
			statement += ` AND r.library_id=?`
			args = append(args, baseID)
		}
	}
	statement += ` ORDER BY r.library_id,d.file_path,d.line_number LIMIT ?`
	// Over-fetch: most dependencies are production code, and the tests among
	// them are filtered in Go rather than with a LIKE the database cannot index.
	args = append(args, limit*20)

	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestResult
	for rows.Next() {
		var current TestResult
		if err = rows.Scan(&current.LibraryID, &current.Ref, &current.FilePath, &current.FromSymbol, &current.LineNumber); err != nil {
			return nil, err
		}
		if !looksLikeTest(current.FilePath) {
			continue
		}
		current.Origin = TestReferences
		out = append(out, current)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// testsNearby finds test files named after the symbol or sitting beside its
// definition, skipping anything the graph already reported.
func (s *Service) testsNearby(ctx context.Context, principals []string, symbol, libraryID, ref string, limit int, already []TestResult) []TestResult {
	seen := map[string]bool{}
	for _, test := range already {
		seen[test.LibraryID+":"+test.FilePath] = true
	}
	patterns := []string{symbol + "*"}
	// The symbol's own directory is worth sweeping, but only once its definition
	// is known.
	if found, err := s.FindSymbols(ctx, principals, libraryID, ref, symbol, "", 1); err == nil && len(found) == 1 {
		patterns = append(patterns, path.Join(path.Dir(found[0].FilePath), "*"))
	}
	var out []TestResult
	for _, pattern := range patterns {
		files, err := s.FindFiles(ctx, principals, pattern, libraryID, "", "", "", ref, 100)
		if err != nil {
			continue
		}
		for _, file := range files.Files {
			key := file.LibraryID + ":" + file.Path
			if seen[key] || !looksLikeTest(file.Path) {
				continue
			}
			seen[key] = true
			out = append(out, TestResult{LibraryID: file.LibraryID, Ref: file.Ref, FilePath: file.Path, Origin: TestNearby})
			if len(out) >= limit {
				return out
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FilePath < out[j].FilePath })
	return out
}

// FormatTests renders the list with each entry's origin, so a reader can tell a
// test that provably touches the symbol from one that merely looks related.
func FormatTests(result TestSearch) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s 를 다루는 테스트\n\n", result.Target)
	var referencing, nearby []TestResult
	for _, test := range result.Tests {
		if test.Origin == TestReferences {
			referencing = append(referencing, test)
			continue
		}
		nearby = append(nearby, test)
	}
	if len(referencing) > 0 {
		out.WriteString("## 이 심볼을 참조함 (의존성 그래프 확인)\n\n")
		for _, test := range referencing {
			fmt.Fprintf(&out, "- `%s` %s:%d", test.LibraryID, test.FilePath, test.LineNumber)
			if test.FromSymbol != "" {
				fmt.Fprintf(&out, " — %s", test.FromSymbol)
			}
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
	if len(nearby) > 0 {
		out.WriteString("## 이름·위치로 추정 (참조 여부 미확인)\n\n")
		for _, test := range nearby {
			fmt.Fprintf(&out, "- `%s` %s\n", test.LibraryID, test.FilePath)
		}
		out.WriteString("\n")
	}
	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(&out, "_%s_\n", diagnostic)
	}
	return strings.TrimSpace(out.String()) + "\n"
}

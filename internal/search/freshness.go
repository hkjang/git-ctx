package search

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// An assembled answer reads as current. It is not: everything in it came from an
// index built at some point in the past, and if that was six weeks ago then the
// answer is six weeks old however confidently it is worded.
//
// This reports the age of the index behind an answer, not the age of the content
// in it. Those differ, and the difference is worth stating plainly: git does not
// carry a modification time per file in a tree listing, so dating the content
// itself would mean one commit lookup per file at index time, which is not
// affordable on a large repository. Index age is what is knowable cheaply, and
// it is a ceiling on how current anything derived from it can be.

// IndexAge is how stale one repository ref's index is.
type IndexAge struct {
	LibraryID, Ref, CommitID string
	IndexedAt                time.Time
	Age                      time.Duration
}

// staleIndexAfter is when index age starts being worth mentioning. A week is
// long enough that a normal indexing schedule has run several times, so passing
// it suggests something is wrong rather than merely recent.
const staleIndexAfter = 7 * 24 * time.Hour

// IndexAges reports when the given libraries were last indexed, oldest first.
// The ACL is applied, so this cannot reveal that a repository exists.
func (s *Service) IndexAges(ctx context.Context, principals []string, libraryIDs []string, now time.Time) ([]IndexAge, error) {
	if len(principals) == 0 || len(libraryIDs) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	placeholders := make([]string, 0, len(libraryIDs))
	join, predicate, args := repositoryACL(principals)
	for _, libraryID := range libraryIDs {
		base := libraryID
		// A library may arrive with a ref appended as "/library/id/ref"; the
		// stored identity is the library itself.
		if index := strings.LastIndex(strings.TrimPrefix(base, "/"), "/"); index > 0 {
			candidate := "/" + strings.TrimPrefix(base, "/")[:index]
			if strings.Count(candidate, "/") >= 2 {
				base = candidate
			}
		}
		if seen[base] {
			continue
		}
		seen[base] = true
		placeholders = append(placeholders, "?")
		args = append(args, base)
	}
	if len(placeholders) == 0 {
		return nil, nil
	}
	statement := `SELECT r.library_id,rs.ref_name,rs.commit_id,rs.indexed_at
FROM repository_ref_states rs JOIN repositories r ON r.id=rs.repository_id ` + join + `
WHERE r.enabled=1 AND ` + predicate + ` AND r.library_id IN (` + strings.Join(placeholders, ",") + `)
ORDER BY rs.indexed_at`
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexAge
	for rows.Next() {
		var current IndexAge
		if err = rows.Scan(&current.LibraryID, &current.Ref, &current.CommitID, &current.IndexedAt); err != nil {
			return nil, err
		}
		current.Age = now.Sub(current.IndexedAt)
		if current.Age < 0 {
			current.Age = 0
		}
		out = append(out, current)
	}
	return out, rows.Err()
}

// FreshnessNote is the line to attach to an answer, or empty when every index
// behind it is recent enough not to be worth the reader's attention.
func FreshnessNote(ages []IndexAge) string {
	var stale []IndexAge
	for _, age := range ages {
		if age.Age >= staleIndexAfter {
			stale = append(stale, age)
		}
	}
	if len(stale) == 0 {
		return ""
	}
	parts := make([]string, 0, len(stale))
	for _, age := range stale {
		if len(parts) == 3 {
			parts = append(parts, fmt.Sprintf("외 %d곳", len(stale)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %s전", age.LibraryID, humanDays(age.Age)))
	}
	return "색인이 오래됐습니다: " + strings.Join(parts, ", ") +
		". 이 답변은 그 시점의 코드를 반영하며, 이후 변경은 포함되지 않았습니다."
}

func humanDays(age time.Duration) string {
	days := int(age.Hours() / 24)
	if days >= 30 {
		return fmt.Sprintf("%d개월", days/30)
	}
	return fmt.Sprintf("%d일", days)
}

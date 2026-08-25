package search

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// "Who knows this code" is a question about people, and the platform has the
// commit trail that answers it. git blame names whoever touched a line last,
// which is often whoever ran a formatter; what a person actually wants is who
// has worked on this repeatedly and recently enough to still remember it.
//
// The commits are read live rather than stored. The indexer's change table
// records the ref's tip commit against every file a re-index touched, so it
// cannot attribute a file to the person who changed it -- and a stored copy
// would be a second source of truth that goes stale between index runs. One
// path-scoped call to the source answers this exactly, and the same call
// already backs get-file-history.

// OwnerResult is one person's claim on a path, with the evidence for it.
type OwnerResult struct {
	Author, Email string
	// Commits is how many of the commits examined were theirs.
	Commits int
	// Score weights those commits by recency, so someone who worked on this for
	// a year and left ranks below someone active last month.
	Score float64
	// FirstSeen and LastSeen bound their involvement, which is what tells a
	// reader whether "expert" means current or historical.
	FirstSeen, LastSeen time.Time
}

// OwnershipResult answers a who-knows-this question for one path.
type OwnershipResult struct {
	LibraryID, Ref, Path string
	// Declared are the CODEOWNERS rules covering this path. A declaration is an
	// answer someone wrote down on purpose, so it leads; the commit ranking
	// below it is the evidence of who has actually been working there.
	Declared []OwnerDeclaration
	Owners   []OwnerResult
	// Examined is how many commits the ranking is based on. A ranking drawn from
	// four commits deserves less confidence than one drawn from two hundred, and
	// the caller cannot tell without being told.
	Examined    int
	Diagnostics []string
}

// ownershipHalfLifeDays is how quickly a commit's weight decays. Ninety days is
// roughly the point where someone stops being able to answer questions about a
// change from memory.
const ownershipHalfLifeDays = 90.0

// recencyWeight halves a commit's contribution every half-life. A commit from
// today counts 1.0, one from three months ago 0.5, one from a year ago 0.06.
func recencyWeight(authored, now time.Time) float64 {
	if authored.IsZero() || authored.After(now) {
		return 1
	}
	days := now.Sub(authored).Hours() / 24
	return math.Pow(0.5, days/ownershipHalfLifeDays)
}

// FindOwners ranks the people who have worked on a path. The path may be a file
// or a directory; the source returns the commits that touched anything under it.
func (s *Service) FindOwners(ctx context.Context, principals []string, libraryID, repository, filePath, ref string, limit int) (OwnershipResult, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return OwnershipResult{}, fmt.Errorf("path is required; give a file or a directory")
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}
	// Resolved first, and separately from the history call: the declaration is
	// indexed here and must still answer when the source server cannot.
	target, resolveErr := s.resolveRepositoryPath(ctx, principals, libraryID, repository, filePath, ref)
	result := OwnershipResult{LibraryID: libraryID, Ref: ref, Path: filePath}
	if resolveErr == nil {
		result.LibraryID, result.Ref, result.Path = target.libraryID, target.refName, target.path
		declared, source, declaredErr := s.declaredOwners(ctx, principals, target.repositoryID, target.refName, target.path)
		switch {
		case declaredErr != nil:
			result.Diagnostics = append(result.Diagnostics, "codeowners: "+declaredErr.Error())
		case len(declared) > 0:
			result.Declared = declared
			result.Diagnostics = append(result.Diagnostics,
				fmt.Sprintf("codeowners: %s 의 규칙 %d건이 이 경로를 덮습니다. 마지막 규칙이 우선합니다.", source, len(declared)))
		}
	}

	// The history call applies the repository ACL, so ownership cannot reveal
	// activity in a repository the caller may not read.
	history, err := s.FileHistory(ctx, principals, libraryID, repository, filePath, ref, 200)
	if err != nil {
		// A declared owner is an answer on its own. Failing the whole call
		// because the source server could not be reached would throw away the
		// part of the answer this platform already holds.
		if len(result.Declared) > 0 {
			result.Diagnostics = append(result.Diagnostics,
				"history: 커밋 이력을 읽지 못해 선언된 소유자만 답합니다: "+err.Error())
			return result, nil
		}
		return OwnershipResult{}, err
	}
	result.LibraryID, result.Ref, result.Path = history.LibraryID, history.Ref, history.Path
	result.Examined = len(history.Commits)
	result.Diagnostics = append(result.Diagnostics, history.Diagnostics...)
	if len(history.Commits) == 0 {
		if len(result.Declared) == 0 {
			result.Diagnostics = append(result.Diagnostics,
				"이 경로에 대한 커밋 이력이 없습니다. 경로가 맞는지, 저장소가 연결돼 있는지 확인하세요.")
		}
		return result, nil
	}

	result.Owners = rankOwners(history.Commits, time.Now().UTC(), limit)
	return result, nil
}

// rankOwners turns a commit trail into a ranking. It is separate from the
// fetching so the weighting can be exercised directly: how people are ranked is
// the part with judgement in it.
func rankOwners(commits []CommitEntry, now time.Time, limit int) []OwnerResult {
	type tally struct {
		author, email       string
		commits             int
		score               float64
		firstSeen, lastSeen time.Time
	}
	byPerson := map[string]*tally{}
	for _, commit := range commits {
		// Email identifies a person more reliably than a display name, which
		// varies between a laptop and a CI runner.
		key := strings.ToLower(strings.TrimSpace(commit.AuthorEmail))
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(commit.Author))
		}
		if key == "" {
			continue
		}
		current := byPerson[key]
		if current == nil {
			current = &tally{author: commit.Author, email: commit.AuthorEmail, firstSeen: commit.AuthoredAt, lastSeen: commit.AuthoredAt}
			byPerson[key] = current
		}
		current.commits++
		current.score += recencyWeight(commit.AuthoredAt, now)
		if !commit.AuthoredAt.IsZero() {
			if current.firstSeen.IsZero() || commit.AuthoredAt.Before(current.firstSeen) {
				current.firstSeen = commit.AuthoredAt
			}
			if commit.AuthoredAt.After(current.lastSeen) {
				current.lastSeen = commit.AuthoredAt
			}
		}
	}

	owners := make([]OwnerResult, 0, len(byPerson))
	for _, current := range byPerson {
		owners = append(owners, OwnerResult{
			Author: current.author, Email: current.email, Commits: current.commits,
			Score: current.score, FirstSeen: current.firstSeen, LastSeen: current.lastSeen,
		})
	}
	sort.SliceStable(owners, func(i, j int) bool {
		if owners[i].Score != owners[j].Score {
			return owners[i].Score > owners[j].Score
		}
		return owners[i].Commits > owners[j].Commits
	})
	if len(owners) > limit {
		owners = owners[:limit]
	}
	return owners
}

// FormatOwners renders the ranking with the evidence behind it. A name on its
// own invites a message to someone who left the team two years ago; the commit
// count and the last-seen date let the reader judge that for themselves.
func FormatOwners(result OwnershipResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s 를 아는 사람\n\n", result.Path)
	if result.LibraryID != "" {
		fmt.Fprintf(&out, "%s · %s\n\n", result.LibraryID, result.Ref)
	}
	if len(result.Declared) > 0 {
		out.WriteString("## 선언된 소유자 (CODEOWNERS)\n\n")
		// Last match wins, so the effective rule is stated as such and the rules
		// it overrode are kept visible — that is how a reader checks the answer.
		effective := result.Declared[len(result.Declared)-1]
		fmt.Fprintf(&out, "- **%s** — 규칙 `%s`", strings.Join(effective.Owners, ", "), effective.Pattern)
		if effective.Section != "" {
			fmt.Fprintf(&out, " · 섹션 %s", effective.Section)
		}
		fmt.Fprintf(&out, " · %s\n", effective.Source)
		for _, item := range result.Declared[:len(result.Declared)-1] {
			fmt.Fprintf(&out, "  - (덮임) %s — `%s`\n", strings.Join(item.Owners, ", "), item.Pattern)
		}
		out.WriteString("\n")
	}
	if len(result.Owners) == 0 {
		if len(result.Declared) == 0 {
			out.WriteString("순위를 매길 커밋 이력이 없습니다.\n")
		}
	} else {
		out.WriteString("## 최근 작업자\n\n")
		fmt.Fprintf(&out, "최근 커밋 %d건을 기준으로, 반감기 %d일의 최신성 가중치를 적용했습니다.\n\n",
			result.Examined, int(ownershipHalfLifeDays))
		for index, owner := range result.Owners {
			fmt.Fprintf(&out, "%d. **%s**", index+1, owner.Author)
			if owner.Email != "" {
				fmt.Fprintf(&out, " <%s>", owner.Email)
			}
			fmt.Fprintf(&out, " — 커밋 %d건, 점수 %.2f\n", owner.Commits, owner.Score)
			if !owner.LastSeen.IsZero() {
				fmt.Fprintf(&out, "   최근 %s", owner.LastSeen.Format("2006-01-02"))
				if !owner.FirstSeen.IsZero() && owner.FirstSeen != owner.LastSeen {
					fmt.Fprintf(&out, " · 최초 %s", owner.FirstSeen.Format("2006-01-02"))
				}
				out.WriteString("\n")
			}
		}
	}
	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(&out, "\n_%s_\n", diagnostic)
	}
	return strings.TrimSpace(out.String()) + "\n"
}

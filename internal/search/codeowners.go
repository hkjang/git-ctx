package search

import (
	"context"
	"path"
	"strings"
)

// A repository that ships a CODEOWNERS file has already answered "who owns
// this", and it answered it deliberately. Ranking commit authors is a good
// proxy when nobody has said, but it is only a proxy: it names whoever has been
// busy, not whoever is accountable, and it needs a live call to the source
// server to produce anything at all. The declaration is already indexed here,
// so it costs one query and keeps working while the source is unreachable.

// OwnerDeclaration is one CODEOWNERS rule that applies to a path.
type OwnerDeclaration struct {
	// Pattern is the rule as written, so the reader can see why it matched.
	Pattern string
	// Owners are the handles or addresses the rule names.
	Owners []string
	// Source is the CODEOWNERS file the rule came from.
	Source string
	// Section is the GitLab section heading the rule sits under, when there is
	// one. An "[Optional]" section is advisory, and saying so matters.
	Section string
}

// codeownersPaths are the locations the platforms read, in the order they are
// searched. GitLab and Bitbucket use the first three; the fourth is GitHub's,
// which turns up in mirrored repositories.
var codeownersPaths = []string{"CODEOWNERS", ".gitlab/CODEOWNERS", "docs/CODEOWNERS", ".github/CODEOWNERS"}

// declaredOwners returns the CODEOWNERS rules that apply to filePath, most
// specific last — the same "last match wins" rule GitLab and GitHub apply.
func (s *Service) declaredOwners(ctx context.Context, principals []string, repositoryID, ref, filePath string) ([]OwnerDeclaration, string, error) {
	if repositoryID == "" || filePath == "" {
		return nil, "", nil
	}
	placeholders := make([]string, 0, len(codeownersPaths))
	args := []any{repositoryID, ref}
	for _, candidate := range codeownersPaths {
		placeholders = append(placeholders, "?")
		args = append(args, candidate)
	}
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT file_path,content FROM document_chunks
WHERE repository_id=? AND ref_name=? AND file_path IN (`+strings.Join(placeholders, ",")+`)
ORDER BY file_path,line_start`), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	contents := map[string][]string{}
	for rows.Next() {
		var source, content string
		if err = rows.Scan(&source, &content); err != nil {
			return nil, "", err
		}
		// A large CODEOWNERS file is stored as several chunks, and the rules are
		// line-based, so the pieces are read back in order.
		contents[source] = append(contents[source], content)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	for _, candidate := range codeownersPaths {
		parts, ok := contents[candidate]
		if !ok {
			continue
		}
		matches := matchCodeowners(strings.Join(parts, "\n"), filePath, candidate)
		return matches, candidate, nil
	}
	return nil, "", nil
}

// matchCodeowners parses a CODEOWNERS file and returns every rule matching
// filePath, in file order. The caller reads the last one as the effective rule
// and may show the others as the trail that led there.
func matchCodeowners(content, filePath, source string) []OwnerDeclaration {
	var matches []OwnerDeclaration
	section := ""
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// GitLab groups rules under [Section] or ^[Optional Section] headings.
		if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "^[") {
			heading := strings.TrimPrefix(line, "^")
			if end := strings.Index(heading, "]"); end > 0 {
				section = strings.TrimSpace(heading[1:end])
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pattern, owners := fields[0], fields[1:]
		kept := owners[:0]
		for _, owner := range owners {
			if strings.HasPrefix(owner, "#") {
				break
			}
			kept = append(kept, owner)
		}
		if len(kept) == 0 || !codeownersMatch(pattern, filePath) {
			continue
		}
		matches = append(matches, OwnerDeclaration{Pattern: pattern, Owners: append([]string(nil), kept...), Source: source, Section: section})
	}
	return matches
}

// codeownersMatch reports whether a CODEOWNERS pattern covers a path. The
// syntax is gitignore's, narrowed to what CODEOWNERS files actually use:
// a leading slash anchors to the repository root, a trailing slash matches a
// directory and everything under it, * does not cross a directory boundary and
// ** does, and a bare name matches at any depth.
func codeownersMatch(pattern, filePath string) bool {
	filePath = strings.TrimPrefix(strings.TrimSpace(filePath), "/")
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || filePath == "" {
		return false
	}
	if pattern == "*" || pattern == "**" || pattern == "/" {
		return true
	}
	directoryOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if directoryOnly {
		pattern += "/**"
	}
	if anchored || strings.Contains(pattern, "/") {
		if globMatch(pattern, filePath) {
			return true
		}
		// A directory pattern also owns everything inside it, whether or not the
		// author wrote the trailing slash.
		return globMatch(pattern+"/**", filePath)
	}
	// An unanchored name applies at every depth.
	segments := strings.Split(filePath, "/")
	for index := range segments {
		tail := strings.Join(segments[index:], "/")
		if globMatch(pattern, tail) || globMatch(pattern+"/**", tail) {
			return true
		}
	}
	return false
}

// globMatch matches one pattern against one path, where ** spans directory
// separators and * does not.
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		return err == nil && ok
	}
	head, tail, _ := strings.Cut(pattern, "**")
	head = strings.TrimSuffix(head, "/")
	tail = strings.TrimPrefix(tail, "/")
	if head != "" {
		prefix, rest, found := cutPrefixSegments(head, name)
		if !prefix {
			return false
		}
		name = rest
		_ = found
	}
	if tail == "" {
		return true
	}
	segments := strings.Split(name, "/")
	for index := range segments {
		if globMatch(tail, strings.Join(segments[index:], "/")) {
			return true
		}
	}
	return false
}

// cutPrefixSegments matches the leading segments of name against pattern and
// returns what is left of name.
func cutPrefixSegments(pattern, name string) (bool, string, bool) {
	patternSegments := strings.Split(pattern, "/")
	nameSegments := strings.Split(name, "/")
	if len(nameSegments) < len(patternSegments) {
		return false, "", false
	}
	for index, segment := range patternSegments {
		ok, err := path.Match(segment, nameSegments[index])
		if err != nil || !ok {
			return false, "", false
		}
	}
	return true, strings.Join(nameSegments[len(patternSegments):], "/"), true
}

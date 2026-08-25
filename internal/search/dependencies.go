package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"git-ctx/internal/calltrace"
)

// DependencyUser is one repository that declares a package.
type DependencyUser struct {
	LibraryID    string
	SourceType   string
	Ref          string
	Ecosystem    string
	Name         string
	Version      string
	Scope        string
	ManifestPath string
}

// DependencyVersion groups the repositories that agree on a version. Version
// spread is the number an upgrade is planned around: five repositories on one
// version is one change, five versions is five.
type DependencyVersion struct {
	Version      string
	Repositories []string
}

// DependencyUsage answers "who depends on this, at which version".
type DependencyUsage struct {
	Query        string
	Ecosystem    string
	Users        []DependencyUser
	Versions     []DependencyVersion
	Repositories int
	Diagnostics  []string
}

// dependencyScanLimit bounds one inventory query. A very common package in a
// large catalogue would otherwise return every repository twice over.
const dependencyScanLimit = 2000

// FindDependencyUsage reports which accessible repositories declare a package
// and at which versions.
//
// An import graph cannot answer this: an import line names a package but never
// its version, and a transitive dependency has no import line at all. During an
// advisory the question is exactly "who is on the affected version", so the
// answer is grouped by version rather than listed flat.
func (s *Service) FindDependencyUsage(ctx context.Context, principals []string, name, ecosystem, sourceType string, limit int) (DependencyUsage, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DependencyUsage{}, errors.New("name is required")
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	result := DependencyUsage{Query: name, Ecosystem: ecosystem, Users: []DependencyUser{}, Versions: []DependencyVersion{}}
	if len(principals) == 0 {
		result.Diagnostics = append(result.Diagnostics, "acl: no source principal is mapped to this account, so no repository can be authorized.")
		return result, nil
	}
	join, predicate, args := repositoryACL(principals)
	// The ACL join already binds the alias p to repository_permissions, so the
	// inventory table takes its own.
	statement := `SELECT r.library_id,r.source_type,pkg.ref_name,pkg.ecosystem,pkg.name,pkg.version,pkg.scope,pkg.manifest_path
FROM repository_packages pkg JOIN repositories r ON r.id=pkg.repository_id ` + join + `
WHERE r.enabled=1 AND ` + predicate + ` AND (pkg.name_lower=LOWER(?) OR pkg.name_lower LIKE LOWER(?))`
	args = append(args, name, "%"+name+"%")
	if ecosystem != "" {
		statement += ` AND pkg.ecosystem=?`
		args = append(args, ecosystem)
	}
	if sourceType != "" {
		statement += ` AND r.source_type=?`
		args = append(args, sourceType)
	}
	// An exact package name first: searching "log4j" must not bury
	// log4j-core under a dozen incidental substring matches.
	statement += ` ORDER BY CASE WHEN pkg.name_lower=LOWER(?) THEN 0 ELSE 1 END,pkg.name,r.library_id LIMIT ?`
	args = append(args, name, min(limit*4, dependencyScanLimit))

	span := calltrace.Start(ctx, "dependency-inventory", ecosystem)
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		span.Fail(err)
		return DependencyUsage{}, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	repositories := map[string]bool{}
	versions := map[string]map[string]bool{}
	scanned := 0
	for rows.Next() {
		var item DependencyUser
		if err = rows.Scan(&item.LibraryID, &item.SourceType, &item.Ref, &item.Ecosystem, &item.Name, &item.Version, &item.Scope, &item.ManifestPath); err != nil {
			span.Fail(err)
			return DependencyUsage{}, err
		}
		scanned++
		key := item.LibraryID + "\x00" + item.Ref + "\x00" + item.Name + "\x00" + item.ManifestPath
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(result.Users) < limit {
			result.Users = append(result.Users, item)
		}
		repositories[item.LibraryID] = true
		declared := item.Version
		if strings.TrimSpace(declared) == "" {
			declared = "(선언 없음)"
		}
		if versions[declared] == nil {
			versions[declared] = map[string]bool{}
		}
		versions[declared][item.LibraryID] = true
	}
	if err = rows.Err(); err != nil {
		span.Fail(err)
		return DependencyUsage{}, err
	}
	result.Repositories = len(repositories)
	for version, users := range versions {
		list := make([]string, 0, len(users))
		for library := range users {
			list = append(list, library)
		}
		sort.Strings(list)
		result.Versions = append(result.Versions, DependencyVersion{Version: version, Repositories: list})
	}
	// Most-used version first: that is the one an upgrade converges on.
	sort.SliceStable(result.Versions, func(i, j int) bool {
		if len(result.Versions[i].Repositories) != len(result.Versions[j].Repositories) {
			return len(result.Versions[i].Repositories) > len(result.Versions[j].Repositories)
		}
		return result.Versions[i].Version > result.Versions[j].Version
	})
	span.End(statusFor(len(result.Users)), scanned, len(result.Users), fmt.Sprintf("%d repositories, %d distinct versions", result.Repositories, len(result.Versions)))

	if len(result.Users) == 0 {
		var inventoried int
		_ = s.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_packages`).Scan(&inventoried)
		if inventoried == 0 {
			// Distinguishing "nobody uses it" from "nothing is inventoried yet" is
			// the difference between a safe conclusion and a false one.
			result.Diagnostics = append(result.Diagnostics,
				"inventory: 아직 어떤 저장소에서도 의존성 매니페스트를 색인하지 않았습니다. 재색인 후 다시 확인하세요. 지금 결과는 '사용처 없음'의 근거가 되지 못합니다.")
		} else {
			result.Diagnostics = append(result.Diagnostics,
				fmt.Sprintf("inventory: 색인된 의존성 %d건 중 일치하는 항목이 없습니다. 생태계 표기(예: maven 은 group:artifact)를 확인하세요.", inventoried))
		}
		return result, nil
	}
	if scanned >= dependencyScanLimit {
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("inventory: 상위 %d건만 확인했습니다. 이름을 더 정확히 지정하면 전체를 볼 수 있습니다.", dependencyScanLimit))
	}
	if len(result.Versions) > 1 {
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("versions: %d개 버전이 공존합니다. 업그레이드는 버전별로 나뉘어 진행해야 합니다.", len(result.Versions)))
	}
	return result, nil
}

// FormatDependencyUsage renders the inventory for an agent: versions first,
// because the decision an upgrade or an advisory needs is which version groups
// exist, then the repositories that hold each one.
func FormatDependencyUsage(result DependencyUsage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Dependency Usage\n\nPackage: `%s`", result.Query)
	if result.Ecosystem != "" {
		fmt.Fprintf(&b, " · ecosystem: %s", result.Ecosystem)
	}
	fmt.Fprintf(&b, "\nRepositories: %d · declared versions: %d\n", result.Repositories, len(result.Versions))
	if len(result.Versions) > 0 {
		b.WriteString("\n### Versions\n")
		for _, version := range result.Versions {
			fmt.Fprintf(&b, "\n- **%s** — %d repositor%s: %s\n", version.Version, len(version.Repositories),
				plural(len(version.Repositories)), strings.Join(version.Repositories, ", "))
		}
	}
	if len(result.Users) > 0 {
		b.WriteString("\n### Declarations\n")
		for _, user := range result.Users {
			version := user.Version
			if version == "" {
				version = "unspecified"
			}
			fmt.Fprintf(&b, "\n- %s · `%s` %s (%s, %s)\n  Source: `%s://%s@%s/%s`\n",
				user.LibraryID, user.Name, version, user.Scope, user.Ecosystem,
				user.SourceType, strings.TrimPrefix(user.LibraryID, "/"), user.Ref, user.ManifestPath)
		}
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

func plural(count int) string {
	if count == 1 {
		return "y"
	}
	return "ies"
}

package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"git-ctx/internal/calltrace"
	"git-ctx/internal/manifest"
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
	// Status answers an advisory directly: "affected", "safe" or "unknown".
	// It is empty when the caller asked no advisory question.
	Status string
}

// DependencyUsage answers "who depends on this, at which version".
type DependencyUsage struct {
	Query     string
	Ecosystem string
	Users     []DependencyUser
	Versions  []DependencyVersion
	// FixedIn is the release an advisory names as the fix, when the caller
	// supplied one.
	FixedIn string
	// Affected, Safe and Undecided count repositories, not declarations. During
	// an advisory the number that matters is how many repositories must change,
	// and how many could not be judged from what their manifest declares.
	Affected     []string
	Safe         []string
	Undecided    []string
	Repositories int
	Diagnostics  []string
}

// likePattern renders a user term as a contains-pattern with the SQL wildcards
// escaped.
//
// Without this, searching "%" reports that every repository declares the
// package and "_" quietly widens any name by one character. For a catalogue
// search that only over-matches what the caller may already read it is a
// nuisance; for an advisory it produces a confident, wrong answer about which
// repositories are affected.
func likePattern(term string) string {
	replacer := strings.NewReplacer(`\`, `\`, "%", `\%`, "_", `\_`)
	return "%" + replacer.Replace(term) + "%"
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
func (s *Service) FindDependencyUsage(ctx context.Context, principals []string, name, ecosystem, sourceType, fixedIn string, limit int) (DependencyUsage, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DependencyUsage{}, errors.New("name is required")
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	fixedIn = strings.TrimSpace(fixedIn)
	result := DependencyUsage{Query: name, Ecosystem: ecosystem, FixedIn: fixedIn,
		Users: []DependencyUser{}, Versions: []DependencyVersion{}}
	if len(principals) == 0 {
		result.Diagnostics = append(result.Diagnostics, "acl: no source principal is mapped to this account, so no repository can be authorized.")
		return result, nil
	}
	join, predicate, args := repositoryACL(principals)
	// The ACL join already binds the alias p to repository_permissions, so the
	// inventory table takes its own.
	statement := `SELECT r.library_id,r.source_type,pkg.ref_name,pkg.ecosystem,pkg.name,pkg.version,pkg.scope,pkg.manifest_path
FROM repository_packages pkg JOIN repositories r ON r.id=pkg.repository_id ` + join + `
WHERE r.enabled=1 AND ` + predicate + ` AND (pkg.name_lower=LOWER(?) OR pkg.name_lower LIKE LOWER(?) ESCAPE '\')`
	args = append(args, name, likePattern(name))
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
	if fixedIn != "" {
		result = classifyAgainstFix(result, fixedIn)
	}
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
	if fixedIn != "" {
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("advisory: %s 기준 영향 %d개 · 안전 %d개 · 판정 불가 %d개 저장소.",
				fixedIn, len(result.Affected), len(result.Safe), len(result.Undecided)))
		if len(result.Undecided) > 0 {
			// The undecided set is the honest part of the answer: a caret range or a
			// floating version cannot be judged from the manifest alone, and calling
			// it safe is the one mistake an advisory cannot afford.
			result.Diagnostics = append(result.Diagnostics,
				"advisory: 판정 불가 저장소는 선언이 범위·부동 버전이라 매니페스트만으로 결정할 수 없습니다. 락파일이나 빌드 산출물로 확인하세요. 안전으로 간주하지 마세요.")
		}
	}
	return result, nil
}

// classifyAgainstFix labels each version group against the release that fixes
// an advisory, and counts repositories rather than declarations because that is
// the unit of work an upgrade is planned in.
func classifyAgainstFix(result DependencyUsage, fixedIn string) DependencyUsage {
	affected, safe, undecided := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index, group := range result.Versions {
		below, decided := manifest.Below(group.Version, fixedIn)
		switch {
		case !decided:
			result.Versions[index].Status = "unknown"
			for _, library := range group.Repositories {
				undecided[library] = true
			}
		case below:
			result.Versions[index].Status = "affected"
			for _, library := range group.Repositories {
				affected[library] = true
			}
		default:
			result.Versions[index].Status = "safe"
			for _, library := range group.Repositories {
				safe[library] = true
			}
		}
	}
	// A repository that declares the package twice — an affected version in one
	// manifest and a fixed one in another — is affected. The stricter label wins
	// so a mixed repository is never reported as done.
	for library := range affected {
		delete(safe, library)
		delete(undecided, library)
	}
	for library := range undecided {
		delete(safe, library)
	}
	result.Affected, result.Safe, result.Undecided = sortedLibraries(affected), sortedLibraries(safe), sortedLibraries(undecided)
	// Affected first: an advisory is read from the top.
	sort.SliceStable(result.Versions, func(i, j int) bool {
		return statusRank(result.Versions[i].Status) < statusRank(result.Versions[j].Status)
	})
	return result
}

func statusRank(status string) int {
	switch status {
	case "affected":
		return 0
	case "unknown":
		return 1
	default:
		return 2
	}
}

func sortedLibraries(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
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
	if result.FixedIn != "" {
		fmt.Fprintf(&b, "Advisory: fixed in %s · affected %d · safe %d · undecided %d repositories\n",
			result.FixedIn, len(result.Affected), len(result.Safe), len(result.Undecided))
	}
	if len(result.Versions) > 0 {
		b.WriteString("\n### Versions\n")
		for _, version := range result.Versions {
			label := ""
			switch version.Status {
			case "affected":
				label = " — AFFECTED"
			case "safe":
				label = " — safe"
			case "unknown":
				label = " — undecidable from the manifest"
			}
			fmt.Fprintf(&b, "\n- **%s**%s — %d repositor%s: %s\n", version.Version, label, len(version.Repositories),
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

// DependencySummaryEntry is one package as the whole catalogue uses it.
type DependencySummaryEntry struct {
	Ecosystem string
	Name      string
	// Repositories is how many accessible repositories declare it, which is the
	// unit a standardisation decision is made in.
	Repositories int
	// Versions lists the distinct declared versions, most used first. More than
	// one is drift: the same library at different versions across the estate.
	Versions []DependencyVersion
}

// DependencyInventory is the catalogue-wide view.
type DependencyInventory struct {
	Ecosystems []DependencyEcosystemCount
	Packages   []DependencySummaryEntry
	// Covered and Total say how much of the catalogue the inventory speaks for.
	// Without them a short list reads as "we use very little", when it may only
	// mean most repositories have not been indexed since the feature existed.
	Covered      int
	Total        int
	Diagnostics  []string
	DriftPackage int
}

// DependencyEcosystemCount is the package-manager breakdown.
type DependencyEcosystemCount struct {
	Ecosystem string
	Packages  int
}

// inventorySummaryScan bounds the aggregate. A catalogue of thousands of
// repositories has tens of thousands of declarations, and the screen shows the
// head of the distribution.
const inventorySummaryScan = 20000

// DependencyInventorySummary answers the question the per-package tool cannot:
// what does this organisation actually depend on, and where has it drifted?
//
// find-dependency-usage needs a package name, so it can only confirm a suspicion.
// Standardisation starts from the opposite end — the list itself — and the number
// that makes it actionable is how many distinct versions of one library the
// estate carries.
func (s *Service) DependencyInventorySummary(ctx context.Context, principals []string, ecosystem string, limit int) (DependencyInventory, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	result := DependencyInventory{Ecosystems: []DependencyEcosystemCount{}, Packages: []DependencySummaryEntry{}}
	if len(principals) == 0 {
		result.Diagnostics = append(result.Diagnostics, "acl: no source principal is mapped to this account, so no repository can be authorized.")
		return result, nil
	}
	join, predicate, aclArgs := repositoryACL(principals)
	// Coverage is counted over the same ACL scope as the inventory itself, so the
	// ratio describes what this caller can see rather than the whole catalogue.
	coverage := `SELECT COUNT(DISTINCT r.id),
COUNT(DISTINCT CASE WHEN EXISTS (SELECT 1 FROM repository_packages pk WHERE pk.repository_id=r.id) THEN r.id END)
FROM repositories r ` + join + `
WHERE r.enabled=1 AND ` + predicate
	if err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(coverage), aclArgs...).Scan(&result.Total, &result.Covered); err != nil {
		return DependencyInventory{}, err
	}

	args := append([]any(nil), aclArgs...)
	statement := `SELECT pkg.ecosystem,pkg.name,pkg.version,r.library_id
FROM repository_packages pkg JOIN repositories r ON r.id=pkg.repository_id ` + join + `
WHERE r.enabled=1 AND ` + predicate
	if ecosystem != "" {
		statement += ` AND pkg.ecosystem=?`
		args = append(args, ecosystem)
	}
	statement += ` LIMIT ?`
	args = append(args, inventorySummaryScan)
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(statement), args...)
	if err != nil {
		return DependencyInventory{}, err
	}
	defer rows.Close()
	type aggregate struct {
		ecosystem, name string
		repositories    map[string]bool
		versions        map[string]map[string]bool
	}
	packages := map[string]*aggregate{}
	ecosystems := map[string]map[string]bool{}
	scanned := 0
	for rows.Next() {
		var eco, name, version, library string
		if err = rows.Scan(&eco, &name, &version, &library); err != nil {
			return DependencyInventory{}, err
		}
		scanned++
		key := eco + "\x00" + name
		entry := packages[key]
		if entry == nil {
			entry = &aggregate{ecosystem: eco, name: name, repositories: map[string]bool{}, versions: map[string]map[string]bool{}}
			packages[key] = entry
		}
		entry.repositories[library] = true
		declared := version
		if strings.TrimSpace(declared) == "" {
			declared = "(선언 없음)"
		}
		if entry.versions[declared] == nil {
			entry.versions[declared] = map[string]bool{}
		}
		entry.versions[declared][library] = true
		if ecosystems[eco] == nil {
			ecosystems[eco] = map[string]bool{}
		}
		ecosystems[eco][name] = true
	}
	if err = rows.Err(); err != nil {
		return DependencyInventory{}, err
	}
	for eco, names := range ecosystems {
		result.Ecosystems = append(result.Ecosystems, DependencyEcosystemCount{Ecosystem: eco, Packages: len(names)})
	}
	sort.SliceStable(result.Ecosystems, func(i, j int) bool {
		return result.Ecosystems[i].Packages > result.Ecosystems[j].Packages
	})
	for _, entry := range packages {
		summary := DependencySummaryEntry{Ecosystem: entry.ecosystem, Name: entry.name, Repositories: len(entry.repositories)}
		for version, libraries := range entry.versions {
			summary.Versions = append(summary.Versions, DependencyVersion{Version: version, Repositories: sortedLibraries(libraries)})
		}
		sort.SliceStable(summary.Versions, func(i, j int) bool {
			if len(summary.Versions[i].Repositories) != len(summary.Versions[j].Repositories) {
				return len(summary.Versions[i].Repositories) > len(summary.Versions[j].Repositories)
			}
			return summary.Versions[i].Version > summary.Versions[j].Version
		})
		if len(summary.Versions) > 1 {
			result.DriftPackage++
		}
		result.Packages = append(result.Packages, summary)
	}
	// Drift first, then reach: a library used everywhere at one version needs no
	// decision, while one used in five repositories at four versions does.
	sort.SliceStable(result.Packages, func(i, j int) bool {
		left, right := result.Packages[i], result.Packages[j]
		if (len(left.Versions) > 1) != (len(right.Versions) > 1) {
			return len(left.Versions) > 1
		}
		if len(left.Versions) != len(right.Versions) {
			return len(left.Versions) > len(right.Versions)
		}
		if left.Repositories != right.Repositories {
			return left.Repositories > right.Repositories
		}
		return left.Name < right.Name
	})
	if len(result.Packages) > limit {
		result.Packages = result.Packages[:limit]
	}
	if result.Total > 0 && result.Covered < result.Total {
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("coverage: 접근 가능한 저장소 %d개 중 %d개만 의존성 매니페스트가 색인되어 있습니다. 이 목록은 나머지 저장소를 대변하지 않습니다.",
				result.Total, result.Covered))
	}
	if result.Covered == 0 {
		result.Diagnostics = append(result.Diagnostics,
			"coverage: 아직 어떤 저장소도 매니페스트가 색인되지 않았습니다. 재색인 후 다시 확인하세요.")
	}
	if scanned >= inventorySummaryScan {
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("inventory: 선언 %d건까지만 집계했습니다. 생태계 필터로 범위를 좁히세요.", inventorySummaryScan))
	}
	if result.DriftPackage > 0 {
		result.Diagnostics = append(result.Diagnostics,
			fmt.Sprintf("drift: %d개 패키지가 저장소마다 다른 버전으로 선언되어 있습니다. 표준화 대상입니다.", result.DriftPackage))
	}
	return result, nil
}

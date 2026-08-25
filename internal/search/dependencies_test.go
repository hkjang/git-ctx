package search

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/store"
)

func inventoryFixture(t *testing.T, name string) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:"+name+"?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for _, repository := range []string{"api", "worker", "console", "secret"} {
		exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES(?,'core',?,?,'gitlab',?,?,'main')`,
			repository, repository, repository, repository, "/core/"+repository)
		principal := "alice"
		if repository == "secret" {
			principal = "bob"
		}
		exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,?,'read')`, repository, principal)
	}
	add := func(repository, ecosystem, name, version, scope, path string) {
		exec(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES(?,'main',?,?,?,?,?,?,'abc')`,
			repository, ecosystem, name, strings.ToLower(name), version, scope, path)
	}
	add("api", "maven", "org.apache.logging.log4j:log4j-core", "2.14.1", "direct", "pom.xml")
	add("worker", "maven", "org.apache.logging.log4j:log4j-core", "2.17.1", "direct", "pom.xml")
	add("console", "maven", "org.apache.logging.log4j:log4j-core", "2.17.1", "direct", "service/pom.xml")
	add("api", "maven", "org.apache.logging.log4j:log4j-api", "2.14.1", "direct", "pom.xml")
	add("secret", "maven", "org.apache.logging.log4j:log4j-core", "2.14.1", "direct", "pom.xml")
	add("api", "npm", "lodash", "4.17.21", "direct", "web/package.json")
	return db
}

// The question an advisory asks is not "who imports it" but "who is on the
// affected version", so the answer has to group by version and must not leak a
// repository the caller cannot read.
func TestFindDependencyUsageGroupsVersionsAndRespectsACL(t *testing.T) {
	db := inventoryFixture(t, "dependency-usage")
	service := New(db)
	result, err := service.FindDependencyUsage(context.Background(), []string{"alice"}, "org.apache.logging.log4j:log4j-core", "", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories != 3 {
		t.Fatalf("the unreadable repository must be excluded: %#v", result)
	}
	if len(result.Versions) != 2 {
		t.Fatalf("versions=%#v", result.Versions)
	}
	// The version most repositories agree on leads, because that is what an
	// upgrade converges on.
	if result.Versions[0].Version != "2.17.1" || len(result.Versions[0].Repositories) != 2 {
		t.Fatalf("version order=%#v", result.Versions)
	}
	for _, version := range result.Versions {
		for _, library := range version.Repositories {
			if library == "/core/secret" {
				t.Fatalf("ACL leak in the version grouping: %#v", version)
			}
		}
	}
	if !strings.Contains(strings.Join(result.Diagnostics, " "), "2개 버전") {
		t.Fatalf("a split version must be stated: %v", result.Diagnostics)
	}

	// An exact name must not be buried by the sibling artifact.
	if result.Users[0].Name != "org.apache.logging.log4j:log4j-core" {
		t.Fatalf("exact match must rank first: %#v", result.Users[0])
	}

	rendered := FormatDependencyUsage(result)
	for _, expected := range []string{"## Dependency Usage", "2.17.1", "/core/api", "pom.xml"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered output missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "/core/secret") {
		t.Fatalf("rendered output leaked a repository:\n%s", rendered)
	}
}

// "No result" must never be read as "nobody uses it" when nothing has been
// inventoried yet — that is the difference between a safe conclusion and a
// false all-clear during an advisory.
func TestFindDependencyUsageSeparatesEmptyFromUnindexed(t *testing.T) {
	ctx := context.Background()
	empty, err := store.Open(ctx, "sqlite", "file:dependency-empty?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.DB.Close()
	result, err := New(empty).FindDependencyUsage(ctx, []string{"alice"}, "left-pad", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Diagnostics, " "), "색인하지 않았습니다") {
		t.Fatalf("an empty inventory must say so: %v", result.Diagnostics)
	}

	populated := inventoryFixture(t, "dependency-known")
	known, err := New(populated).FindDependencyUsage(ctx, []string{"alice"}, "left-pad", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(known.Diagnostics, " ")
	if strings.Contains(joined, "색인하지 않았습니다") || !strings.Contains(joined, "일치하는 항목이 없습니다") {
		t.Fatalf("a populated inventory must report a genuine miss: %v", known.Diagnostics)
	}

	// Filters narrow the same query without changing its shape.
	filtered, err := New(populated).FindDependencyUsage(ctx, []string{"alice"}, "lodash", "npm", "", "", 10)
	if err != nil || len(filtered.Users) != 1 || filtered.Users[0].Ecosystem != "npm" {
		t.Fatalf("filtered=%#v err=%v", filtered, err)
	}
	if none, err := New(populated).FindDependencyUsage(ctx, []string{"alice"}, "lodash", "maven", "", "", 10); err != nil || len(none.Users) != 0 {
		t.Fatalf("the ecosystem filter must apply: %#v err=%v", none, err)
	}
	if denied, err := New(populated).FindDependencyUsage(ctx, nil, "lodash", "", "", "", 10); err != nil || len(denied.Users) != 0 {
		t.Fatalf("a caller with no principal must see nothing: %#v err=%v", denied, err)
	}
}

// An advisory names a fixed version, and the only dangerous answer is a false
// "safe". A declaration that cannot be read must land in undecided, and a
// repository that declares both an affected and a fixed version stays affected.
func TestFindDependencyUsageJudgesAgainstTheFix(t *testing.T) {
	db := inventoryFixture(t, "dependency-advisory")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	// console declares the package a second time, unreadably; api declares an
	// affected version in one manifest and a fixed one in another.
	exec(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES('console','main','maven','org.apache.logging.log4j:log4j-core','org.apache.logging.log4j:log4j-core','${log4j.version}','direct','other/pom.xml','abc')`)
	exec(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES('api','main','maven','org.apache.logging.log4j:log4j-core','org.apache.logging.log4j:log4j-core','2.17.1','direct','extra/pom.xml','abc')`)

	result, err := New(db).FindDependencyUsage(context.Background(), []string{"alice"},
		"org.apache.logging.log4j:log4j-core", "", "", "2.17.1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Affected, ",") != "/core/api" {
		t.Fatalf("affected=%v (api declares 2.14.1 somewhere and must stay affected)", result.Affected)
	}
	if strings.Join(result.Undecided, ",") != "/core/console" {
		t.Fatalf("undecided=%v", result.Undecided)
	}
	if strings.Join(result.Safe, ",") != "/core/worker" {
		t.Fatalf("safe=%v", result.Safe)
	}
	// The affected group is read first.
	if result.Versions[0].Status != "affected" {
		t.Fatalf("version order=%#v", result.Versions)
	}
	joined := strings.Join(result.Diagnostics, " ")
	if !strings.Contains(joined, "영향 1개") || !strings.Contains(joined, "안전으로 간주하지 마세요") {
		t.Fatalf("diagnostics=%v", result.Diagnostics)
	}
	rendered := FormatDependencyUsage(result)
	if !strings.Contains(rendered, "AFFECTED") || !strings.Contains(rendered, "undecidable from the manifest") {
		t.Fatalf("rendered:\n%s", rendered)
	}

	// Without a fix version the tool keeps its plain inventory shape.
	plain, err := New(db).FindDependencyUsage(context.Background(), []string{"alice"},
		"org.apache.logging.log4j:log4j-core", "", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Affected)+len(plain.Safe)+len(plain.Undecided) != 0 || plain.Versions[0].Status != "" {
		t.Fatalf("no advisory was asked for: %#v", plain)
	}
}

// Standardisation starts from the list, not from a name you already suspect.
// The ordering has to put drift first, and the coverage ratio has to be stated:
// a short list from a mostly unindexed catalogue is not "we use very little".
func TestDependencyInventorySummaryRanksDriftAndStatesCoverage(t *testing.T) {
	db := inventoryFixture(t, "dependency-summary")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	// A package used widely but consistently must rank below the drifting one.
	for _, repository := range []string{"api", "worker", "console"} {
		exec(`INSERT INTO repository_packages(repository_id,ref_name,ecosystem,name,name_lower,version,scope,manifest_path,commit_id) VALUES(?,'main','npm','react','react','18.2.0','direct','web/package.json','abc')`, repository)
	}
	result, err := New(db).DependencyInventorySummary(context.Background(), []string{"alice"}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) == 0 {
		t.Fatal("summary is empty")
	}
	first := result.Packages[0]
	if first.Name != "org.apache.logging.log4j:log4j-core" || len(first.Versions) != 2 {
		t.Fatalf("drift must lead: %#v", result.Packages[:min(3, len(result.Packages))])
	}
	var react DependencySummaryEntry
	for _, entry := range result.Packages {
		if entry.Name == "react" {
			react = entry
		}
	}
	if react.Repositories != 3 || len(react.Versions) != 1 {
		t.Fatalf("react=%#v", react)
	}
	if result.DriftPackage < 1 {
		t.Fatalf("drift count=%d", result.DriftPackage)
	}
	// The fixture gives alice three readable repositories, all of which have
	// packages; the fourth is another principal's and must not be counted.
	if result.Total != 3 || result.Covered != 3 {
		t.Fatalf("coverage=%d/%d", result.Covered, result.Total)
	}
	joined := strings.Join(result.Diagnostics, " ")
	if !strings.Contains(joined, "표준화 대상") {
		t.Fatalf("diagnostics=%v", result.Diagnostics)
	}

	// A repository with no manifest indexed lowers coverage, and that must be
	// said rather than left for the reader to assume.
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('fresh','core','fresh','fresh','gitlab','9','/core/fresh','main')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('fresh','alice','read')`)
	partial, err := New(db).DependencyInventorySummary(context.Background(), []string{"alice"}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Total != 4 || partial.Covered != 3 {
		t.Fatalf("coverage=%d/%d", partial.Covered, partial.Total)
	}
	if !strings.Contains(strings.Join(partial.Diagnostics, " "), "대변하지 않습니다") {
		t.Fatalf("partial coverage must be stated: %v", partial.Diagnostics)
	}

	// The ecosystem filter narrows both the packages and the breakdown.
	npm, err := New(db).DependencyInventorySummary(context.Background(), []string{"alice"}, "npm", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range npm.Packages {
		if entry.Ecosystem != "npm" {
			t.Fatalf("filter leaked %#v", entry)
		}
	}
	if len(npm.Ecosystems) != 1 || npm.Ecosystems[0].Ecosystem != "npm" {
		t.Fatalf("ecosystems=%#v", npm.Ecosystems)
	}
}

// A package name is an identifier, not a pattern. A wildcard in it must match
// literally: during an advisory, "%" reporting the whole catalogue as affected
// would be a confident wrong answer.
func TestFindDependencyUsageTreatsWildcardsLiterally(t *testing.T) {
	db := inventoryFixture(t, "dependency-wildcard")
	service := New(db)
	for _, term := range []string{"%", "_", "%log4j%"} {
		result, err := service.FindDependencyUsage(context.Background(), []string{"alice"}, term, "", "", "", 50)
		if err != nil {
			t.Fatalf("%q: %v", term, err)
		}
		if len(result.Users) != 0 {
			t.Fatalf("%q matched %d declarations; wildcards must be literal", term, len(result.Users))
		}
	}
	// The escaping must not break ordinary names, including the ones with dots
	// and colons that ecosystems use.
	for _, term := range []string{"log4j-core", "org.apache.logging.log4j:log4j-core", "lodash"} {
		result, err := service.FindDependencyUsage(context.Background(), []string{"alice"}, term, "", "", "", 50)
		if err != nil || len(result.Users) == 0 {
			t.Fatalf("%q found %d declarations err=%v", term, len(result.Users), err)
		}
	}
}

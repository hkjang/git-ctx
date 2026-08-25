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
	result, err := service.FindDependencyUsage(context.Background(), []string{"alice"}, "org.apache.logging.log4j:log4j-core", "", "", 50)
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
	result, err := New(empty).FindDependencyUsage(ctx, []string{"alice"}, "left-pad", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Diagnostics, " "), "색인하지 않았습니다") {
		t.Fatalf("an empty inventory must say so: %v", result.Diagnostics)
	}

	populated := inventoryFixture(t, "dependency-known")
	known, err := New(populated).FindDependencyUsage(ctx, []string{"alice"}, "left-pad", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(known.Diagnostics, " ")
	if strings.Contains(joined, "색인하지 않았습니다") || !strings.Contains(joined, "일치하는 항목이 없습니다") {
		t.Fatalf("a populated inventory must report a genuine miss: %v", known.Diagnostics)
	}

	// Filters narrow the same query without changing its shape.
	filtered, err := New(populated).FindDependencyUsage(ctx, []string{"alice"}, "lodash", "npm", "", 10)
	if err != nil || len(filtered.Users) != 1 || filtered.Users[0].Ecosystem != "npm" {
		t.Fatalf("filtered=%#v err=%v", filtered, err)
	}
	if none, err := New(populated).FindDependencyUsage(ctx, []string{"alice"}, "lodash", "maven", "", 10); err != nil || len(none.Users) != 0 {
		t.Fatalf("the ecosystem filter must apply: %#v err=%v", none, err)
	}
	if denied, err := New(populated).FindDependencyUsage(ctx, nil, "lodash", "", "", 10); err != nil || len(denied.Users) != 0 {
		t.Fatalf("a caller with no principal must see nothing: %#v err=%v", denied, err)
	}
}

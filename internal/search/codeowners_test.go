package search

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/store"
)

// The pattern rules are gitignore's, and getting them subtly wrong hands a
// change to the wrong team, so each shape is pinned directly.
func TestCodeownersPatternsFollowTheGitignoreRules(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*", "any/where.go", true},
		{"/service.go", "service.go", true},
		{"/service.go", "internal/service.go", false},
		{"service.go", "internal/service.go", true},
		{"internal/", "internal/app/auth.go", true},
		{"internal/", "web/app.js", false},
		{"internal/app", "internal/app/auth.go", true},
		{"*.go", "service.go", true},
		{"*.go", "internal/service.go", true},
		{"docs/*.md", "docs/guide.md", true},
		{"docs/*.md", "docs/ko/guide.md", false},
		{"docs/**/*.md", "docs/ko/guide.md", true},
		{"/web/**", "web/app.js", true},
		{"/web/**", "internal/web/app.js", false},
	}
	for _, item := range cases {
		if got := codeownersMatch(item.pattern, item.path); got != item.want {
			t.Errorf("pattern %q against %q = %v, want %v", item.pattern, item.path, got, item.want)
		}
	}
}

// Sections, comments and inline comments all appear in real files, and the last
// matching rule is the one that decides.
func TestCodeownersParsingKeepsTheDecidingRuleLast(t *testing.T) {
	content := `# 소유자 선언
* @platform-team

[Backend]
/internal/ @backend-team @alice

^[Optional]
/internal/search/ @search-guild  # 검색 관련 문의
`
	matches := matchCodeowners(content, "internal/search/service.go", "CODEOWNERS")
	if len(matches) != 3 {
		t.Fatalf("every covering rule must be kept: %#v", matches)
	}
	last := matches[len(matches)-1]
	if strings.Join(last.Owners, ",") != "@search-guild" || last.Section != "Optional" {
		t.Fatalf("the deciding rule is wrong: %#v", last)
	}
	if strings.Join(matches[1].Owners, ",") != "@backend-team,@alice" || matches[1].Section != "Backend" {
		t.Fatalf("section rule=%#v", matches[1])
	}
	// A path no specific rule covers still falls to the catch-all.
	fallback := matchCodeowners(content, "web/app.js", "CODEOWNERS")
	if len(fallback) != 1 || fallback[0].Owners[0] != "@platform-team" {
		t.Fatalf("catch-all=%#v", fallback)
	}
}

// A repository that declares its owners has answered the question already, and
// that answer must survive a source server this platform cannot reach — which
// is exactly when the commit-ranking path returns nothing at all.
func TestDeclaredOwnersAnswerWithoutTheSourceServer(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:codeowners-fallback?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:1','core','api','api','gitlab','1','/core/api','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:1','alice','read')`)
	exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('gitlab:1','main','internal/search/service.go','service.go',100,1,'abc')`)
	exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('co1','gitlab:1','main','abc','CODEOWNERS',1,4,'CODEOWNERS','configuration','* @platform-team
/internal/search/ @search-guild','h1')`)

	service := New(db)
	// No source loader is installed, so the commit history cannot be read.
	result, err := service.FindOwners(ctx, []string{"alice"}, "/core/api", "", "internal/search/service.go", "main", 5)
	if err != nil {
		t.Fatalf("a declared owner must answer without the source: %v", err)
	}
	if len(result.Declared) != 2 {
		t.Fatalf("declared=%#v", result.Declared)
	}
	if got := result.Declared[len(result.Declared)-1].Owners[0]; got != "@search-guild" {
		t.Fatalf("the most specific rule must decide: %q", got)
	}
	rendered := FormatOwners(result)
	if !strings.Contains(rendered, "@search-guild") || !strings.Contains(rendered, "CODEOWNERS") {
		t.Fatalf("the declaration is not in the answer:\n%s", rendered)
	}
	// The reader has to know the ranking is missing and why.
	if !strings.Contains(strings.Join(result.Diagnostics, " "), "커밋 이력을 읽지 못해") {
		t.Fatalf("diagnostics=%v", result.Diagnostics)
	}

	// Without a declaration the call still fails as before rather than
	// pretending the question was answered.
	if _, err = service.FindOwners(ctx, []string{"alice"}, "/core/api", "", "internal/search/nothing.go", "main", 5); err == nil {
		t.Fatal("an unknown path with no declaration must not report success")
	}
}

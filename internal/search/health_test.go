package search

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/store"
)

func TestRepositoryHealthCountsWhatTheIndexSupports(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:health?mode=memory&cache=shared&_foreign_keys=on")
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
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','order','Order','gitlab','1','/core/order','main')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	sym := func(id, name string) {
		exec(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,line_start,line_end,content_hash) VALUES(?,'r','main','abc','internal/a.go',?,?,'func','go',1,5,'h')`, id, name, name)
	}
	sym("s1", "Covered")
	sym("s2", "Uncovered")
	sym("s3", "Unreferenced")
	// A test references Covered; production code references Uncovered.
	exec(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES('d1','r','main','abc','internal/a_test.go','T','Covered','call',1)`)
	exec(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES('d2','r','main','abc','internal/b.go','B','Uncovered','call',1)`)
	exec(`INSERT INTO repository_files(repository_id,ref_name,commit_id,path,base_name,size_bytes) VALUES('r','main','abc','README.md','readme.md',10)`)

	service := New(db)
	result, err := service.RepositoryHealthReport(ctx, []string{"alice"}, "/core/order", "")
	if err != nil {
		t.Fatalf("RepositoryHealthReport: %v", err)
	}

	byName := map[string]HealthMeasure{}
	for _, measure := range result.Measures {
		byName[measure.Name] = measure
	}
	coverage := byName["tests-referencing-symbols"]
	if coverage.Total != 3 || coverage.Value != 1 {
		t.Errorf("coverage = %d/%d, want 1/3 -- only the test-referenced symbol counts", coverage.Value, coverage.Total)
	}
	referenced := byName["symbols-referenced-somewhere"]
	if referenced.Value != 2 {
		t.Errorf("referenced = %d, want 2 (Unreferenced has no dependency row)", referenced.Value)
	}
	if byName["convention-files"].Value != 1 {
		t.Errorf("conventions = %d, want the README counted", byName["convention-files"].Value)
	}

	flags := map[string]HealthFlag{}
	for _, flag := range result.Flags {
		flags[flag.Name] = flag
	}
	if _, present := flags["untested-symbols"]; !present {
		t.Errorf("untested symbols were not flagged: %#v", result.Flags)
	}
	if _, present := flags["no-conventions"]; present {
		t.Errorf("a repository with a README was flagged as having none")
	}
	unreferenced, present := flags["unreferenced-symbols"]
	if !present || len(unreferenced.Examples) == 0 || unreferenced.Examples[0] != "Unreferenced" {
		t.Errorf("unreferenced flag = %#v", unreferenced)
	}
	// The flag must not read as a delete list.
	if !strings.Contains(unreferenced.Summary, "삭제 목록이 아닙니다") {
		t.Errorf("summary overstates the finding: %q", unreferenced.Summary)
	}

	rendered := FormatHealth(result)
	for _, want := range []string{"보지 않은 것", "복잡도", "순환 의존", "점수 대신"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the report does not state its limits (%q):\n%s", want, rendered)
		}
	}
}

func TestRepositoryHealthFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:health-acl?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','order','Order','gitlab','1','/core/order','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','bob','read')`)
	if _, err := New(db).RepositoryHealthReport(ctx, []string{"alice"}, "/core/order", ""); err == nil {
		t.Error("a repository outside the caller's ACL was reported on")
	}
}

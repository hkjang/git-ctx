package search

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/store"
)

// The two signals mean different things and must not be presented as one. A test
// the graph shows calling the symbol will fail if the change is wrong; a test
// that merely sits beside it might not touch the code at all.
func TestFindTestsSeparatesReferencesFromProximity(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:find-tests?mode=memory&cache=shared&_foreign_keys=on")
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
	// A test that calls the symbol, and production code that also calls it.
	exec(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES('d1','r','main','abc','internal/order/service_test.go','TestPlaceOrder','OrderService','call',10)`)
	exec(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES('d2','r','main','abc','internal/api/handler.go','Handle','OrderService','call',20)`)
	// A test file beside the symbol that the graph knows nothing about.
	exec(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,line_start,line_end,content_hash) VALUES('s1','r','main','abc','internal/order/service.go','OrderService','order.OrderService','class','go',1,50,'h')`)
	exec(`INSERT INTO repository_files(repository_id,ref_name,commit_id,path,base_name,size_bytes) VALUES('r','main','abc','internal/order/helper_test.go','helper_test.go',10)`)
	exec(`INSERT INTO repository_files(repository_id,ref_name,commit_id,path,base_name,size_bytes) VALUES('r','main','abc','internal/order/service.go','service.go',10)`)

	service := New(db)
	result, err := service.FindTests(ctx, []string{"alice"}, "OrderService", "", "", 20)
	if err != nil {
		t.Fatalf("FindTests: %v", err)
	}

	byPath := map[string]TestOrigin{}
	for _, test := range result.Tests {
		byPath[test.FilePath] = test.Origin
	}
	if byPath["internal/order/service_test.go"] != TestReferences {
		t.Errorf("the referencing test was not found or mislabelled: %#v", result.Tests)
	}
	// Production code that calls the symbol is not a test and must not appear.
	if _, present := byPath["internal/api/handler.go"]; present {
		t.Errorf("production code was reported as a test: %#v", result.Tests)
	}
	if byPath["internal/order/helper_test.go"] != TestNearby {
		t.Errorf("the neighbouring test was not found or mislabelled: %#v", result.Tests)
	}
	// The non-test file beside it must not be included either.
	if _, present := byPath["internal/order/service.go"]; present {
		t.Errorf("a non-test file was reported: %#v", result.Tests)
	}

	rendered := FormatTests(result)
	if !strings.Contains(rendered, "이 심볼을 참조함") || !strings.Contains(rendered, "이름·위치로 추정") {
		t.Errorf("the two signals were not distinguished:\n%s", rendered)
	}
}

// With no referencing test, the answer has to say the list is guesswork rather
// than let it read as coverage.
func TestFindTestsWarnsWhenOnlyProximityMatched(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:find-tests-weak?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','order','Order','gitlab','1','/core/order','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO code_symbols(id,repository_id,ref_name,commit_id,file_path,name,qualified_name,symbol_kind,language,line_start,line_end,content_hash) VALUES('s1','r','main','abc','internal/order/service.go','OrderService','order.OrderService','class','go',1,50,'h')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,commit_id,path,base_name,size_bytes) VALUES('r','main','abc','internal/order/service_test.go','service_test.go',10)`)

	service := New(db)
	result, err := service.FindTests(ctx, []string{"alice"}, "OrderService", "", "", 20)
	if err != nil {
		t.Fatalf("FindTests: %v", err)
	}
	if len(result.Tests) == 0 {
		t.Fatal("the neighbouring test was not found")
	}
	joined := strings.Join(result.Diagnostics, " ")
	if !strings.Contains(joined, "직접 참조하는 테스트는 없습니다") {
		t.Errorf("a proximity-only result must say so: %v", result.Diagnostics)
	}
}

func TestFindTestsFailsClosedWithoutPermissions(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:find-tests-acl?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	service := New(db)
	if _, err := service.FindTests(ctx, nil, "OrderService", "", "", 20); err == nil {
		t.Error("a caller with no principals was served")
	}
	if _, err := service.FindTests(ctx, []string{"alice"}, "  ", "", "", 20); err == nil {
		t.Error("an empty symbol was accepted")
	}
}

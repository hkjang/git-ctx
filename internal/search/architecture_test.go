package search

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/store"
)

// Classification has to be specific enough to be evidence. Matching "sql"
// anywhere would label every repository that mentions it.
func TestClassifyRecognisesTechnologiesWithoutOverreaching(t *testing.T) {
	cases := []struct {
		target string
		want   []string
	}{
		{"net/http", []string{"http-server"}},
		{"database/sql", []string{"database"}},
		{"github.com/jackc/pgx/v5", []string{"database"}},
		{"github.com/segmentio/kafka-go", []string{"messaging"}},
		{"google.golang.org/grpc", []string{"rpc"}},
		{"org.springframework.web.bind.annotation", []string{"http-server"}},
		{"sqlalchemy.orm", []string{"database"}},
		{"@nestjs/common", []string{"http-server"}},
	}
	for _, c := range cases {
		got := classify(c.target)
		for _, want := range c.want {
			found := false
			for _, item := range got {
				if item == want {
					found = true
				}
			}
			if !found {
				t.Errorf("classify(%q) = %v, want it to include %q", c.target, got, want)
			}
		}
	}
	// Ordinary imports are not evidence of anything and must stay unclassified.
	for _, target := range []string{"fmt", "strings", "encoding/json", "java.util.List", "lodash", "os"} {
		if got := classify(target); len(got) != 0 {
			t.Errorf("classify(%q) = %v, want nothing", target, got)
		}
	}
}

func TestArchitectureOverviewClassifiesAndLinksWithinTheACL(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:architecture?mode=memory&cache=shared&_foreign_keys=on")
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
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('api','core','orderapi','Order API','gitlab','1','/core/orderapi','main')`)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('lib','core','ordercore','Order Core','gitlab','2','/core/ordercore','main')`)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('secret','core','hidden','Hidden','gitlab','3','/core/hidden','main')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('api','alice','read')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('lib','alice','read')`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('secret','bob','read')`)

	add := func(repo, target, kind string) {
		exec(`INSERT INTO code_dependencies(id,repository_id,ref_name,commit_id,file_path,from_symbol,target,dependency_kind,line_number) VALUES(?,?,'main','abc','a.go','F',?,?,1)`,
			repo+target+kind, repo, target, kind)
	}
	add("api", "net/http", "import")
	add("api", "database/sql", "import")
	add("api", "github.com/segmentio/kafka-go", "import")
	add("api", "fmt", "import")
	add("api", "github.com/acme/ordercore/model", "import")
	add("lib", "database/sql", "import")
	add("secret", "net/http", "import")

	service := New(db)
	result, err := service.ArchitectureOverview(ctx, []string{"alice"}, "", 50)
	if err != nil {
		t.Fatalf("ArchitectureOverview: %v", err)
	}

	byLibrary := map[string]Component{}
	for _, component := range result.Components {
		byLibrary[component.LibraryID] = component
	}
	if _, present := byLibrary["/core/hidden"]; present {
		t.Errorf("a repository outside the ACL appeared: %#v", result.Components)
	}
	if len(result.Components) != 2 {
		t.Fatalf("components = %#v, want the two readable ones", result.Components)
	}

	names := map[string]bool{}
	for _, capability := range byLibrary["/core/orderapi"].Capabilities {
		names[capability.Name] = true
		if len(capability.Evidence) == 0 {
			t.Errorf("%s was claimed without evidence", capability.Name)
		}
	}
	for _, want := range []string{"http-server", "database", "messaging"} {
		if !names[want] {
			t.Errorf("capabilities = %v, want %q", names, want)
		}
	}

	// The import naming the other repository becomes an edge; the ACL-hidden one
	// cannot appear on either end.
	if len(result.Edges) != 1 {
		t.Fatalf("edges = %#v, want one", result.Edges)
	}
	if result.Edges[0].From != "/core/orderapi" || result.Edges[0].To != "/core/ordercore" {
		t.Errorf("edge = %#v", result.Edges[0])
	}

	rendered := FormatArchitecture(result)
	// The rendering must say what it did not look at, or it reads as complete.
	if !strings.Contains(rendered, "엔드포인트 경로") || !strings.Contains(rendered, "내장된 SQL") {
		t.Errorf("the rendering does not state its limits:\n%s", rendered)
	}
}

func TestArchitectureOverviewFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:architecture-acl?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if _, err := New(db).ArchitectureOverview(ctx, nil, "", 50); err == nil {
		t.Error("a caller with no principals was served")
	}
}

package codeintel

import "testing"

func TestExtractGoDependencies(t *testing.T) {
	items := ExtractDependencies("service.go", `package service
import "context"
func Run(ctx context.Context) { client.Fetch(); validate(ctx) }`)
	assertDependency(t, items, "", "context", "import")
	assertDependency(t, items, "Run", "client.Fetch", "call")
	assertDependency(t, items, "Run", "validate", "call")
}

func TestExtractOtherDependencies(t *testing.T) {
	cases := []struct {
		path, content, target, kind string
	}{
		{"A.java", "import com.company.Platform;", "com.company.Platform", "import"},
		{"app.ts", `import api from "./api"`, "./api", "import"},
		{"app.py", "from platform.client import API", "platform.client", "import"},
		{"schema.sql", "CREATE VIEW v AS SELECT * FROM core.users;", "core.users", "data"},
	}
	for _, test := range cases {
		assertDependency(t, ExtractDependencies(test.path, test.content), "", test.target, test.kind)
	}
}

func assertDependency(t *testing.T, items []Dependency, from, target, kind string) {
	t.Helper()
	for _, item := range items {
		if item.FromSymbol == from && item.Target == target && item.Kind == kind {
			return
		}
	}
	t.Fatalf("dependency %q -> %q (%s) not found in %#v", from, target, kind, items)
}

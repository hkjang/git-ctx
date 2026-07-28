package codeintel

import "testing"

func TestGoASTSymbols(t *testing.T) {
	symbols := Extract("service.go", `package demo
// Service handles work.
type Service struct{}
type Reader interface { Read() error }
func NewService() *Service { return &Service{} }
func (s *Service) Run() error { return nil }`)
	if len(symbols) != 4 || symbols[0].Name != "Service" || symbols[1].Kind != "interface" || symbols[2].Kind != "function" || symbols[3].QualifiedName != "Service.Run" {
		t.Fatalf("symbols=%#v", symbols)
	}
	if symbols[0].Documentation != "Service handles work." {
		t.Fatalf("documentation=%q", symbols[0].Documentation)
	}
}

func TestLanguageStructureExtractors(t *testing.T) {
	cases := []struct {
		path, content, name, kind string
	}{
		{"Demo.java", "public class Demo {\n public void run() {}\n}", "Demo", "class"},
		{"app.ts", "export async function load() {\n return 1\n}", "load", "function"},
		{"worker.py", "class Worker:\n    def run(self):\n        pass\n", "Worker", "class"},
		{"schema.sql", "CREATE TABLE users (\n id bigint\n);", "users", "table"},
	}
	for _, item := range cases {
		symbols := Extract(item.path, item.content)
		found := false
		for _, symbol := range symbols {
			if symbol.Name == item.name && symbol.Kind == item.kind {
				found = true
			}
		}
		if !found {
			t.Errorf("%s symbols=%#v", item.path, symbols)
		}
	}
}

package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The other direction. TestEveryDocumentedEndpointIsRouted proves the document
// promises nothing the server lacks; nothing proved the reverse, and
// GET /api/v1/admin/freshness had been serving an operator screen for releases
// without appearing in the document an integrator reads. An endpoint that only
// the console knows about is an endpoint nobody else can use, and the omission
// is silent in both directions.
func TestEveryRoutedAPIEndpointIsDocumented(t *testing.T) {
	root := filepath.Join("..", "..")
	spec, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	parameter := regexp.MustCompile(`\{[^}]+\}`)
	documented := map[string]bool{}
	var path string
	for _, line := range strings.Split(string(spec), "\n") {
		switch {
		case strings.HasPrefix(line, "  /"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case path != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     "):
			method := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			switch method {
			case "GET", "POST", "PUT", "DELETE", "PATCH":
				documented[method+" "+parameter.ReplaceAllString(path, "{}")] = true
			}
		}
	}
	if len(documented) < 40 {
		t.Fatalf("the spec walk found only %d operations, so it is not reading the document", len(documented))
	}

	route := regexp.MustCompile(`\.(?:HandleFunc|Handle)\("([A-Z]+) (/api/[^"]+)"`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	routed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		source, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range route.FindAllStringSubmatch(string(source), -1) {
			routed++
			key := match[1] + " " + parameter.ReplaceAllString(match[2], "{}")
			if !documented[key] {
				missing = append(missing, key+"  ("+entry.Name()+")")
			}
		}
	}
	if routed < 40 {
		t.Fatalf("the route walk found only %d endpoints, so it is not reading the handlers", routed)
	}
	sort.Strings(missing)
	for _, item := range missing {
		t.Errorf("routed but absent from docs/openapi.yaml: %s", item)
	}
}

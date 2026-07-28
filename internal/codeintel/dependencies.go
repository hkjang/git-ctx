package codeintel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// Dependency is a source-level relationship extracted without resolving or
// executing repository content. Target remains the source spelling so it can
// be matched against indexed symbols and modules later.
type Dependency struct {
	FromSymbol string
	Target     string
	Kind       string
	Line       int
}

var (
	javaImportRE = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([A-Za-z_][A-Za-z0-9_.*]+)\s*;`)
	tsImportRE   = regexp.MustCompile(`^\s*import(?:[\s\S]*?\sfrom\s+)?["']([^"']+)["']`)
	tsRequireRE  = regexp.MustCompile(`require\(\s*["']([^"']+)["']\s*\)`)
	pythonImpRE  = regexp.MustCompile(`^\s*import\s+([A-Za-z_][A-Za-z0-9_.]*)`)
	pythonFromRE = regexp.MustCompile(`^\s*from\s+([A-Za-z_.][A-Za-z0-9_.]*)\s+import\s+`)
	sqlRefRE     = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|REFERENCES|UPDATE|INTO)\s+([A-Za-z_][A-Za-z0-9_.$"]*)`)
)

func ExtractDependencies(path, content string) []Dependency {
	switch Language(path) {
	case "go":
		return extractGoDependencies(content)
	case "java":
		return extractLineDependencies(content, javaImportRE, "import")
	case "typescript":
		out := extractLineDependencies(content, tsImportRE, "import")
		return append(out, extractLineDependencies(content, tsRequireRE, "import")...)
	case "python":
		out := extractLineDependencies(content, pythonImpRE, "import")
		return append(out, extractLineDependencies(content, pythonFromRE, "import")...)
	case "sql":
		return extractLineDependencies(content, sqlRefRE, "data")
	default:
		return nil
	}
}

func extractGoDependencies(content string) []Dependency {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", content, 0)
	if err != nil {
		return nil
	}
	out := make([]Dependency, 0, len(file.Imports))
	for _, item := range file.Imports {
		target, err := strconv.Unquote(item.Path.Value)
		if err == nil {
			out = append(out, Dependency{Target: target, Kind: "import", Line: fset.Position(item.Pos()).Line})
		}
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		from := function.Name.Name
		if function.Recv != nil && len(function.Recv.List) > 0 {
			from = receiverName(function.Recv.List[0].Type) + "." + from
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			target := expressionName(call.Fun)
			if target != "" && target != from {
				out = append(out, Dependency{FromSymbol: from, Target: target, Kind: "call", Line: fset.Position(call.Pos()).Line})
			}
			return true
		})
	}
	return uniqueDependencies(out)
}

func expressionName(expression ast.Expr) string {
	switch node := expression.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		left := expressionName(node.X)
		if left == "" {
			return node.Sel.Name
		}
		return left + "." + node.Sel.Name
	case *ast.IndexExpr:
		return expressionName(node.X)
	case *ast.IndexListExpr:
		return expressionName(node.X)
	default:
		return ""
	}
}

func extractLineDependencies(content string, pattern *regexp.Regexp, kind string) []Dependency {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []Dependency
	for index, line := range lines {
		for _, match := range pattern.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 {
				out = append(out, Dependency{Target: strings.Trim(match[1], `"`), Kind: kind, Line: index + 1})
			}
		}
	}
	return uniqueDependencies(out)
}

func uniqueDependencies(input []Dependency) []Dependency {
	seen := map[string]bool{}
	out := make([]Dependency, 0, len(input))
	for _, item := range input {
		key := item.FromSymbol + "\x00" + item.Target + "\x00" + item.Kind + "\x00" + strconv.Itoa(item.Line)
		if item.Target == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

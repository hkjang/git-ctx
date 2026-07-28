package codeintel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

type Symbol struct {
	Name, QualifiedName, Kind, Language, Signature, Documentation string
	LineStart, LineEnd                                            int
}

var (
	javaTypeRE = regexp.MustCompile(`^\s*(?:public|protected|private|abstract|final|static|\s)*\s*(class|interface|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	javaFuncRE = regexp.MustCompile(`^\s*(?:public|protected|private|static|final|abstract|synchronized|native|\s)+[\w<>\[\],.?]+\s+([A-Za-z_][A-Za-z0-9_]*)\s*\([^;]*\)\s*(?:throws[^{]+)?\{?`)
	tsTypeRE   = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(class|interface|enum|type)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	tsFuncRE   = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)
	tsArrowRE  = regexp.MustCompile(`^\s*(?:export\s+)?const\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s*)?\([^)]*\)\s*=>`)
	pythonRE   = regexp.MustCompile(`^(\s*)(?:async\s+)?(def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	sqlRE      = regexp.MustCompile(`(?i)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(TABLE|VIEW|FUNCTION|PROCEDURE|TRIGGER)\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_.$"]*)`)
)

func Language(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".java":
		return "java"
	case ".ts", ".tsx", ".js", ".jsx":
		return "typescript"
	case ".py":
		return "python"
	case ".sql", ".ddl":
		return "sql"
	default:
		return ""
	}
}

func Extract(path, content string) []Symbol {
	switch Language(path) {
	case "go":
		return extractGo(content)
	case "java":
		return extractBraceLanguage(content, "java", javaTypeRE, javaFuncRE)
	case "typescript":
		return extractTypeScript(content)
	case "python":
		return extractPython(content)
	case "sql":
		return extractSQL(content)
	default:
		return nil
	}
}

func extractGo(content string) []Symbol {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", content, parser.ParseComments)
	if err != nil {
		return nil
	}
	var out []Symbol
	for _, declaration := range file.Decls {
		switch node := declaration.(type) {
		case *ast.FuncDecl:
			kind, qualified := "function", node.Name.Name
			if node.Recv != nil && len(node.Recv.List) > 0 {
				receiver := receiverName(node.Recv.List[0].Type)
				kind, qualified = "method", receiver+"."+node.Name.Name
			}
			out = append(out, Symbol{Name: node.Name.Name, QualifiedName: qualified, Kind: kind, Language: "go",
				Signature: signatureLine(content, fset.Position(node.Pos()).Line), Documentation: commentText(node.Doc),
				LineStart: fset.Position(node.Pos()).Line, LineEnd: fset.Position(node.End()).Line})
		case *ast.GenDecl:
			for _, specification := range node.Specs {
				typeSpec, ok := specification.(*ast.TypeSpec)
				if !ok {
					continue
				}
				kind := "type"
				switch typeSpec.Type.(type) {
				case *ast.StructType:
					kind = "class"
				case *ast.InterfaceType:
					kind = "interface"
				}
				out = append(out, Symbol{Name: typeSpec.Name.Name, QualifiedName: typeSpec.Name.Name, Kind: kind, Language: "go",
					Signature: signatureLine(content, fset.Position(typeSpec.Pos()).Line), Documentation: commentText(node.Doc),
					LineStart: fset.Position(node.Pos()).Line, LineEnd: fset.Position(node.End()).Line})
			}
		}
	}
	return out
}

func receiverName(expression ast.Expr) string {
	switch node := expression.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.StarExpr:
		return receiverName(node.X)
	case *ast.IndexExpr:
		return receiverName(node.X)
	case *ast.IndexListExpr:
		return receiverName(node.X)
	default:
		return "receiver"
	}
}

func extractBraceLanguage(content, language string, typeRE, functionRE *regexp.Regexp) []Symbol {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []Symbol
	for index, line := range lines {
		if match := typeRE.FindStringSubmatch(line); len(match) > 2 {
			out = append(out, Symbol{Name: match[2], QualifiedName: match[2], Kind: normalizeKind(match[1]), Language: language,
				Signature: strings.TrimSpace(line), LineStart: index + 1, LineEnd: braceEnd(lines, index)})
			continue
		}
		if match := functionRE.FindStringSubmatch(line); len(match) > 1 && !controlKeyword(match[1]) {
			out = append(out, Symbol{Name: match[1], QualifiedName: match[1], Kind: "method", Language: language,
				Signature: strings.TrimSpace(line), LineStart: index + 1, LineEnd: braceEnd(lines, index)})
		}
	}
	return out
}

func extractTypeScript(content string) []Symbol {
	out := extractBraceLanguage(content, "typescript", tsTypeRE, tsFuncRE)
	for index := range out {
		if strings.Contains(out[index].Signature, "function ") {
			out[index].Kind = "function"
		}
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if match := tsArrowRE.FindStringSubmatch(line); len(match) > 1 {
			out = append(out, Symbol{Name: match[1], QualifiedName: match[1], Kind: "function", Language: "typescript",
				Signature: strings.TrimSpace(line), LineStart: index + 1, LineEnd: braceEnd(lines, index)})
		}
	}
	return out
}

func extractPython(content string) []Symbol {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []Symbol
	for index, line := range lines {
		match := pythonRE.FindStringSubmatch(line)
		if len(match) < 4 {
			continue
		}
		indent := len(match[1])
		end := len(lines)
		for next := index + 1; next < len(lines); next++ {
			trimmed := strings.TrimSpace(lines[next])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if leadingSpaces(lines[next]) <= indent {
				end = next
				break
			}
		}
		kind := "function"
		if match[2] == "class" {
			kind = "class"
		} else if indent > 0 {
			kind = "method"
		}
		out = append(out, Symbol{Name: match[3], QualifiedName: match[3], Kind: kind, Language: "python",
			Signature: strings.TrimSpace(line), LineStart: index + 1, LineEnd: end})
	}
	return out
}

func extractSQL(content string) []Symbol {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []Symbol
	for index, line := range lines {
		match := sqlRE.FindStringSubmatch(line)
		if len(match) < 3 {
			continue
		}
		end := index + 1
		for end < len(lines) && !strings.Contains(lines[end-1], ";") {
			end++
		}
		out = append(out, Symbol{Name: strings.Trim(match[2], `"`), QualifiedName: strings.Trim(match[2], `"`),
			Kind: strings.ToLower(match[1]), Language: "sql", Signature: strings.TrimSpace(line), LineStart: index + 1, LineEnd: end})
	}
	return out
}

func braceEnd(lines []string, start int) int {
	depth, opened := 0, false
	for index := start; index < len(lines); index++ {
		depth += strings.Count(lines[index], "{")
		if depth > 0 {
			opened = true
		}
		depth -= strings.Count(lines[index], "}")
		if opened && depth <= 0 {
			return index + 1
		}
	}
	return min(len(lines), start+80)
}

func signatureLine(content string, line int) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}
func commentText(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	return strings.TrimSpace(group.Text())
}
func normalizeKind(kind string) string {
	if strings.EqualFold(kind, "record") || strings.EqualFold(kind, "class") {
		return "class"
	}
	return strings.ToLower(kind)
}
func controlKeyword(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch":
		return true
	default:
		return false
	}
}
func leadingSpaces(line string) int { return len(line) - len(strings.TrimLeft(line, " \t")) }

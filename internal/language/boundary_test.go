package language

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Parser dependencies are intentionally centralized here. This test makes the
// boundary executable: a future language feature cannot quietly add another
// AST/subprocess backend under runner, unit, or harness.
func TestParserDependenciesStayInsideLanguage(t *testing.T) {
	root := filepath.Clean("..")
	languageRoot := filepath.Join(root, "language") + string(filepath.Separator)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if strings.HasPrefix(filepath.Clean(path), languageRoot) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		violations, err := parserDependencies(content)
		if err != nil {
			return err
		}
		for _, dependency := range violations {
			t.Errorf("%s uses parser dependency %s outside internal/language", path, dependency)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Check dependency syntax rather than arbitrary strings: an observation "node"
// field does not invoke Node.js, and a documentation example is not an import.
func parserDependencies(content []byte) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "", content, 0)
	if err != nil {
		return nil, err
	}
	forbidden := []string{"go/ast", "go/parser", "go/token", "go/types", "golang.org/x/tools/go/packages", "github.com/odvcencio/gotreesitter", "github.com/tree-sitter/"}
	var result []string
	execName := ""
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		for _, dependency := range forbidden {
			if path == dependency || strings.HasPrefix(path, dependency+"/") || strings.HasSuffix(dependency, "/") && strings.HasPrefix(path, dependency) {
				result = append(result, path)
			}
		}
		if path == "os/exec" {
			execName = "exec"
			if spec.Name != nil {
				execName = spec.Name.Name
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || execName == "" {
			return true
		}
		name := ""
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			if owner, ok := selector.X.(*ast.Ident); ok && owner.Name == execName {
				name = selector.Sel.Name
			}
		}
		if execName == "." {
			if ident, ok := call.Fun.(*ast.Ident); ok {
				name = ident.Name
			}
		}
		index := 0
		switch name {
		case "Command":
		case "CommandContext":
			index = 1
		default:
			return true
		}
		if len(call.Args) <= index {
			return true
		}
		if literal, ok := call.Args[index].(*ast.BasicLit); ok && literal.Kind == token.STRING {
			program, _ := strconv.Unquote(literal.Value)
			if filepath.Base(program) == "node" || filepath.Base(program) == "python3" {
				result = append(result, program)
			}
		}
		return true
	})
	return result, nil
}

func TestParserDependencyDetection(t *testing.T) {
	for _, tt := range []struct {
		name, source string
		count        int
	}{
		{"JSON field", `package consumer;var value=map[string]any{"node":"python3"}`, 0},
		{"parser import", `package consumer;import "go/parser"`, 1},
		{"aliased subprocess", `package consumer;import process "os/exec";func run(){process.Command("node","parse.js")}`, 1},
		{"context subprocess", `package consumer;import "os/exec";func run(){exec.CommandContext(ctx,"/usr/bin/python3","parse.py")}`, 1},
		{"other program argument", `package consumer;import "os/exec";func run(){exec.Command("echo","node")}`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parserDependencies([]byte(tt.source))
			if err != nil || len(got) != tt.count {
				t.Fatalf("dependencies=%v error=%v", got, err)
			}
		})
	}
}

func TestLanguageSyntaxStaysOutsideConsumers(t *testing.T) {
	root := filepath.Clean("..")
	consumerDirs := []string{"runner", "unit", "harness"}
	forbidden := []string{
		`".go"`, `".py"`, `".ts"`, `".tsx"`, `".js"`, `".jsx"`, `".mjs"`, `".cjs"`,
		`"*.go"`, `"*.py"`, `"*.ts"`, `"*.tsx"`, `"*.js"`, `"*.jsx"`, `"*.mjs"`, `"*.cjs"`,
		`"::"`, `unicode.IsUpper`, `def\s+`, `function\s+`, `(const|let|var)\s+`,
		`"go.mod"`, `GOMODCACHE`, `GOPATH`, `VIRTUAL_ENV`, `".venv"`, `site-packages`,
	}
	for _, dir := range consumerDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, knowledge := range forbidden {
				if strings.Contains(string(content), knowledge) {
					t.Errorf("%s contains source-language knowledge %s outside internal/language", path, knowledge)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Graph parsing and resolution are upstream responsibilities, including inside
// the language adapter. Consumers derive every source view from graph values.
func TestSourceAnalysisIsOwnedByCodeGraph(t *testing.T) {
	forbidden := []string{`"go/ast"`, `"go/parser"`, `"go/types"`, `"golang.org/x/tools/go/packages"`, `"github.com/odvcencio/gotreesitter/grammars"`, `NewParser(`, `NewOutliner(`, `FactProgram`}
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, token := range forbidden {
			if strings.Contains(string(content), token) {
				t.Errorf("%s duplicates source-analysis ownership with %s", path, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Extraction artifacts may be held only by material-production code. All review
// projections must accept graph values, so a new feature cannot reopen a Facts path.
func TestFactsStayInsideGraphProduction(t *testing.T) {
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if filepath.Clean(path) == filepath.Join("..", "language", "analyzer.go") || filepath.Clean(path) == filepath.Join("..", "language", "repository.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), "cg.Facts") || strings.Contains(string(content), "codegraph.Facts") {
			t.Errorf("%s reads extraction artifacts outside graph production", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

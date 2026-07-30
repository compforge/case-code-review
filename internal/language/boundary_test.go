package language

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parser dependencies are intentionally centralized here. This test makes the
// boundary executable: a future language feature cannot quietly add another
// AST/subprocess backend under runner, unit, or harness.
func TestParserDependenciesStayInsideLanguage(t *testing.T) {
	root := filepath.Clean("..")
	languageRoot := filepath.Join(root, "language") + string(filepath.Separator)
	forbidden := []string{
		`"go/ast"`,
		`"go/parser"`,
		`"go/token"`,
		`"go/types"`,
		`"golang.org/x/tools/go/packages"`,
		`"github.com/odvcencio/gotreesitter`,
		`"github.com/tree-sitter/`,
		`"python3"`,
		`"node"`,
	}
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
		for _, dependency := range forbidden {
			if strings.Contains(string(content), dependency) {
				t.Errorf("%s uses parser dependency %s outside internal/language", path, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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

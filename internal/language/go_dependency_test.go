package language

import (
	"context"
	"encoding/json"
	"github.com/qiankunli/case-code-review/internal/sourceview"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/sumdb/dirhash"
)

func TestGoDependencyRequiresLockedUnmodifiedSource(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	sourceDir := filepath.Join(cache, "example.org", "dep@v1.0.0")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "dep.go")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "package dep\nfunc Contract() {}\n")
	hash, err := dirhash.HashDir(sourceDir, "example.org/dep@v1.0.0", dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(repo, "go.mod"), "module example.org/app\ngo 1.22\nrequire example.org/dep v1.0.0\n")
	write(filepath.Join(repo, "go.sum"), "example.org/dep v1.0.0 "+hash+"\n")
	provider := NewGoDependencyReader(testDependencySnapshot(repo), 500, 32<<10)
	provider.moduleCache = cache
	execute := func(args map[string]any) sourceview.DependencySource {
		t.Helper()
		data, err := provider.Execute(t.Context(), args)
		if err != nil {
			t.Fatal(err)
		}
		var result sourceview.DependencySource
		if err = json.Unmarshal([]byte(data), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	discovery := execute(map[string]any{"import_path": "example.org/dep"})
	if discovery.Status != "discovery" || len(discovery.Files) != 1 || discovery.Files[0] != "dep.go" {
		t.Fatalf("discovery=%+v", discovery)
	}
	args := map[string]any{"import_path": "example.org/dep", "file_path": "dep.go", "start_line": float64(2), "end_line": float64(2)}
	result := execute(args)
	if result.Status != "read" || result.Checksum != hash || result.Version != "v1.0.0" || !strings.Contains(result.Content, "2|func Contract") {
		t.Fatalf("source=%+v", result)
	}
	for _, path := range []string{"../go.mod", "/etc/passwd", "dep.txt"} {
		invalid := execute(map[string]any{"import_path": "example.org/dep", "file_path": path})
		if invalid.Status != "unavailable" {
			t.Fatalf("accepted path %q", path)
		}
	}
	undeclared := execute(map[string]any{"import_path": "example.org/undeclared", "file_path": "dep.go"})
	if undeclared.Status != "unavailable" {
		t.Fatal("read undeclared dependency")
	}
	write(source, "package dep\nfunc Modified() {}\n")
	modified := execute(args)
	if modified.Status != "unavailable" || !strings.Contains(modified.Message, "checksum") {
		t.Fatalf("modified=%+v", modified)
	}
}

func TestGoStandardLibrarySourceHasDistinctProvenance(t *testing.T) {
	repo, goRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.org/app\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(goRoot, "src", "net", "url")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "url.go"), []byte("package url\nfunc QueryEscape(s string) string { return s }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := NewGoDependencyReader(testDependencySnapshot(repo), 500, 32<<10)
	provider.goRoot = goRoot
	data, err := provider.Execute(t.Context(), map[string]any{"import_path": "net/url", "file_path": "url.go"})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := sourceview.DecodeDependencySource(data)
	if !ok || result.Module != "stdlib" || result.Version == "" || result.DeclaredGo != "1.22" || result.Ref == "" {
		t.Fatalf("stdlib=%+v", result)
	}
}

func testDependencySnapshot(repo string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, name string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		data, err := os.ReadFile(filepath.Join(repo, name))
		return string(data), err
	}
}

func TestGoDependencyUsesReviewedNestedModule(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	sourceDir := filepath.Join(cache, "example.org", "dep@v1.0.0")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "dep.go"), []byte("package dep\nfunc Contract() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hash, err := dirhash.HashDir(sourceDir, "example.org/dep@v1.0.0", dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":        "module example.org/root\ngo 1.24\nrequire example.org/dep v2.0.0\n",
		"nested/go.mod": "module example.org/nested\ngo 1.22\nrequire example.org/dep v1.0.0\n",
		"nested/go.sum": "example.org/dep v1.0.0 " + hash + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	provider := NewGoDependencyReader(testDependencySnapshot(repo), 500, 32<<10)
	provider.moduleCache = cache
	data, err := provider.Execute(t.Context(), map[string]any{"import_path": "example.org/dep", "file_path": "dep.go", "_reviewed_path": "nested/sub/file.go"})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := sourceview.DecodeDependencySource(data)
	if !ok || result.Version != "v1.0.0" || result.ManifestPath != "nested/go.mod" || result.DeclaredGo != "1.22" {
		t.Fatalf("nested module=%+v", result)
	}
}

package language

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/compforge/repocli/toolkit/go"
)

// +case:id=review_material_admission,desc=`Dependencies and fixtures are not Git ignored`,expect=`Explicit CCR exclusions keep them outside graph material budgets`
func TestRepositoryUsesExplicitMaterialExclusions(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"keep.go", "vendor/bad.go", "node_modules/bad.go", "testdata/bad.go", ".codex/bad.go", ".cache/bad.go", "dist/bad.go", "bad.gen.go"} {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		data := "invalid source"
		if name == "keep.go" {
			data = "package p\nfunc Keep(){}\n"
		}
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := NewAnalyzer(root).Repository()
	if index.Graph == nil || len(index.Gaps) != 0 {
		t.Fatalf("graph failed: %+v", index)
	}
	if len(index.Sources) != 1 || index.Sources["keep.go"] == "" {
		t.Fatalf("review exclusions lost: %v", index.Sources)
	}
}

func TestReviewGraphSourceEnumeratesOverlayDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.go"), []byte("package p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base, err := repocli.OpenGraphSource(ctx, repocli.InputRequest{Repository: root})
	if err != nil {
		t.Fatal(err)
	}
	source, err := newReviewGraphSource(base, map[string]string{"keep.go": "package p\nfunc Keep(){}\n", "new/nested/a.go": "package p\n"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := source.ReadDirectory(ctx, ".")
	if err != nil || !slices.Contains(entries.Directories, "new") || len(entries.Documents) != 1 || entries.Documents[0].Size != int64(len(source.documents["keep.go"])) {
		t.Fatalf("root: %+v %v", entries, err)
	}
	entries, err = source.ReadDirectory(ctx, "new/nested")
	if err != nil || len(entries.Documents) != 1 || entries.Documents[0].Path != "new/nested/a.go" {
		t.Fatalf("new directory: %+v %v", entries, err)
	}
	for name, want := range source.documents {
		if got, err := source.ReadSource(ctx, name); err != nil || got != want {
			t.Fatalf("overlay content %s: %q %v", name, got, err)
		}
	}
}

// +case:id=excluded_directory_unreadable,desc=`An excluded dependency directory cannot be enumerated`,expect=`Repository graph still contains the review source`
func TestRepositoryPrunesUnreadableDependencyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.go"), []byte("package p\nfunc Keep(){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "node_modules")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blocked, 0700); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadDir(blocked); !errors.Is(err, fs.ErrPermission) {
		t.Skip("filesystem does not enforce directory read permissions")
	}
	index := NewAnalyzer(root).Repository()
	if index.Graph == nil || len(index.Gaps) != 0 || len(index.Sources) != 1 || index.Sources["keep.go"] == "" {
		t.Fatalf("excluded directory prevented graph construction: %+v", index)
	}
}

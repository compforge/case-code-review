package sourceview

import (
	"context"
	"errors"
	repocli "github.com/compforge/repocli/toolkit/go"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturedMissingSourceNeverBecomesEmptyOrBaseline(t *testing.T) {
	view := New(t.TempDir(), "", "review", map[string]File{
		"missing.go": {Unavailable: true}, "deleted.go": {Deleted: true}, "new.go": {Content: "package p\nfunc Present() {}\n"},
	}, nil)
	if _, err := view.Read(context.Background(), "missing.go"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("capture failure became absence: %v", err)
	}
	if _, err := view.Read(context.Background(), "deleted.go"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted entry: %v", err)
	}
	if _, err := view.Read(context.Background(), "../outside"); err == nil {
		t.Fatal("accepted outside path")
	}
	out, detail, err := view.Grep(context.Background(), []string{"--no-pager", "grep", "-n", "-F", "-e", "Present", "--"}, "")
	if err == nil || !strings.Contains(out, "Present") || !strings.Contains(detail, "unavailable") {
		t.Fatalf("partial search lost evidence/gap: %q %q %v", out, detail, err)
	}
}

func TestDiffSearchUsesCapturedUnchangedAndChangedFiles(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("a.go", "package p\n// Before\n")
	write("b.go", "package p\n// Stable\n")
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	write("a.go", "package p\n// Captured\n")
	d, err := repocli.Diff(context.Background(), repocli.DiffRequest{Repository: root})
	if err != nil {
		t.Fatal(err)
	}
	view := NewCaptured(d, false)
	write("a.go", "package p\n// Later\n")
	write("b.go", "package p\n// Later\n")
	out, detail, err := view.Grep(context.Background(), []string{"--no-pager", "grep", "-n", "-E", "-e", "Captured|Stable|Later", "--"}, "")
	if err != nil || detail != "" || !strings.Contains(out, "Captured") || !strings.Contains(out, "Stable") || strings.Contains(out, "Later") {
		t.Fatalf("captured search: %q %q %v", out, detail, err)
	}
}

func TestCapturedDirectoryIndexDoesNotOpenCheckout(t *testing.T) {
	root := t.TempDir()
	view := New(root, "", "captured", map[string]File{"nested/a.go": {Content: "package p\n"}, "nested/missing.go": {Unavailable: true}}, nil)
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entries, err := view.ReadDirectory(ctx, ".")
	if err != nil || len(entries.Directories) != 1 || entries.Directories[0] != "nested" {
		t.Fatalf("root: %+v %v", entries, err)
	}
	entries, err = view.ReadDirectory(ctx, "nested")
	if err != nil || len(entries.Documents) != 2 || entries.Documents[1].Size != -1 {
		t.Fatalf("nested: %+v %v", entries, err)
	}
	if data, err := view.ReadSource(ctx, "nested/a.go"); err != nil || data != "package p\n" {
		t.Fatalf("captured bytes: %q %v", data, err)
	}
}

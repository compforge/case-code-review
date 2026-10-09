package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffSnapshotFileBudgetIsCallerControlled(t *testing.T) {
	repo := initRepoWithChange(t)
	for _, name := range []string{"extra-a.go", "extra-b.go"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("package extra\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, repo, "add", "extra-a.go", "extra-b.go")
	runGitTest(t, repo, "commit", "-q", "-m", "add supporting files")
	provider := NewWorkspaceProvider(repo, nil)
	provider.MaxFiles = 2
	if _, err := provider.GetDiff(t.Context()); err == nil || !strings.Contains(err.Error(), "limit=2") {
		t.Fatalf("budget error=%v", err)
	}
	provider.MaxFiles = 3
	changes, err := provider.GetDiff(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path() != "sample.txt" {
		t.Fatalf("changes=%+v", changes)
	}
}

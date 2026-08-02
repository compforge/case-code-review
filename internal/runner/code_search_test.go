package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func TestCodeSearchDefinitionsReadsReviewedRef(t *testing.T) {
	repo := t.TempDir()
	runCodeSearchGit(t, repo, "init", "-q")
	runCodeSearchGit(t, repo, "config", "user.email", "test@example.com")
	runCodeSearchGit(t, repo, "config", "user.name", "Test User")
	runCodeSearchGit(t, repo, "config", "commit.gpgsign", "false")

	path := filepath.Join(repo, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n\nfunc OldName() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodeSearchGit(t, repo, "add", "sample.go")
	runCodeSearchGit(t, repo, "commit", "-q", "-m", "initial")
	ref := runCodeSearchGit(t, repo, "rev-parse", "HEAD")

	// The working tree has moved on, but a range/commit review must derive
	// suggestions from the reviewed ref rather than leak current source facts.
	if err := os.WriteFile(path, []byte("package sample\n\nfunc NewName() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := &tool.FileReader{
		RepoDir: repo,
		Mode:    tool.ModeCommit,
		Ref:     ref,
		Runner:  gitcmd.New(0),
	}
	provider := tool.NewCodeSearch(reader).WithDefinitionSource(CodeSearchDefinitions(reader))
	result, err := provider.Execute(context.Background(), map[string]any{
		"searches": []any{map[string]any{"search_text": "HandleName"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "OldName — sample.go:3") || strings.Contains(result, "NewName") {
		t.Fatalf("result = %q, want OldName from reviewed ref only", result)
	}
}

func runCodeSearchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

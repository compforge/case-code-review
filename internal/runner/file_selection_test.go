package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/rules"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func writeSelectionFile(t *testing.T, root, path string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runSelectionGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestProjectFilesBecomeContextInsteadOfUnits(t *testing.T) {
	repo := t.TempDir()
	for _, path := range []string{"backend/pyproject.toml", "backend/uv.lock", "backend/app.py"} {
		writeSelectionFile(t, repo, path)
	}

	a := &Runner{
		args: Args{RepoDir: repo},
		changes: []change.Change{
			{NewPath: "backend/app.py"},
			{NewPath: "backend/pyproject.toml"},
			{NewPath: "backend/uv.lock"},
		},
	}
	a.prepareFileSelections(context.Background())

	if got := a.countReviewable(a.changes); got != 1 {
		t.Fatalf("reviewable files = %d, want only app.py", got)
	}
	preview := a.buildPreview()
	if preview.ReviewableCount != 1 || preview.ContextCount != 2 || preview.ExcludedCount != 0 {
		t.Fatalf("preview counts = review:%d context:%d excluded:%d",
			preview.ReviewableCount, preview.ContextCount, preview.ExcludedCount)
	}

	a.changes = a.filterDiffs(a.changes)
	if len(a.changes) != 1 || a.changes[0].NewPath != "backend/app.py" {
		t.Fatalf("Unit targets = %+v, want backend/app.py", a.changes)
	}
	finder := componentFinder{selections: a.fileSelections, clues: a.componentClues}
	clues := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: "backend/app.py"}}})
	if len(clues) != 2 {
		t.Fatalf("project clues = %d, want manifest + lock", len(clues))
	}
	for _, clue := range clues {
		if clue.Kind != unit.ClueProject || clue.Relation != unit.RelProject {
			t.Fatalf("unexpected project clue: %+v", clue)
		}
	}
}

func TestProjectContextDoesNotLeakAcrossComponents(t *testing.T) {
	repo := t.TempDir()
	for _, path := range []string{
		"backend/pyproject.toml", "backend/app.py",
		"tools/pyproject.toml", "tools/tool.py",
	} {
		writeSelectionFile(t, repo, path)
	}

	a := &Runner{
		args: Args{RepoDir: repo},
		changes: []change.Change{
			{NewPath: "backend/app.py"},
			{NewPath: "backend/pyproject.toml"},
			{NewPath: "tools/tool.py"},
			{NewPath: "tools/pyproject.toml"},
		},
	}
	a.prepareFileSelections(context.Background())

	finder := componentFinder{selections: a.fileSelections, clues: a.componentClues}
	backend := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: "backend/app.py"}}})
	if len(backend) != 1 || backend[0].Ref != "backend/pyproject.toml" {
		t.Fatalf("backend clues = %+v", backend)
	}
	tools := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: "tools/tool.py"}}})
	if len(tools) != 1 || tools[0].Ref != "tools/pyproject.toml" {
		t.Fatalf("tools clues = %+v", tools)
	}
}

func TestOnlyManifestProducesNoUnitTarget(t *testing.T) {
	repo := t.TempDir()
	writeSelectionFile(t, repo, "pyproject.toml")
	a := &Runner{
		args:    Args{RepoDir: repo},
		changes: []change.Change{{NewPath: "pyproject.toml"}},
	}
	a.prepareFileSelections(context.Background())

	if got := a.filterDiffs(a.changes); len(got) != 0 {
		t.Fatalf("manifest unexpectedly became Unit target: %+v", got)
	}
	preview := a.buildPreview()
	if preview.ContextCount != 1 || !preview.Entries[0].ProvidesContext {
		t.Fatalf("manifest preview = %+v", preview)
	}
}

func TestExplicitIncludeCanForceManifestUnit(t *testing.T) {
	repo := t.TempDir()
	writeSelectionFile(t, repo, "pyproject.toml")
	a := &Runner{
		args: Args{
			RepoDir:    repo,
			FileFilter: &rules.FileFilter{Include: []string{"pyproject.toml"}},
		},
		changes: []change.Change{{NewPath: "pyproject.toml"}},
	}
	a.prepareFileSelections(context.Background())

	selection, _ := a.selectionFor(a.changes[0])
	if !selection.Target || selection.Context {
		t.Fatalf("explicit include selection = %+v", selection)
	}
}

func TestUnownedTomlKeepsGlobalAdmissionBehavior(t *testing.T) {
	repo := t.TempDir()
	writeSelectionFile(t, repo, "config.toml")
	a := &Runner{
		args:    Args{RepoDir: repo},
		changes: []change.Change{{NewPath: "config.toml"}},
	}
	a.prepareFileSelections(context.Background())

	selection, _ := a.selectionFor(a.changes[0])
	if !selection.Target || selection.HasComponent {
		t.Fatalf("unowned TOML selection = %+v", selection)
	}
}

func TestComponentDetectionUsesReviewedCommitTree(t *testing.T) {
	repo := t.TempDir()
	runSelectionGit(t, repo, "init", "-q")
	runSelectionGit(t, repo, "config", "user.email", "test@example.com")
	runSelectionGit(t, repo, "config", "user.name", "Test User")
	writeSelectionFile(t, repo, "pyproject.toml")
	writeSelectionFile(t, repo, "app.py")
	runSelectionGit(t, repo, "add", "pyproject.toml", "app.py")
	runSelectionGit(t, repo, "commit", "-q", "-m", "python component")
	target := runSelectionGit(t, repo, "rev-parse", "HEAD")

	if err := os.Remove(filepath.Join(repo, "pyproject.toml")); err != nil {
		t.Fatal(err)
	}
	runSelectionGit(t, repo, "add", "pyproject.toml")
	runSelectionGit(t, repo, "commit", "-q", "-m", "remove manifest")

	a := &Runner{
		args:    Args{RepoDir: repo, Commit: target},
		changes: []change.Change{{NewPath: "app.py"}},
	}
	a.prepareFileSelections(context.Background())
	selection, _ := a.selectionFor(a.changes[0])
	if !selection.Target || !selection.HasComponent || selection.Component.Kind != "python" {
		t.Fatalf("historical selection = %+v", selection)
	}
}

package runner

import (
	"context"
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/sourceview"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/rules"
	"github.com/qiankunli/case-code-review/internal/project"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func writeSelectionFile(t *testing.T, root, path string) {
	content := "test\n"
	if filepath.Base(path) == "package.json" {
		content = `{"devDependencies":{"typescript":"*"}}`
	}
	writeSelectionContent(t, root, path, content)
}

func writeSelectionContent(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
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
	prepareSelectionFixture(t, a)

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

func TestTypeScriptProjectFilesBecomeContextInsteadOfUnits(t *testing.T) {
	repo := t.TempDir()
	for _, path := range []string{"package.json", "tsconfig.json", "bun.lock", "src/main.ts"} {
		writeSelectionFile(t, repo, path)
	}

	a := &Runner{
		args: Args{RepoDir: repo},
		changes: []change.Change{
			{NewPath: "src/main.ts"},
			{NewPath: "package.json"},
			{NewPath: "tsconfig.json"},
			{NewPath: "bun.lock"},
		},
	}
	prepareSelectionFixture(t, a)

	if got := a.countReviewable(a.changes); got != 1 {
		t.Fatalf("reviewable files = %d, want only src/main.ts", got)
	}
	preview := a.buildPreview()
	if preview.ReviewableCount != 1 || preview.ContextCount != 3 || preview.ExcludedCount != 0 {
		t.Fatalf("preview counts = review:%d context:%d excluded:%d",
			preview.ReviewableCount, preview.ContextCount, preview.ExcludedCount)
	}

	selection, ok := a.selectionFor(a.changes[0])
	if !ok || selection.Component.Kind != project.TypeScript ||
		!selection.Roles.Has(project.RoleSource) || !selection.Roles.Has(project.RoleEntrypoint) {
		t.Fatalf("TypeScript entrypoint selection = %+v", selection)
	}
	finder := componentFinder{selections: a.fileSelections, clues: a.componentClues}
	clues := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: "src/main.ts"}}})
	if len(clues) != 4 {
		t.Fatalf("TypeScript project clues = %+v, want entrypoint + 3 context files", clues)
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
	prepareSelectionFixture(t, a)

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

func TestSourceRolesBecomeReviewClues(t *testing.T) {
	repo := t.TempDir()
	for _, path := range []string{"go.mod", "cmd/server/main.go"} {
		writeSelectionFile(t, repo, path)
	}
	a := &Runner{
		args:    Args{RepoDir: repo},
		changes: []change.Change{{NewPath: "cmd/server/main.go"}},
	}
	prepareSelectionFixture(t, a)

	selection, ok := a.selectionFor(a.changes[0])
	if !ok || !selection.Roles.Has(project.RoleSource) || !selection.Roles.Has(project.RoleEntrypoint) {
		t.Fatalf("entrypoint selection = %+v", selection)
	}
	finder := componentFinder{selections: a.fileSelections, clues: a.componentClues}
	clues := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: "cmd/server/main.go"}}})
	if len(clues) != 1 || clues[0].Kind != unit.ClueProject || clues[0].Relation != unit.RelSelf ||
		!strings.Contains(clues[0].Text, "entrypoint") {
		t.Fatalf("entrypoint clues = %+v", clues)
	}
}

func TestFastAPIDecoratorBecomesHandlerClue(t *testing.T) {
	repo := t.TempDir()
	writeSelectionFile(t, repo, "backend/pyproject.toml")
	path := "backend/api/v1/endpoints/items.py"
	writeSelectionContent(t, repo, path, `
from fastapi import APIRouter

router = APIRouter()

@router.post("/items")
async def create_item():
    return {"ok": True}
`)
	a := &Runner{
		args:    Args{RepoDir: repo},
		changes: []change.Change{{NewPath: path}},
	}
	prepareSelectionFixture(t, a)

	selection, ok := a.selectionFor(a.changes[0])
	if !ok || !selection.Roles.Has(project.RoleHandler) {
		t.Fatalf("FastAPI selection = %+v", selection)
	}
	finder := componentFinder{selections: a.fileSelections, clues: a.componentClues}
	clues := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: path}}})
	if len(clues) != 1 || !strings.Contains(clues[0].Text, "request handler") {
		t.Fatalf("FastAPI handler clues = %+v", clues)
	}
}

func TestOnlyManifestProducesNoUnitTarget(t *testing.T) {
	repo := t.TempDir()
	writeSelectionFile(t, repo, "pyproject.toml")
	a := &Runner{
		args:    Args{RepoDir: repo},
		changes: []change.Change{{NewPath: "pyproject.toml"}},
	}
	prepareSelectionFixture(t, a)

	if got := a.filterDiffs(a.changes); len(got) != 0 {
		t.Fatalf("manifest unexpectedly became Unit target: %+v", got)
	}
	preview := a.buildPreview()
	if preview.ContextCount != 1 || !preview.Entries[0].ProvidesContext {
		t.Fatalf("manifest preview = %+v", preview)
	}
}

func TestVersionRoleProducesNoUnitOrContext(t *testing.T) {
	repo := t.TempDir()
	writeSelectionFile(t, repo, "go.mod")
	writeSelectionFile(t, repo, "VERSION")
	a := &Runner{
		args:    Args{RepoDir: repo},
		changes: []change.Change{{NewPath: "VERSION"}},
	}
	prepareSelectionFixture(t, a)

	if got := a.filterDiffs(a.changes); len(got) != 0 {
		t.Fatalf("VERSION unexpectedly became Unit target: %+v", got)
	}
	preview := a.buildPreview()
	entry := preview.Entries[0]
	if preview.ExcludedCount != 1 || entry.FileRole != "version" || entry.ProvidesContext ||
		entry.ExcludeReason != ExcludeNonReviewRole {
		t.Fatalf("VERSION preview = %+v", preview)
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
	prepareSelectionFixture(t, a)

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
	prepareSelectionFixture(t, a)

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
	prepareSelectionFixture(t, a)
	selection, _ := a.selectionFor(a.changes[0])
	if !selection.Target || !selection.HasComponent || selection.Component.Kind != "python" {
		t.Fatalf("historical selection = %+v", selection)
	}
}

func prepareSelectionFixture(t *testing.T, a *Runner) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(a.args.RepoDir, ".git")); os.IsNotExist(err) {
		runSelectionGit(t, a.args.RepoDir, "init", "-q")
	}
	d, err := repocli.Diff(context.Background(), repocli.DiffRequest{Repository: a.args.RepoDir, EmptyBase: true, Head: a.args.Commit})
	if err != nil {
		t.Fatal(err)
	}
	a.repositoryDiff = d
	a.afterSource = sourceview.NewCaptured(d, false)
	a.beforeSource = sourceview.NewCaptured(d, true)
	if err := a.prepareFileSelections(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// +case:id=engineering_component_context,desc=`Nested manifests and mixed language files share repocli ownership`,expect=`Review context follows engineering roots while file roles retain their own language semantics`
func TestProjectContextUsesEngineeringComponent(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"server/pyproject.toml", "server/web/package.json", "server/web/main.ts", "other/go.mod", "other/main.go"} {
		writeSelectionFile(t, root, path)
	}
	a := &Runner{args: Args{RepoDir: root}, changes: []change.Change{{NewPath: "server/pyproject.toml"}, {NewPath: "server/web/main.ts"}, {NewPath: "other/main.go"}}}
	prepareSelectionFixture(t, a)
	selection := a.fileSelections["server/web/main.ts"]
	if selection.Component.Root != "server" || selection.Component.Kind != project.Python || !selection.Roles.Has(project.RoleSource) || !selection.Roles.Has(project.RoleEntrypoint) {
		t.Fatalf("selection: %+v", selection)
	}
	finder := componentFinder{selections: a.fileSelections, clues: a.componentClues}
	clues := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: "server/web/main.ts"}}})
	if len(clues) != 2 {
		t.Fatalf("missing parent manifest or file role clue: %+v", clues)
	}
	other := finder.Find(unit.Unit{Fragments: []unit.Fragment{{Path: "other/main.go"}}})
	for _, clue := range other {
		if clue.Ref == "server/pyproject.toml" {
			t.Fatal("component context leaked", clue)
		}
	}
}

func TestDeletedComponentUsesCapturedBaseLayout(t *testing.T) {
	root := t.TempDir()
	runSelectionGit(t, root, "init", "-q")
	runSelectionGit(t, root, "config", "user.email", "test@example.com")
	runSelectionGit(t, root, "config", "user.name", "Test")
	writeSelectionFile(t, root, "old/go.mod")
	writeSelectionFile(t, root, "old/main.go")
	runSelectionGit(t, root, "add", ".")
	runSelectionGit(t, root, "commit", "-qm", "base")
	if err := os.RemoveAll(filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	d, err := repocli.Diff(context.Background(), repocli.DiffRequest{Repository: root})
	if err != nil {
		t.Fatal(err)
	}
	a := &Runner{args: Args{RepoDir: root}, changes: d.Changes, repositoryDiff: d, afterSource: sourceview.NewCaptured(d, false)}
	if err := a.prepareFileSelections(context.Background()); err != nil {
		t.Fatal(err)
	}
	selection := a.fileSelections["old/main.go"]
	if !selection.HasComponent || selection.Component.Root != "old" || selection.Component.Kind != project.Go {
		t.Fatalf("deleted ownership: %+v", selection)
	}
}

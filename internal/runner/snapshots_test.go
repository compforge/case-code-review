package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/runner/source"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

func TestCapturedGraphAndOldContractsSurviveWorkspaceChanges(t *testing.T) {
	repo := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		p := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write("go.mod", "module example\n")
	write("a.go", "package p\nfunc A(){ Guard() }\n")
	write("guard.go", "package p\n// Guard checks authorization.\nfunc Guard(){}\n")
	write(".casecodereview/spec.json", `{"guard.go::Guard":{"spec":"old authorization contract"}}`)
	git("init", "-q")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("-c", "commit.gpgsign=false", "add", ".")
	git("-c", "commit.gpgsign=false", "commit", "-qm", "base")
	write("a.go", "package p\nfunc A(){}\n")
	if err := os.Remove(filepath.Join(repo, "guard.go")); err != nil {
		t.Fatal(err)
	}
	write(".casecodereview/spec.json", `{"guard.go::Guard":{"spec":"new unrelated contract"}}`)
	runner := gitcmd.New(0)
	provider := source.NewWorkspaceProvider(repo, runner)
	changes, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := &Runner{args: Args{RepoDir: repo, GitRunner: runner}, analyzer: language.NewAnalyzer(repo), repositoryDiff: provider.Diff, beforeSource: provider.Before}
	a.analyzer.SetSnapshot(provider.After)
	for _, d := range changes {
		if strings.HasSuffix(d.Path(), ".go") {
			a.changes = append(a.changes, d)
		}
	}
	a.captureGraphs(context.Background())
	// Current source no longer matches the captured diff. It must not create a
	// new call in the graph used to assess that diff.
	write("a.go", "package p\nfunc A(){ Surprise() }\nfunc Surprise(){}\n")
	us, err := a.splitUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.analyzer.Repository().Sources["a.go"], "Surprise") {
		t.Fatal("worktree leaked into captured graph")
	}
	var oldContract, oldDoc bool
	for _, u := range us {
		for _, c := range u.Clues {
			if c.Snapshot == "" {
				continue
			}
			if strings.Contains(c.Text, "new unrelated") {
				t.Fatal("current contract mislabeled as old")
			}
			oldContract = oldContract || strings.Contains(c.Text, "old authorization")
			oldDoc = oldDoc || strings.Contains(c.Text, "checks authorization")
		}
	}
	if !oldContract || !oldDoc {
		t.Fatalf("lost old contract/doc: %+v", us)
	}
}

func TestBeforeClueLabelsDoNotLookCurrent(t *testing.T) {
	c := unit.Clue{Kind: unit.ClueDoc, Relation: unit.RelUsed, Ref: "a.go::A", Snapshot: "old-commit", Text: "old docs"}
	text, _, _, _ := renderClues([]unit.Clue{c})
	if !strings.Contains(text, "before change, snapshot old-commit") {
		t.Fatal(text)
	}
}

// The graph, preload, search, file tools and contract catalog must describe one
// review input even while the developer continues editing and committing.
func TestReviewSourcesRemainPinned(t *testing.T) {
	for _, mode := range []string{"commit", "workspace"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = repo
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
			}
			write := func(name, content string) {
				t.Helper()
				full := filepath.Join(repo, name)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-q")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.com")
			commit := func() { git("add", "."); git("-c", "commit.gpgsign=false", "commit", "-qm", "fixture") }
			write("go.mod", "module example.com/reviewfixture\n\ngo 1.26\n")
			write("a.go", "package p\nfunc A() int { return 0 }\n")
			write("untouched.go", "package p\nfunc Stable() {}\n")
			write("deleted.go", "package p\nfunc Deleted() {}\n")
			commit()
			target := "package p\nfunc A() int { return 1 }\n"
			write("a.go", target)
			write("new.go", "package p\nfunc Added() {}\n")
			write(".casecodereview/spec.json", `{"a.go::A":{"spec":"captured contract"}}`)
			if err := os.Remove(filepath.Join(repo, "deleted.go")); err != nil {
				t.Fatal(err)
			}
			ref := ""
			reviewMode := tool.ModeWorkspace
			if mode == "commit" {
				commit()
				ref = "HEAD"
				reviewMode = tool.ModeCommit
			}
			runner := gitcmd.New(0)
			reader := &tool.FileReader{RepoDir: repo, Mode: reviewMode, Ref: ref, Runner: runner}
			registry := tool.NewRegistry()
			registry.Register(tool.NewFileRead(reader))
			registry.Register(tool.NewFileReadBase(&tool.FileReader{RepoDir: repo, Mode: tool.ModeCommit, Runner: runner}))
			recording := session.New(repo, "main", "test", session.SessionOptions{})
			defer recording.Finalize()
			a := New(Args{RepoDir: repo, Commit: ref, GitRunner: runner, Tools: registry, Session: recording})
			if err := a.loadChanges(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(repo, "go.mod")); err != nil {
				t.Fatal(err)
			}
			write("a.go", "package p\nfunc Surprise() {}\n")
			write("untouched.go", "package p\nfunc SurpriseStable() {}\n")
			write("new.go", "package p\nfunc SurpriseAdded() {}\n")
			write("deleted.go", "package p\nfunc SurpriseDeleted() {}\n")
			write(".casecodereview/spec.json", `{"a.go::A":{"spec":"later contract"}}`)
			commit()
			a.prepareFileSelections(context.Background())
			if !a.fileSelections["a.go"].HasComponent {
				t.Fatal("component drifted away from captured manifest")
			}
			graphSource := a.analyzer.Repository().Sources["a.go"]
			toolSource, err := reader.Read(context.Background(), "a.go")
			if err != nil {
				t.Fatal(err)
			}
			if graphSource != target || toolSource != target {
				t.Fatalf("graph=%q reader=%q", graphSource, toolSource)
			}
			if _, err := reader.Read(context.Background(), "deleted.go"); err == nil {
				t.Fatal("deleted path was resurrected")
			}
			lines, _, err := reader.ReadLines(context.Background(), "untouched.go", 1, 10)
			if err != nil || strings.Contains(strings.Join(lines, "\n"), "Surprise") {
				t.Fatalf("unchanged baseline drift: %v %v", lines, err)
			}
			search := tool.NewCodeSearch(reader)
			result, err := search.Execute(context.Background(), map[string]any{"searches": []any{map[string]any{"query": "Surprise"}, map[string]any{"query": "Added", "file_patterns": []any{"*.go"}}}})
			if err != nil || strings.Contains(result, "SurpriseAdded") || !strings.Contains(result, "func Added()") {
				t.Fatalf("search drift: %s %v", result, err)
			}
			found, err := tool.NewFileFind(reader).Execute(context.Background(), map[string]any{"query_name": "deleted.go"})
			if err != nil || strings.Contains(found, "deleted.go") {
				t.Fatalf("file list drift: %s %v", found, err)
			}
			catalog, err := spec.LoadSnapshot(context.Background(), repo, "", reader.Snapshot)
			if err != nil || catalog.Local["a.go::A"].Spec != "captured contract" {
				t.Fatalf("contract drift: %+v %v", catalog, err)
			}
		})
	}
}

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/runner/source"
	"github.com/qiankunli/case-code-review/internal/unit"
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
	changes, err := source.NewWorkspaceProvider(repo, runner).GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := &Runner{args: Args{RepoDir: repo, GitRunner: runner}, analyzer: language.NewAnalyzer(repo)}
	for _, d := range changes {
		if strings.HasSuffix(d.Path(), ".go") {
			a.changes = append(a.changes, d)
		}
	}
	a.captureGraphs(context.Background())
	// Current source no longer matches the captured diff. It must not create a
	// new call in the graph used to assess that diff.
	write("a.go", "package p\nfunc A(){ Surprise() }\nfunc Surprise(){}\n")
	us, err := a.splitUnits()
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

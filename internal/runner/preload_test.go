package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/sourcecontext"
)

func newPreloadRunner(t *testing.T, files map[string]string) *Runner {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := tool.NewRegistry()
	reg.Register(tool.NewFileRead(&tool.FileReader{RepoDir: dir, Mode: tool.ModeWorkspace}))
	return &Runner{args: Args{RepoDir: dir, Tools: reg}}
}

func TestPreloadReviewFilesWholeSource(t *testing.T) {
	a := newPreloadRunner(t, map[string]string{
		"pkg/a.go": "package a\n\nfunc F() {}\n",
	})
	u := unit.UnitOf(unit.Fragment{Path: "pkg/a.go", Symbols: []string{"pkg/a.go::F"}})
	own, related, outcomes := a.preloadReviewFiles(context.Background(), u)
	if len(own) != 1 || len(related) != 0 {
		t.Fatalf("preloaded files off: own=%d related=%d", len(own), len(related))
	}
	if own[0].Path != "pkg/a.go" || own[0].Label != "code under review" ||
		!strings.Contains(own[0].Content, "1|package a") || !strings.Contains(own[0].Content, "3|func F() {}") {
		t.Fatalf("own source off: %+v", own[0])
	}
	if own[0].Priority() != priorityUnitSource {
		t.Fatalf("diff file priority = %d, want %d", own[0].Priority(), priorityUnitSource)
	}
	if !strings.Contains(own[0].Outline, "File outline: pkg/a.go (go)") ||
		!strings.Contains(own[0].Outline, "func F()") {
		t.Fatalf("own outline off:\n%s", own[0].Outline)
	}
	if len(outcomes) != 1 || outcomes[0] != "whole pkg/a.go" {
		t.Fatalf("outcomes = %v", outcomes)
	}

	missing := unit.UnitOf(unit.Fragment{Path: "gone.go"})
	own, _, outcomes = a.preloadReviewFiles(context.Background(), missing)
	if len(own) != 0 || len(outcomes) != 1 || outcomes[0] != "unreadable gone.go" {
		t.Fatalf("missing source result off: own=%d outcomes=%v", len(own), outcomes)
	}
}

func TestPreloadReviewFilesKeepsLargeWholeSource(t *testing.T) {
	big := strings.Repeat("x", 40*1024)
	a := newPreloadRunner(t, map[string]string{"big.go": big, "small.go": "ok\n"})
	u := unit.Unit{
		Scope: unit.ScopeCallChain,
		Fragments: []unit.Fragment{
			{Path: "big.go"},
			{Path: "small.go"},
		},
	}
	own, _, outcomes := a.preloadReviewFiles(context.Background(), u)
	if len(own) != 2 || own[0].Path != "big.go" || own[1].Path != "small.go" ||
		!strings.Contains(own[0].Content, big) {
		t.Fatalf("full source preload off: own=%+v", own)
	}
	if len(outcomes) != 2 || outcomes[0] != "whole big.go" || outcomes[1] != "whole small.go" {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestPreloadReviewFilesAddsBoundedCallNeighbors(t *testing.T) {
	a := newPreloadRunner(t, map[string]string{
		"a.go": "package p\n\nfunc F() {}\n",
		"b.go": "package p\n\nfunc G() {}\n",
		"c.go": "package p\n\nfunc Entry() {\n\tF()\n}\n",
	})
	u := unit.NewChainUnit([]unit.Fragment{
		{Path: "a.go", Symbols: []string{"a.go::F"}},
		{Path: "b.go", Symbols: []string{"b.go::G"}},
	})
	u.Clues = []unit.Clue{
		{Kind: unit.ClueSpec, Relation: unit.RelCaller, Ref: "c.go::Entry", Text: "spec"},
		{Kind: unit.ClueDoc, Relation: unit.RelCallee, Ref: "a.go::F2", Text: "member file — skip"},
		{Kind: unit.ClueSpec, Relation: unit.RelOwner, Ref: "d.go::T", Text: "not a call edge — skip"},
	}
	own, related, _ := a.preloadReviewFiles(context.Background(), u)
	if len(own) != 2 || len(related) != 1 {
		t.Fatalf("source counts off: own=%d related=%d", len(own), len(related))
	}
	if related[0].Path != "c.go" || related[0].Label != "related caller c.go::Entry" ||
		!strings.Contains(related[0].Content, "LINE_RANGE: 3-5") ||
		strings.Contains(related[0].Content, "1|package p") {
		t.Fatalf("related source off: %+v", related[0])
	}
	if own[0].Priority() != priorityUnitSource || related[0].Priority() != priorityRelatedSource {
		t.Fatalf("source priorities: unit=%d related=%d", own[0].Priority(), related[0].Priority())
	}

	a.features = feature.Set{feature.NeighborSource: false}
	_, related, _ = a.preloadReviewFiles(context.Background(), u)
	if len(related) != 0 {
		t.Fatalf("neighbor_source off must remove related source: %+v", related)
	}
}

func TestAssembleReviewMessages(t *testing.T) {
	build := func(unitSlot, relatedSlot string) []llm.Message {
		return []llm.Message{
			llm.NewTextMessage("system", "sys"),
			llm.NewTextMessage("user", "task\n[unit:"+unitSlot+"]\n[rel:"+relatedSlot+"]"),
		}
	}
	own := []*msg.File{
		msg.NewFile("a.go", 1, 2, 2, "File: a.go (Total lines: 2)\n1|x\n2|y").
			ConfigurePresentation("code under review", ""),
	}
	related := []*msg.File{
		msg.NewFile("n.go", 5, 9, 9, "File: n.go (Total lines: 9)\nLINE_RANGE: 5-9\n5|z").
			ConfigurePresentation("related caller n.go::C", ""),
	}
	a := &Runner{}

	domain := a.assembleReviewMessages(build, own, related, nil)
	if len(domain) != 4 {
		t.Fatalf("messages = %d, want 4", len(domain))
	}
	taskWire, _ := domain[1].ToMessage()
	taskText := taskWire.TextContent()
	if !strings.Contains(taskText, unitSourcePointer) || !strings.Contains(taskText, relatedSourcePointer) {
		t.Fatalf("task slots off:\n%s", taskText)
	}
	if _, ok := domain[0].(agentgo.Message); !ok {
		t.Fatalf("system task should use the generic message type: %T", domain[0])
	}
	if domain[2] != own[0] || domain[3] != related[0] {
		t.Fatal("assembly must pass full File messages to Harness unchanged")
	}
}

func TestInitialFileContextUsesOutlineAndRepositoryReferences(t *testing.T) {
	a := newPreloadRunner(t, map[string]string{
		"a.go":           "package p\n\nfunc F() {}\n",
		"owner.go":       "package p\n\ntype Owner struct{}\n",
		"repository.go":  "package p\n\nfunc RepositoryUser() { F() }\n",
		"pyproject.toml": "[project]\nname = 'p'\n",
	})
	a.repoIndex = sourcecontext.Scan(language.NewAnalyzer(a.args.RepoDir))
	u := unit.UnitOf(unit.Fragment{Path: "a.go", Symbols: []string{"a.go::F"}})
	u.Clues = []unit.Clue{
		{Relation: unit.RelOwner, Ref: "owner.go::Owner"},
		{Relation: unit.RelProject, Ref: "pyproject.toml"},
	}
	own, related, _ := a.preloadReviewFiles(context.Background(), u)
	entries, attempts := a.initialFileContext(context.Background(), u, nil, own, related)
	byPath := make(map[string]msg.FileContextEntry)
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	if byPath["owner.go"].View != msg.ViewOutline || !strings.Contains(byPath["owner.go"].Content, "type Owner") {
		t.Fatalf("owner context = %+v", byPath["owner.go"])
	}
	if byPath["pyproject.toml"].View != msg.ViewReference {
		t.Fatalf("project context = %+v", byPath["pyproject.toml"])
	}
	if byPath["owner.go"].Reason != "owner" || byPath["owner.go"].Ref != "owner.go::Owner" {
		t.Fatalf("owner admission = %+v", byPath["owner.go"])
	}
	if byPath["repository.go"].Reason != "repository_reference" {
		t.Fatalf("repository context = %+v", byPath["repository.go"])
	}
	if len(attempts) != 2 || attempts[0].Path != "owner.go" || attempts[0].Outcome != "admitted" ||
		attempts[1].Path != "repository.go" || attempts[1].Outcome != "admitted" {
		t.Fatalf("initial outline attempts = %+v", attempts)
	}
}

func TestDescribePreloadedSources(t *testing.T) {
	a := &Runner{}
	u := unit.NewChainUnit([]unit.Fragment{
		{Path: "a.go", Symbols: []string{"a.go::F"}},
		{Path: "b.go", Symbols: []string{"b.go::G"}},
	})
	u.Clues = []unit.Clue{{Relation: unit.RelCaller, Ref: "c.go::Entry"}}
	got := a.describePreloadedSources(u)
	if len(got) != 3 || !strings.Contains(got[0], "a.go::F") || got[2] != "caller c.go::Entry (body)" {
		t.Fatalf("descriptors = %v", got)
	}
}

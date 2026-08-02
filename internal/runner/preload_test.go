package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
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
	own, related, notes, outcomes := a.preloadReviewFiles(context.Background(), u)
	if len(own) != 1 || len(related) != 0 || len(notes) != 0 {
		t.Fatalf("preloaded files off: own=%d related=%d notes=%v", len(own), len(related), notes)
	}
	if own[0].Path != "pkg/a.go" || own[0].Label != "code under review" ||
		!strings.Contains(own[0].Content, "1|package a") || !strings.Contains(own[0].Content, "3|func F() {}") {
		t.Fatalf("own source off: %+v", own[0])
	}
	if len(outcomes) != 1 || outcomes[0] != "whole pkg/a.go" {
		t.Fatalf("outcomes = %v", outcomes)
	}

	missing := unit.UnitOf(unit.Fragment{Path: "gone.go"})
	own, _, _, outcomes = a.preloadReviewFiles(context.Background(), missing)
	if len(own) != 0 || len(outcomes) != 1 || outcomes[0] != "unreadable gone.go" {
		t.Fatalf("missing source result off: own=%d outcomes=%v", len(own), outcomes)
	}
}

func TestPreloadReviewFilesBudgetAndRangedFallback(t *testing.T) {
	big := strings.Repeat("x", preloadSourceBudget+1)
	a := newPreloadRunner(t, map[string]string{"big.go": big, "small.go": "ok\n"})
	u := unit.Unit{
		Scope: unit.ScopeCallChain,
		Fragments: []unit.Fragment{
			{Path: "big.go"},
			{Path: "small.go"},
		},
	}
	own, _, notes, outcomes := a.preloadReviewFiles(context.Background(), u)
	if len(own) != 1 || own[0].Path != "small.go" || len(notes) != 1 ||
		!strings.Contains(notes[0], "exceeds the preload budget") {
		t.Fatalf("budget fallback off: own=%+v notes=%v", own, notes)
	}
	if len(outcomes) != 2 || outcomes[0] != "budget_miss big.go" || outcomes[1] != "whole small.go" {
		t.Fatalf("outcomes = %v", outcomes)
	}

	filler := "// " + strings.Repeat("y", 120)
	var source strings.Builder
	source.WriteString("package big\n\n")
	for range preloadSourceBudget / len(filler) {
		source.WriteString(filler + "\n")
	}
	source.WriteString("\nfunc Changed() int {\n\treturn 42\n}\n")
	a = newPreloadRunner(t, map[string]string{"big.go": source.String()})
	u = unit.UnitOf(unit.Fragment{Path: "big.go", Symbols: []string{"big.go::Changed"}})
	own, _, notes, _ = a.preloadReviewFiles(context.Background(), u)
	if len(own) != 1 || len(notes) != 0 || !strings.Contains(own[0].Content, "LINE_RANGE: ") ||
		!strings.Contains(own[0].Content, "func Changed() int {") || strings.Contains(own[0].Content, filler) {
		t.Fatalf("ranged fallback off: own=%+v notes=%v", own, notes)
	}

	a.features = feature.Set{feature.RangedPreload: false}
	own, _, notes, _ = a.preloadReviewFiles(context.Background(), u)
	if len(own) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "exceeds the preload budget") {
		t.Fatalf("disabled ranged fallback off: own=%+v notes=%v", own, notes)
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
	own, related, _, _ := a.preloadReviewFiles(context.Background(), u)
	if len(own) != 2 || len(related) != 1 {
		t.Fatalf("source counts off: own=%d related=%d", len(own), len(related))
	}
	if related[0].Path != "c.go" || related[0].Label != "related caller c.go::Entry" ||
		!strings.Contains(related[0].Content, "LINE_RANGE: 3-5") ||
		strings.Contains(related[0].Content, "1|package p") {
		t.Fatalf("related source off: %+v", related[0])
	}

	a.features = feature.Set{feature.NeighborSource: false}
	_, related, _, _ = a.preloadReviewFiles(context.Background(), u)
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
	notes := []string{"File: big.go — 99999 bytes exceeds the preload budget; read on demand via read_files"}
	a := &Runner{}

	deb := session.Debrief{}
	domain := a.assembleReviewMessages(build, own, related, notes, 1<<20, &deb)
	if len(domain) != 4 {
		t.Fatalf("messages = %d, want 4", len(domain))
	}
	taskWire := domain[1].ToLLM(msg.CompactionNone)
	taskText := taskWire.ExtractText()
	if !strings.Contains(taskText, unitSourcePointer) || !strings.Contains(taskText, notes[0]) ||
		!strings.Contains(taskText, relatedSourcePointer) {
		t.Fatalf("task slots off:\n%s", taskText)
	}
	if _, ok := domain[0].(msg.Raw); !ok {
		t.Fatalf("system task should use the generic message type: %T", domain[0])
	}

	fullTokens := llm.CountMessagesTokens(msg.Lower(domain))
	deb = session.Debrief{}
	domain = a.assembleReviewMessages(build, own, related, notes, fullTokens-1, &deb)
	if len(domain) != 3 || len(deb.Degradations) != 1 || deb.Degradations[0] != "related_source_dropped" {
		t.Fatalf("related-drop stage off: n=%d deg=%v", len(domain), deb.Degradations)
	}
	taskWire = domain[1].ToLLM(msg.CompactionNone)
	if strings.Contains(taskWire.ExtractText(), relatedSourcePointer) {
		t.Fatal("dropped related source must not remain advertised")
	}

	deb = session.Debrief{}
	domain = a.assembleReviewMessages(build, own, related, notes, 10, &deb)
	taskWire = domain[1].ToLLM(msg.CompactionNone)
	if len(domain) != 2 || !strings.Contains(taskWire.ExtractText(), sourceNotPreloaded) ||
		len(deb.Degradations) != 2 {
		t.Fatalf("own-drop stage off: n=%d deg=%v", len(domain), deb.Degradations)
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

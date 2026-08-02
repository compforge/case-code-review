package scan

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func newRunnerForTest(t *testing.T, tpl template.ScanTemplate) *Runner {
	t.Helper()
	return New(Args{
		Template: tpl,
		Findings: finding.NewCollector(),
		Tools:    tool.NewRegistry(),
		Session: session.New(t.TempDir(), "main", "test-model", session.SessionOptions{
			ReviewMode: session.ReviewModeFullScan,
		}),
	})
}

func makeTemplateWithFullScan() template.ScanTemplate {
	return template.ScanTemplate{
		MaxTokens:           1000,
		MaxToolRequestTimes: 5,
		MainTask: template.LlmConversation{
			Messages: []template.ChatMessage{
				{Role: "system", Content: "scan system rule={{system_rule}}"},
				{
					Role: "user",
					Content: "path={{current_file_path}}\n" +
						"date={{current_system_date_time}}\n" +
						"siblings=[{{change_files}}]\n" +
						"bg={{requirement_background}}\n" +
						"plan={{plan_guidance}}\n" +
						"<content>\n{{file_content}}\n</content>",
				},
			},
		},
	}
}

func TestFormatPlanGuidance_FullJSON(t *testing.T) {
	raw := "```json\n" + `{
  "summary": "this file orchestrates X.",
  "checkpoints": [
    {"focus": "race in cache", "lines": "45-78", "why": "writes under read lock"},
    {"focus": "error swallowing", "lines": "120-130", "why": "ignored Err return"}
  ]
}` + "\n```"
	got := formatPlanGuidance(raw)
	for _, want := range []string{
		"**Summary**: this file orchestrates X.",
		"1. `race in cache` (lines 45-78) — writes under read lock",
		"2. `error swallowing` (lines 120-130) — ignored Err return",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\nfull:\n%s", want, got)
		}
	}
}

func TestFormatPlanGuidance_EmptyAndMalformed(t *testing.T) {
	if got := formatPlanGuidance(""); got != "" {
		t.Errorf("empty input should yield empty guidance, got %q", got)
	}
	// Malformed JSON falls back to the raw text so we don't lose what the
	// model said — better to feed bad text to the reviewer than nothing.
	raw := "the LLM forgot to use JSON: focus on error handling"
	if got := formatPlanGuidance(raw); got != raw {
		t.Errorf("malformed input should pass through raw, got %q", got)
	}
}

func TestFormatPlanGuidance_SummaryOnly(t *testing.T) {
	raw := `{"summary": "small helper file", "checkpoints": []}`
	got := formatPlanGuidance(raw)
	if !strings.Contains(got, "**Summary**: small helper file") {
		t.Errorf("missing summary header, got %q", got)
	}
	if strings.Contains(got, "Focus areas") {
		t.Errorf("should not render focus header when no checkpoints, got %q", got)
	}
}

// TestPreview_DoesNotMutateAgentItems guards against re-introducing a
// side-effect that pre-populated a.items, which made subsequent Run calls
// on the same Runner silently observe stale state.
func TestPreview_DoesNotMutateAgentItems(t *testing.T) {
	repo := initTestRepo(t)
	writeFile(t, repo, "a.go", []byte("package a\n"))
	writeFile(t, repo, "b.go", []byte("package b\n"))
	gitCommit(t, repo, "init")

	a := New(Args{
		RepoDir:   repo,
		GitRunner: nil,
		Template:  makeTemplateWithFullScan(),
	})
	if got := a.items; got != nil {
		t.Fatalf("pre-Preview items should be nil, got %v", got)
	}
	if _, err := a.Preview(t.Context()); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if a.items != nil {
		t.Errorf("Preview must not mutate a.items; got %d items", len(a.items))
	}
}

// TestPreview_EmptyResultEntriesIsNonNilSlice prevents `"files":null` in
// JSON output when there is nothing reviewable to enumerate.
func TestPreview_EmptyResultEntriesIsNonNilSlice(t *testing.T) {
	// Empty repo → empty Entries
	repo := initTestRepo(t)
	a := New(Args{
		RepoDir:  repo,
		Template: makeTemplateWithFullScan(),
	})
	got, err := a.Preview(t.Context())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got.Entries == nil {
		t.Errorf("Entries must be non-nil even when empty (JSON would emit null)")
	}
	if len(got.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(got.Entries))
	}
}

func TestBuildSummaryCommentsList_TruncatesAndOneLines(t *testing.T) {
	long := strings.Repeat("x", 400)
	cs := []finding.Finding{
		{Path: "a.go", Content: "line one\nline two\nline three"},
		{Path: "b.go", Content: long},
	}
	got := buildSummaryCommentsList(cs)

	// Newlines in content should be collapsed to spaces.
	if strings.Contains(got, "line one\nline two") {
		t.Errorf("expected content newlines to be flattened, got:\n%s", got)
	}
	if !strings.Contains(got, "- `a.go`: line one line two line three") {
		t.Errorf("expected path-anchored prefix, got:\n%s", got)
	}
	// Long content truncated to ~280 + "..." marker.
	if !strings.Contains(got, "...") {
		t.Errorf("expected truncation marker on long content, got:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 320 { // 280 content + small path/prefix overhead
			t.Errorf("line not capped: len=%d %q", len(line), line)
		}
	}
}

func TestMaybeRunPlan_SkipPathsDoNotCallLLM(t *testing.T) {
	tpl := makeTemplateWithFullScan()
	// no PlanTask attached → must return sentinel without crashing
	a := newRunnerForTest(t, tpl)
	guidance := a.maybeRunPlan(t.Context(), Item{Path: "x.go", Content: "package x"}, "rule")
	if !strings.Contains(guidance, "no pre-scan plan") {
		t.Errorf("expected fallback sentinel, got %q", guidance)
	}

	// PlanTask attached but SkipPlan set
	tpl.PlanTask = &template.LlmConversation{
		Messages: []template.ChatMessage{{Role: "user", Content: "plan {{file_content}}"}},
	}
	a2 := New(Args{
		Template: tpl,
		Findings: finding.NewCollector(),
		Tools:    tool.NewRegistry(),
		Session: session.New(t.TempDir(), "main", "test-model", session.SessionOptions{
			ReviewMode: session.ReviewModeFullScan,
		}),
		SkipPlan: true,
	})
	guidance2 := a2.maybeRunPlan(t.Context(), Item{Path: "x.go", Content: "package x"}, "rule")
	if !strings.Contains(guidance2, "no pre-scan plan") {
		t.Errorf("SkipPlan should suppress plan, got %q", guidance2)
	}
}

func TestRenderMessages(t *testing.T) {
	tpl := makeTemplateWithFullScan()
	a := newRunnerForTest(t, tpl)
	a.currentDate = "2026-06-09 10:00"
	a.args.Background = "ticket-123"

	it := Item{
		Path:    "internal/foo/bar.go",
		Content: "package foo\n\nfunc Bar() {}\n",
	}
	msgs := a.renderMessages(it, "rule-text", "(no pre-scan plan; review the entire file as usual)")

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	sysText := msgs[0].ExtractText()
	if !strings.Contains(sysText, "rule=rule-text") {
		t.Errorf("system missing system_rule: %q", sysText)
	}

	userText := msgs[1].ExtractText()
	checks := map[string]string{
		"path":     "path=internal/foo/bar.go",
		"date":     "date=2026-06-09 10:00",
		"siblings": "siblings=[" + changeFilesScanLiteral + "]",
		"bg":       "bg=ticket-123",
		"content":  "<content>\npackage foo\n\nfunc Bar() {}\n\n</content>",
	}
	for label, want := range checks {
		if !strings.Contains(userText, want) {
			t.Errorf("%s missing %q\nfull: %q", label, want, userText)
		}
	}
	for _, leak := range []string{"{{diff}}", "{{file_content}}", "{{change_files}}", "{{plan_guidance}}"} {
		if strings.Contains(userText, leak) {
			t.Errorf("placeholder %s leaked into prompt", leak)
		}
	}
}

func TestFilterLargeScans(t *testing.T) {
	tpl := makeTemplateWithFullScan()
	tpl.MaxTokens = 40 // threshold = 32
	a := newRunnerForTest(t, tpl)

	short := strings.Repeat("a ", 5)
	huge := strings.Repeat("token ", 200)
	in := []Item{
		{Path: "a.go", Content: short},
		{Path: "huge.go", Content: huge},
		{Path: "b.go", Content: short},
	}
	out := a.filterLargeScans(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 kept, got %d", len(out))
	}
	for _, it := range out {
		if it.Path == "huge.go" {
			t.Errorf("huge.go should have been filtered")
		}
	}
}

func TestFilterLargeScans_NoLimit(t *testing.T) {
	tpl := makeTemplateWithFullScan()
	tpl.MaxTokens = 0
	a := newRunnerForTest(t, tpl)
	in := []Item{
		{Path: "a.go", Content: "anything"},
		{Path: "b.go", Content: strings.Repeat("x ", 1000)},
	}
	out := a.filterLargeScans(in)
	if len(out) != 2 {
		t.Errorf("with MaxTokens=0 nothing should be filtered, got %d", len(out))
	}
}

func TestInjectScanContentMap(t *testing.T) {
	tpl := makeTemplateWithFullScan()
	a := newRunnerForTest(t, tpl)
	a.args.Tools.Register(tool.NewFileReadDiff(tool.DiffMap{}))

	a.items = []Item{
		{Path: "x.go", Content: "package x"},
		{Path: "y.go", Content: "package y"},
	}
	a.injectScanContentMap()

	p, ok := a.args.Tools.Get(tool.FileReadDiff.Name())
	if !ok {
		t.Fatal("read_diffs not registered")
	}
	frd := p.(*tool.FileReadDiffProvider)
	res, err := frd.Execute(t.Context(), map[string]any{
		"paths": []any{"x.go", "y.go", "missing.go"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res, "package x") || !strings.Contains(res, "package y") {
		t.Errorf("missing scan content:\n%s", res)
	}
}

func TestNewRunner_SetsSessionMode(t *testing.T) {
	a := New(Args{Template: makeTemplateWithFullScan()})
	if a.session.ReviewMode != session.ReviewModeFullScan {
		t.Errorf("ReviewMode = %q, want %q", a.session.ReviewMode, session.ReviewModeFullScan)
	}
}

func TestRunner_Warnings_RoundTrip(t *testing.T) {
	a := newRunnerForTest(t, makeTemplateWithFullScan())
	a.recordWarning("foo", "x.go", "boom")
	ws := a.Warnings()
	if len(ws) != 1 || ws[0].Type != "foo" || ws[0].File != "x.go" {
		t.Errorf("warnings = %+v", ws)
	}
}

// Keep scan's public counters tied to the execution aggregate that records
// both Harness turns and scan's single-shot plan/dedup/summary calls.
func TestTokenCountersDelegateToExecutor(t *testing.T) {
	a := newRunnerForTest(t, makeTemplateWithFullScan())
	if a.TotalInputTokens() != a.executor.TotalInputTokens() ||
		a.TotalOutputTokens() != a.executor.TotalOutputTokens() ||
		a.TotalCacheReadTokens() != a.executor.TotalCacheReadTokens() ||
		a.TotalCacheWriteTokens() != a.executor.TotalCacheWriteTokens() {
		t.Fatal("scan.Runner token getters must mirror executor")
	}
}

func TestItemFormsWholeFileUnit(t *testing.T) {
	u := (Item{Path: "pkg/a.go", Content: "package a\n", LineCount: 1}).AsUnit()
	if u.ID != "pkg/a.go" || u.Scope != unit.ScopeFile || u.Formed != unit.FormedFile {
		t.Fatalf("unexpected scan unit identity: %+v", u)
	}
	if len(u.Fragments) != 1 || u.Fragments[0].Path != "pkg/a.go" || u.Fragments[0].Diff != "package a\n" {
		t.Fatalf("scan must form one whole-file fragment: %+v", u.Fragments)
	}
}

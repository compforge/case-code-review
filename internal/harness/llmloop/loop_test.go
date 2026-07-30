package llmloop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecuteToolCall_CodeCommentOverridesHallucinatedPath(t *testing.T) {
	collector := tool.NewCommentCollector()
	reg := tool.NewRegistry()
	reg.Register(&tool.CodeCommentProvider{Collector: collector})
	reg.Freeze()

	r := NewRunner(Deps{
		Tools:            reg,
		CommentCollector: collector,
	})

	args := map[string]any{
		"path": "wrong.go",
		"comments": []any{
			map[string]any{
				"content":       "issue",
				"existing_code": "foo",
			},
		},
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}

	cp := r.executeToolCall(context.Background(), session.Scope{ID: "correct.go", Kind: "file", Type: "file", Paths: []string{"correct.go"}}, llm.ToolCall{
		Function: llm.FunctionCall{
			Name:      "code_comment",
			Arguments: string(argsJSON),
		},
	}, nil, "")
	if cp.Data != tool.CommentSucceed {
		t.Fatalf("unexpected result: %+v", cp)
	}

	comments := collector.Comments()
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Path != "correct.go" {
		t.Errorf("path override: got %q, want %q", comments[0].Path, "correct.go")
	}
}

func TestRunnerModelsUsed(t *testing.T) {
	r := NewRunner(Deps{})
	if got := r.ModelsUsed(); len(got) != 0 {
		t.Errorf("fresh runner should report no models, got %v", got)
	}
	r.recordModel("deepseek-v4-pro")
	r.recordModel("deepseek-v4-pro")
	r.recordModel("seed-2.1-turbo")
	r.recordModel("") // empty alias (single-model / non-routing) is ignored

	got := r.ModelsUsed()
	if len(got) != 2 || got["deepseek-v4-pro"] != 2 || got["seed-2.1-turbo"] != 1 {
		t.Errorf("ModelsUsed deduped counts wrong: %v", got)
	}
	// returned map is a copy — mutating it must not affect the runner
	got["deepseek-v4-pro"] = 99
	if r.ModelsUsed()["deepseek-v4-pro"] != 2 {
		t.Error("ModelsUsed must return a copy, not the internal map")
	}
}

func TestExecuteToolCall_CodeCommentKeepsScopeMemberPath(t *testing.T) {
	collector := tool.NewCommentCollector()
	reg := tool.NewRegistry()
	reg.Register(&tool.CodeCommentProvider{Collector: collector})
	reg.Freeze()

	r := NewRunner(Deps{Tools: reg, CommentCollector: collector})

	// A call-chain scope spans two files; a comment on the SECOND member
	// must keep its path instead of being re-anchored to the first.
	args, err := json.Marshal(map[string]any{
		"path": "b.go",
		"comments": []any{
			map[string]any{"content": "issue", "existing_code": "foo"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cp := r.executeToolCall(context.Background(), session.Scope{ID: "chain", Kind: "unit", Type: "callchain", Paths: []string{"a.go", "b.go"}}, llm.ToolCall{
		Function: llm.FunctionCall{Name: "code_comment", Arguments: string(args)},
	}, nil, "")
	if cp.Data != tool.CommentSucceed {
		t.Fatalf("unexpected result: %+v", cp)
	}
	comments := collector.Comments()
	if len(comments) != 1 || comments[0].Path != "b.go" {
		t.Fatalf("member path must be kept, got %+v", comments)
	}
}

func TestExtractFacts_ToolSpecificPathArgs(t *testing.T) {
	sc := session.Scope{ID: "u1", Kind: "unit", Type: "file", Paths: []string{"main.go", "b.go"}}
	calls := []llm.ToolCall{
		{Function: llm.FunctionCall{Name: "file_read",
			Arguments: `{"file_path":"a.go","start_line":3,"end_line":9}`}},
		// The common shape: code_comment's raw arguments carry NO path (the
		// anchor path is injected post-parse by executeToolCall) — the flag
		// fact must anchor to the scope's representative path, not vanish
		// (regression: zero flag facts ever reached the board).
		{Function: llm.FunctionCall{Name: "code_comment",
			Arguments: `{"comments":[{"content":"issue","existing_code":"x"}]}`}},
		// A raw path that IS a scope member is kept (multi-file units comment
		// beyond the representative path).
		{Function: llm.FunctionCall{Name: "code_comment",
			Arguments: `{"path":"b.go","comments":[]}`}},
		// A hallucinated non-member path snaps to the representative path,
		// mirroring executeToolCall's rule.
		{Function: llm.FunctionCall{Name: "code_comment",
			Arguments: `{"path":"elsewhere.go","comments":[]}`}},
	}
	facts := extractFacts(sc, 2, calls)
	if len(facts) != 4 {
		t.Fatalf("want 4 facts, got %d: %+v", len(facts), facts)
	}
	if facts[0].Text != "read a.go:3-9" || facts[0].Paths[0] != "a.go" {
		t.Fatalf("unexpected read fact: %+v", facts[0])
	}
	for i, want := range map[int]string{1: "main.go", 2: "b.go", 3: "main.go"} {
		if facts[i].Paths[0] != want || facts[i].Text != "flagged an issue in "+want {
			t.Fatalf("flag fact %d: want path %s, got %+v", i, want, facts[i])
		}
	}
}

func TestHandlePostBulletin(t *testing.T) {
	b := board.New()
	r := NewRunner(Deps{Board: b, PostBulletinEnabled: true})
	budget := 2

	call := func(argsJSON string) (string, bool) {
		return r.handlePostBulletin("u1", 4, llm.ToolCall{
			Function: llm.FunctionCall{Name: "post_bulletin", Arguments: argsJSON},
		}, &budget)
	}

	if res, posted := call(`{"text":"","paths":["a.go"]}`); posted || !strings.Contains(res, "non-empty text") {
		t.Fatalf("empty text must be rejected: %q", res)
	}
	if res, posted := call(`{"text":"suspicion"}`); posted || !strings.Contains(res, "routing key") {
		t.Fatalf("missing routing keys must be rejected: %q", res)
	}
	if _, posted := call(`{"text":"port 8080 here — does the probe config match?","paths":["deploy/probe.yaml"]}`); !posted {
		t.Fatal("valid bulletin must be posted")
	}
	if _, posted := call(`{"text":"another","symbols":["pkg.Fn"]}`); !posted {
		t.Fatal("symbol-only routing must be accepted")
	}
	if res, posted := call(`{"text":"over budget","paths":["a.go"]}`); posted || !strings.Contains(res, "budget") {
		t.Fatalf("budget exhaustion must refuse the post: %q", res)
	}

	posts := b.Posted()
	if len(posts) != 2 {
		t.Fatalf("want 2 published bulletins, got %d", len(posts))
	}
	if posts[0].Level != board.LevelObservation || posts[0].From != "u1" || posts[0].Turn != 4 {
		t.Fatalf("bulletin must be an observation from the posting scope: %+v", posts[0])
	}
}

func TestNewRunner_StripsPostBulletinDefWithoutBoard(t *testing.T) {
	defs := []llm.ToolDef{
		{Type: "function", Function: llm.FunctionDef{Name: "file_read"}},
		{Type: "function", Function: llm.FunctionDef{Name: "post_bulletin"}},
	}
	for _, tc := range []struct {
		name string
		deps Deps
		want int
	}{
		{"no board", Deps{MainToolDefs: defs, PostBulletinEnabled: true}, 1},
		{"gate off", Deps{MainToolDefs: defs, Board: board.New()}, 1},
		{"board and gate", Deps{MainToolDefs: defs, Board: board.New(), PostBulletinEnabled: true}, 2},
	} {
		r := NewRunner(tc.deps)
		if got := len(r.deps.MainToolDefs); got != tc.want {
			t.Fatalf("%s: want %d tool defs, got %d", tc.name, tc.want, got)
		}
	}
}

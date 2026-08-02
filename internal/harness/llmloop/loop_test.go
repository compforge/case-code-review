package llmloop

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
)

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

func TestExtractFacts_ToolSpecificPathArgs(t *testing.T) {
	sc := session.Scope{ID: "u1", Kind: "unit", Type: "file", Paths: []string{"main.go", "b.go"}}
	calls := []llm.ToolCall{
		{Function: llm.FunctionCall{Name: "file_read",
			Arguments: `{"reads":[{"file_path":"a.go","start_line":3,"end_line":9}]}`}},
	}
	facts := extractFacts(sc, 2, calls)
	if len(facts) != 1 {
		t.Fatalf("want 1 fact, got %d: %+v", len(facts), facts)
	}
	if facts[0].Text != "read 1 file range(s): a.go" || facts[0].Paths[0] != "a.go" {
		t.Fatalf("unexpected read fact: %+v", facts[0])
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

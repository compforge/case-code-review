package finding

import (
	"context"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/llmloop"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestHookAnchorsFindingToScope(t *testing.T) {
	collector := NewCollector()
	hook := &Hook{Collector: collector}
	result, handled := hook.Handle(context.Background(), llmloop.HookCall{
		Scope: session.Scope{ID: "correct.go", Kind: "file", Type: "file", Paths: []string{"correct.go"}},
		Tool:  CodeComment,
		Call:  llm.ToolCall{Function: llm.FunctionCall{Name: "code_comment"}},
		Args: map[string]any{
			"path": "wrong.go",
			"comments": []any{
				map[string]any{"content": "issue", "existing_code": "foo"},
			},
		},
	})
	if !handled || result.Data != submitSucceeded {
		t.Fatalf("unexpected hook result: handled=%v result=%+v", handled, result)
	}
	comments := collector.Comments()
	if len(comments) != 1 || comments[0].Path != "correct.go" {
		t.Fatalf("finding must anchor to scope, got %+v", comments)
	}
}

func TestHookKeepsMemberPath(t *testing.T) {
	collector := NewCollector()
	hook := &Hook{Collector: collector}
	_, handled := hook.Handle(context.Background(), llmloop.HookCall{
		Scope: session.Scope{ID: "chain", Kind: "unit", Type: "callchain", Paths: []string{"a.go", "b.go"}},
		Tool:  CodeComment,
		Call:  llm.ToolCall{Function: llm.FunctionCall{Name: "code_comment"}},
		Args: map[string]any{
			"path": "b.go",
			"comments": []any{
				map[string]any{"content": "issue", "existing_code": "foo"},
			},
		},
	})
	if !handled {
		t.Fatal("code_comment was not handled")
	}
	comments := collector.Comments()
	if len(comments) != 1 || comments[0].Path != "b.go" {
		t.Fatalf("member path must be kept, got %+v", comments)
	}
}

func TestHookFactsAnchorToScopeMembers(t *testing.T) {
	hook := &Hook{}
	scope := session.Scope{ID: "chain", Paths: []string{"a.go", "b.go"}}
	facts := hook.Facts(scope, 2, []llm.ToolCall{
		{Function: llm.FunctionCall{Name: "code_comment", Arguments: `{}`}},
		{Function: llm.FunctionCall{Name: "code_comment", Arguments: `{"path":"b.go"}`}},
		{Function: llm.FunctionCall{Name: "code_comment", Arguments: `{"path":"elsewhere.go"}`}},
	})
	if len(facts) != 3 {
		t.Fatalf("want 3 facts, got %d: %+v", len(facts), facts)
	}
	for i, want := range []string{"a.go", "b.go", "a.go"} {
		if facts[i].Paths[0] != want {
			t.Fatalf("fact %d: want path %s, got %+v", i, want, facts[i])
		}
	}
}

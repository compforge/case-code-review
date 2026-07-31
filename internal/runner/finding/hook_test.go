package finding

import (
	"context"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestHookAnchorsFindingToScope(t *testing.T) {
	collector := NewCollector()
	hook := &Hook{Collector: collector}
	result, handled := hook.HandleTool(context.Background(), harness.ToolRequest{
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
	_, handled := hook.HandleTool(context.Background(), harness.ToolRequest{
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

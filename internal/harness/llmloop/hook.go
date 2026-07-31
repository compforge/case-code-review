package llmloop

import (
	"context"

	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// HookCall is the Harness-level view of one validated and decoded tool call.
// It intentionally carries no review-domain types.
type HookCall struct {
	Scope  session.Scope
	Tool   tool.Tool
	Call   llm.ToolCall
	Args   map[string]any
	Record *session.TaskRecord
	Alias  string
}

// ToolCallHook extends generic tool dispatch with domain semantics.
type ToolCallHook interface {
	Handle(context.Context, HookCall) (tool.TaskCheckpoint, bool)
}

// ToolFactHook optionally derives confirmed board facts from domain tool
// calls without teaching the Harness their argument schema.
type ToolFactHook interface {
	Facts(session.Scope, int, []llm.ToolCall) []board.Bulletin
}

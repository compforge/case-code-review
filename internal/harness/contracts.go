package harness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// ToolRequest is the Harness-level view of one validated tool call. It keeps
// agentcore's call type inside the adapter while giving domain extensions the
// scope and served-model identity they need.
type ToolRequest struct {
	Scope session.Scope
	Tool  tool.Tool
	Call  llm.ToolCall
	Args  map[string]any
	Alias string
}

// ToolHandler lets a domain extension handle a tool without teaching the
// execution kernel its argument or result semantics.
type ToolHandler interface {
	HandleTool(context.Context, ToolRequest) (tool.TaskCheckpoint, bool)
}

// TurnContextProvider supplies incremental context at model-turn boundaries.
// The provider owns the source semantics (for example, a Review Team board);
// Harness only commits the returned typed messages into the conversation.
type TurnContextProvider interface {
	PullTurnContext(context.Context, session.Scope) []msg.Msg
}

type ExecutionEventType string

const (
	EventModelResponse  ExecutionEventType = "model_response"
	EventToolStart      ExecutionEventType = "tool_start"
	EventToolEnd        ExecutionEventType = "tool_end"
	EventExecutionEnd   ExecutionEventType = "execution_end"
	EventExecutionError ExecutionEventType = "execution_error"
)

// ExecutionEvent is the agentcore-free event stream exposed by Harness.
// Consumers may persist, render, or measure it without depending on the
// execution library's event schema.
type ExecutionEvent struct {
	Type       ExecutionEventType
	Message    *llm.Message
	Usage      *llm.UsageInfo
	ToolCallID string
	Tool       string
	Arguments  json.RawMessage
	Result     json.RawMessage
	IsError    bool
	Alias      string
	Model      string
	Duration   time.Duration
	EndReason  string
	Err        error
}

// EventSink receives ordered execution events synchronously.
type EventSink interface {
	OnExecutionEvent(ExecutionEvent)
}

type EventSinkFunc func(ExecutionEvent)

func (f EventSinkFunc) OnExecutionEvent(event ExecutionEvent) { f(event) }

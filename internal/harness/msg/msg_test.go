package msg

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestWrapCreatesAgentMessages(t *testing.T) {
	wire := []llm.Message{
		llm.NewTextMessage("system", "you are a reviewer"),
		llm.NewTextMessage("user", "review this"),
		llm.NewToolCallMessage("thinking…", []llm.ToolCall{
			{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "search_code", Arguments: `{"q":"x"}`}},
		}),
		llm.NewToolResultMessage("c1", "3 hits"),
		llm.NewTextMessage("assistant", "done"),
	}
	got := Wrap(wire)
	if len(got) != len(wire) {
		t.Fatal("conversion must remain 1:1")
	}
	toolCall, _ := got[2].ToMessage()
	if calls := toolCall.ToolCalls(); len(calls) != 1 || calls[0].Name != "search_code" {
		t.Fatalf("tool call conversion = %#v", calls)
	}
	toolResult, _ := got[3].ToMessage()
	if toolResult.Role != "tool" || toolResult.Metadata["tool_call_id"] != "c1" {
		t.Fatalf("tool result conversion = %#v", toolResult)
	}
}

func TestText(t *testing.T) {
	m, _ := Text("user", "hi").ToMessage()
	if m.Role != "user" || m.TextContent() != "hi" {
		t.Fatalf("Text lowered wrong: %+v", m)
	}
}

package msg

import (
	"encoding/json"
	"testing"

	"github.com/compforge/agentgo"
)

func TestWrapCreatesAgentMessages(t *testing.T) {
	wire := []agentgo.Message{{Role: agentgo.RoleSystem, Content: []agentgo.ContentBlock{agentgo.TextBlock("you are a reviewer")}}, {Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("review this")}}, {Role: agentgo.RoleAssistant, Content: append([]agentgo.ContentBlock{agentgo.TextBlock("thinking…")}, agentgo.ToolCallBlock(agentgo.ToolCall{ID: "c1", Name: "search_code", Args: json.RawMessage(`{"q":"x"}`)}))}, {Role: agentgo.RoleTool, Content: []agentgo.ContentBlock{agentgo.TextBlock("3 hits")}, Metadata: map[string]any{"tool_call_id": "c1"}}, {Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.TextBlock("done")}}}
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

package msg

import (
	"reflect"
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

// The currency swap's byte-identical guarantee: lifting wire messages into the
// domain and lowering them back is the identity, for every wire shape the loop
// produces (text, tool-call, tool-result).
func TestWrapLowerRoundTrip(t *testing.T) {
	wire := []llm.Message{
		llm.NewTextMessage("system", "you are a reviewer"),
		llm.NewTextMessage("user", "review this"),
		llm.NewToolCallMessage("thinking…", []llm.ToolCall{
			{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "code_search", Arguments: `{"q":"x"}`}},
		}),
		llm.NewToolResultMessage("c1", "3 hits"),
		llm.NewTextMessage("assistant", "done"),
	}
	got := Lower(Wrap(wire))
	if !reflect.DeepEqual(got, wire) {
		t.Fatalf("round trip not identity:\n got %+v\nwant %+v", got, wire)
	}
	// The 1:1 invariant compression's index arithmetic depends on.
	if len(Wrap(wire)) != len(wire) || len(got) != len(wire) {
		t.Fatal("lowering must be 1:1")
	}
}

func TestText(t *testing.T) {
	m := Text("user", "hi").Lower()
	if m.Role != "user" || m.ExtractText() != "hi" {
		t.Fatalf("Text lowered wrong: %+v", m)
	}
}

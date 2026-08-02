package harness

import (
	"encoding/json"
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestToAgentGoResponseCanonicalizesSingletonBatchTools(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		arguments string
		batchKey  string
	}{
		{
			name: "read files", tool: "read_files", batchKey: "reads",
			arguments: `{"file_path":"a.go","start_line":10,"end_line":20}`,
		},
		{
			name: "read baseline", tool: "read_base_files", batchKey: "reads",
			arguments: `{"file_path":"a.go"}`,
		},
		{
			name: "search code", tool: "search_code", batchKey: "searches",
			arguments: `{"query":"Target","case_sensitive":true}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message, err := toAgentGoResponse(toolResponse(tt.tool, tt.arguments))
			if err != nil {
				t.Fatal(err)
			}
			calls := message.ToolCalls()
			if len(calls) != 1 || calls[0].ArgsInvalid {
				t.Fatalf("calls = %+v", calls)
			}
			var args map[string][]map[string]any
			if err := json.Unmarshal(calls[0].Args, &args); err != nil {
				t.Fatal(err)
			}
			if len(args[tt.batchKey]) != 1 {
				t.Fatalf("canonical args = %s", calls[0].Args)
			}
		})
	}
}

func TestToAgentGoResponseLeavesMalformedArgumentsObservable(t *testing.T) {
	raw := `{"searches":[{"query":"Target"}]` // missing closing brace
	message, err := toAgentGoResponse(toolResponse("search_code", raw))
	if err != nil {
		t.Fatal(err)
	}
	call := message.ToolCalls()[0]
	if !call.ArgsInvalid || call.ArgsRawText != raw {
		t.Fatalf("malformed call was hidden: %+v", call)
	}
}

func toolResponse(name, arguments string) *llm.ChatResponse {
	return &llm.ChatResponse{Choices: []llm.Choice{{
		Message: llm.ResponseMessage{ToolCalls: []llm.ToolCall{{
			ID: "call-1", Type: "function",
			Function: llm.FunctionCall{Name: name, Arguments: arguments},
		}}},
	}}}
}

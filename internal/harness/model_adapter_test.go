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

func TestToAgentGoResponseCanonicalizesKnownToolArgumentDrift(t *testing.T) {
	t.Run("read object", func(t *testing.T) {
		args := canonicalResponseArgs(t, "read_files", `{"reads":{"file_path":"a.go","start_line":7}}`)
		reads := args["reads"].([]any)
		item := reads[0].(map[string]any)
		if item["file_path"] != "a.go" || item["start_line"] != float64(7) {
			t.Fatalf("canonical args = %#v", args)
		}
	})

	t.Run("query list with shared options", func(t *testing.T) {
		args := canonicalResponseArgs(t, "search_code", `{
			"queries":["thread_comment","resolve_thread"],
			"file_patterns":["*.py"],
			"syntax":"literal"
		}`)
		searches := args["searches"].([]any)
		if len(searches) != 2 {
			t.Fatalf("canonical args = %#v", args)
		}
		for _, value := range searches {
			item := value.(map[string]any)
			if item["syntax"] != "literal" || item["file_patterns"] == nil {
				t.Fatalf("canonical search = %#v", item)
			}
			if _, exists := item["use_perl_regexp"]; exists {
				t.Fatalf("legacy regexp flag was retained: %#v", item)
			}
		}
	})

	t.Run("unambiguous pattern alias", func(t *testing.T) {
		args := canonicalResponseArgs(t, "search_code", `{
			"searches":[{"pattern":"SessionMessage","syntax":"literal","contextAround":3,"outputMode":"content"}]
		}`)
		item := args["searches"].([]any)[0].(map[string]any)
		if item["query"] != "SessionMessage" || item["syntax"] != "literal" {
			t.Fatalf("canonical search = %#v", item)
		}
		if _, ok := item["pattern"]; ok {
			t.Fatalf("pattern alias was retained: %#v", item)
		}
		if _, ok := item["contextAround"]; ok {
			t.Fatalf("foreign presentation option was retained: %#v", item)
		}
	})

	t.Run("regexp aliases normalize to current contract", func(t *testing.T) {
		args := canonicalResponseArgs(t, "search_code", `{
			"searches":[{"query":"export.*Comment","syntax":"regex"}]
		}`)
		item := args["searches"].([]any)[0].(map[string]any)
		if item["syntax"] != "regexp" {
			t.Fatalf("canonical search = %#v", item)
		}
	})

	t.Run("ambiguous escaped literal remains invalid", func(t *testing.T) {
		args := canonicalResponseArgs(t, "search_code", `{
			"searches":[{"pattern":"\\.inspect\\(","syntax":"literal"}]
		}`)
		item := args["searches"].([]any)[0].(map[string]any)
		if _, ok := item["query"]; ok || item["pattern"] != `\.inspect\(` {
			t.Fatalf("ambiguous pattern was guessed: %#v", item)
		}
	})
}

func TestToAgentGoResponseLeavesMalformedArgumentsObservable(t *testing.T) {
	raw := `{"searches":[{"query":"Target","syntax":"literal"}]` // missing closing brace
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

func canonicalResponseArgs(t *testing.T, name, arguments string) map[string]any {
	t.Helper()
	message, err := toAgentGoResponse(toolResponse(name, arguments))
	if err != nil {
		t.Fatal(err)
	}
	calls := message.ToolCalls()
	if len(calls) != 1 || calls[0].ArgsInvalid {
		t.Fatalf("calls = %+v", calls)
	}
	var args map[string]any
	if err := json.Unmarshal(calls[0].Args, &args); err != nil {
		t.Fatal(err)
	}
	return args
}

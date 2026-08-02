package msg

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func TestSearchBatchKeepsOnePairingAndTypedMembers(t *testing.T) {
	content := tool.EncodeCodeSearchResults([]string{
		"File: a.go\nMatch lines: 1\n10|func Alpha()\n",
		"No matches found",
		"Error: invalid regular expression",
	})
	message := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{
			map[string]any{"query": "Alpha"},
			map[string]any{"query": "Missing"},
			map[string]any{"query": "(bad", "use_perl_regexp": true},
		}},
		Content: content,
	})
	batch, ok := message.(*SearchBatch)
	if !ok || len(batch.Results()) != 2 {
		t.Fatalf("batch promotion = %#v", message)
	}
	if batch.Results()[0].Query != "Alpha" || batch.Results()[1].Query != "Missing" {
		t.Fatalf("search order = %#v", batch.Results())
	}
	wire := batch.ToLLM(CompactionReference)
	text := wire.ExtractText()
	if wire.ToolCallID != "search-1" ||
		!strings.Contains(text, `search_code "Missing" returned no matches`) ||
		!strings.Contains(text, "invalid regular expression") {
		t.Fatalf("batch lowering lost pairing/result: %+v", wire)
	}
}

func TestCloneAllCopiesSearchBatch(t *testing.T) {
	message := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{map[string]any{"query": "Alpha"}}},
		Content: tool.EncodeCodeSearchResults([]string{
			"File: a.go\nMatch lines: 1\n10|func Alpha()\n",
		}),
	}).(*SearchBatch)
	cloned := CloneAll([]Msg{message})[0].(*SearchBatch)
	if cloned == message || cloned.Results()[0] == message.Results()[0] {
		t.Fatal("CloneAll shared SearchBatch state")
	}
}

package msg

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func TestSearchBatchKeepsOnePairingAndTypedMembers(t *testing.T) {
	content := tool.EncodeCodeSearchResults([]string{
		"File: a.go\nMatch lines: 1\n10|func Alpha()\n",
		"Search outcome: {\"status\":\"no_matches\",\"query_mode\":\"literal\",\"searched_files\":3}\nNo matches found",
		"Error: invalid regular expression",
	})
	message := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{
			map[string]any{"query": "Alpha", "syntax": "literal"},
			map[string]any{"query": "Missing", "syntax": "literal"},
			map[string]any{"query": "(bad", "syntax": "regexp"},
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
	projected, _ := batch.Compact(0)
	wire := projected.(*SearchBatch).ToLLM()
	text := wire.ExtractText()
	if wire.ToolCallID != "search-1" ||
		!strings.Contains(text, `search_code "Missing" returned no matches across 3 scoped files`) ||
		!strings.Contains(text, "invalid regular expression") {
		t.Fatalf("batch lowering lost pairing/result: %+v", wire)
	}
}

func TestRawCopiesSearchBatch(t *testing.T) {
	message := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{map[string]any{"query": "Alpha", "syntax": "literal"}}},
		Content: tool.EncodeCodeSearchResults([]string{
			"File: a.go\nMatch lines: 1\n10|func Alpha()\n",
		}),
	}).(*SearchBatch)
	cloned := message.Raw().(*SearchBatch)
	if cloned == message || cloned.Results()[0] == message.Results()[0] {
		t.Fatal("Raw shared SearchBatch state")
	}
}

func TestSearchBatchRetainsEmptyScopeWarningAfterCompaction(t *testing.T) {
	message := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{map[string]any{
			"query": "Alpha", "syntax": "literal", "file_patterns": []any{"missing/**"},
		}}},
		Content: tool.EncodeCodeSearchResults([]string{
			"Search outcome: {\"status\":\"scope_empty\",\"query_mode\":\"literal\",\"searched_files\":0}\nNo files matched file_patterns",
		}),
	}).(*SearchBatch)

	projected, _ := message.Compact(0)
	wire := projected.(*SearchBatch).ToLLM()
	if text := wire.ExtractText(); !strings.Contains(text, "searched no files because its path scope was empty") {
		t.Fatalf("compacted result lost scope warning: %s", text)
	}
}

func TestSearchBatchCondensesSymbolSourceToAnchors(t *testing.T) {
	message := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{map[string]any{"query": "Alpha"}}},
		Content: tool.EncodeCodeSearchResults([]string{
			"File: a.go\nMatch lines: 1\n10|func Alpha()\n" +
				`Symbol context: {"status":"expanded","hit_count":1,"resolved_hits":1,"candidate_count":1}` + "\n" +
				`Symbol: {"symbol_id":"a.go::Alpha","signature":"func Alpha()","path":"a.go","start_line":9,"end_line":11}` + "\n" +
				`Symbol source: {"path":"a.go","start_line":9,"end_line":11,"total_lines":20}` + "\n" +
				"9|// Alpha\n10|func Alpha()\n11|}\n",
		}),
	}).(*SearchBatch)

	projected, _ := message.Compact(0.8)
	wire := projected.(*SearchBatch).ToLLM()
	text := wire.ExtractText()
	if !strings.Contains(text, "matched symbol candidates") ||
		!strings.Contains(text, "func Alpha() — a.go:L9-L11") ||
		strings.Contains(text, "Symbol source:") {
		t.Fatalf("condensed symbol search = %q", text)
	}
}

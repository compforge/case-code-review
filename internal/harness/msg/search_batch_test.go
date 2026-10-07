package msg

import (
	"strings"
	"testing"

	"github.com/compforge/agentgo"
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

func TestSearchBatchSharedSourceRoundTripAndCompaction(t *testing.T) {
	content := tool.MergeCodeSearchResults([]string{
		"File: a.go\nMatch lines: 1\n10|func Alpha() {}\n" +
			`Symbol source: {"path":"a.go","start_line":10,"end_line":10,"total_lines":10}` + "\n10|func Alpha() {}",
		"Error: invalid regex",
	})
	message := FromLLM(LLMToolResult{Tool: CodeSearchToolName, ToolCallID: "search-shared",
		Arguments: map[string]any{"searches": []any{map[string]any{"query": "Alpha"}, map[string]any{"query": "("}}}, Content: content})
	batch, ok := message.(*SearchBatch)
	if !ok || batch.ToLLM().ToolCallID != "search-shared" || batch.TextContent() != content {
		t.Fatalf("roundtrip: %v", message)
	}
	if len(tool.CodeSearchSourceRanges(batch.TextContent())) != 1 {
		t.Fatal("lost visible source")
	}
	compacted, _ := batch.Compact(0)
	if len(tool.CodeSearchSourceRanges(compacted.TextContent())) != 0 || strings.Contains(compacted.TextContent(), "10|func") {
		t.Fatal("compacted source still advertised")
	}
	if compacted.Raw().TextContent() != content {
		t.Fatal("Raw lost shared source")
	}
}

func TestOversizedToolResultsAreBoundedReceipts(t *testing.T) {
	for _, name := range []string{"read_files", "read_base_files", "read_diffs", "file_find", "custom_tool"} {
		message := FromLLM(LLMToolResult{Tool: name, ToolCallID: "large",
			Content: "FILE_PATH: a.go\nLINE_RANGE: 1-1\nTOTAL_LINES: 1\n1|" + strings.Repeat("中", tool.MaxResultBytes)})
		raw := message
		message = LimitToolMessages([]agentgo.AgentMessage{message})[0]
		receipt, ok := message.(*ToolView)
		if !ok {
			t.Fatalf("%s: clipped result promoted to %T", name, message)
		}
		if len(message.TextContent()) > tool.MaxResultBytes || message.Raw().TextContent() != raw.TextContent() || receipt.ToolName() != name {
			t.Fatalf("%s: budget/pairing lost", name)
		}
	}
}

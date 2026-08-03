package msg

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func TestFromLLMToolResultFamilies(t *testing.T) {
	search := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{map[string]any{"query": "NewExecution", "syntax": "literal"}}},
		Content: tool.EncodeCodeSearchResults([]string{
			"File: internal/harness/execution.go\nMatch lines: 2\n10|func NewExecution\n20|NewExecution(spec)\n",
		}),
	})
	searchBatch, ok := search.(*SearchBatch)
	if !ok || len(searchBatch.Results()) != 1 || searchBatch.Results()[0].Query != "NewExecution" {
		t.Fatalf("search result = %#v", search)
	}
	condensed := searchBatch.ToLLM(CompactionCondensed)
	if condensed.ToolCallID != "search-1" || !strings.Contains(condensed.ExtractText(), "2 hits") {
		t.Fatalf("condensed search = %+v", condensed)
	}

	diff := FromLLM(LLMToolResult{
		Tool: FileReadDiffToolName, ToolCallID: "diff-1",
		Arguments: map[string]any{"paths": []any{"b.go", "a.go"}},
		Content:   "==== FILE: a.go ====\ndiff --git a/a.go b/a.go\n@@ -1 +1 @@\n-old\n+new\n",
	})
	diffResult, ok := diff.(*Diff)
	if !ok || len(diffResult.Paths) != 2 {
		t.Fatalf("diff result = %#v", diff)
	}
	diffReference := diffResult.ToLLM(CompactionReference)
	if text := diffReference.ExtractText(); !strings.Contains(text, "a.go, b.go") {
		t.Fatalf("diff reference = %q", text)
	}

	find := FromLLM(LLMToolResult{
		Tool: FileFindToolName, ToolCallID: "find-1",
		Arguments: map[string]any{"query_name": "missing"},
		Content:   "// The file was not found",
	})
	findResult, ok := find.(*SearchResult)
	if !ok {
		t.Fatalf("file_find result = %#v", find)
	}
	findCondensed := findResult.ToLLM(CompactionCondensed)
	if text := findCondensed.ExtractText(); !strings.Contains(text, "had no matches") {
		t.Fatalf("file_find miss = %q", text)
	}

	receipt := FromLLM(LLMToolResult{
		Tool: "submit_hypotheses", ToolCallID: "result-1", Content: "Hypotheses submitted.",
	})
	if _, ok := receipt.(*ToolReceipt); !ok {
		t.Fatalf("result tool should become ToolReceipt: %T", receipt)
	}
}

func TestFromLLMToolErrorStaysReceipt(t *testing.T) {
	decoded := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"query": "x"}, Content: "Error: invalid pattern",
	})
	if _, ok := decoded.(*ToolReceipt); !ok {
		t.Fatalf("recoverable error should not become a compactable search: %T", decoded)
	}

	decoded = FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-2", IsError: true,
		Arguments: map[string]any{"query": "x"}, Content: "invalid regular expression",
	})
	if _, ok := decoded.(*ToolReceipt); !ok {
		t.Fatalf("wire error metadata should take precedence over result wording: %T", decoded)
	}

	decoded = FromLLM(LLMToolResult{
		Tool: FileReadToolName, ToolCallID: "read-1", IsError: true,
		Content: "File: a.go (Total lines: 1)\nIS_TRUNCATED: false\nLINE_RANGE: 1-1\n1|bad\n",
	})
	if _, ok := decoded.(*ToolReceipt); !ok {
		t.Fatalf("wire error metadata should take precedence over a file-shaped payload: %T", decoded)
	}
}

func TestFromLLMBareCodeSearchResultStaysReceipt(t *testing.T) {
	decoded := FromLLM(LLMToolResult{
		Tool: CodeSearchToolName, ToolCallID: "search-1",
		Arguments: map[string]any{"searches": []any{map[string]any{"query": "x", "syntax": "literal"}}},
		Content:   "File: a.go\nMatch lines: 1\n1|x\n",
	})
	if _, ok := decoded.(*ToolReceipt); !ok {
		t.Fatalf("bare search_code result should expose a broken batch envelope: %T", decoded)
	}
}

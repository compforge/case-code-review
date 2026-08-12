package msg

import (
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// SearchBatch keeps search_code's one call/result pairing while exposing each
// query as a typed SearchResult for independent compaction and diagnostics.
type SearchBatch struct {
	items          []searchBatchItem
	toolCallID     string
	representation searchBatchRepresentation
}

type searchBatchRepresentation uint8

const (
	searchBatchFull searchBatchRepresentation = iota
	searchBatchCondensed
	searchBatchReference
)

type searchBatchItem struct {
	result *SearchResult
	raw    string
}

func (b *SearchBatch) FromLLM(result LLMToolResult) bool {
	if result.IsError || result.Tool != CodeSearchToolName {
		return false
	}
	requests, err := tool.ParseCodeSearchRequests(result.Arguments)
	if err != nil {
		return false
	}
	parts, ok := tool.DecodeCodeSearchResults(result.Content)
	if !ok || len(parts) != len(requests) {
		return false
	}
	items := make([]searchBatchItem, len(parts))
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" || strings.HasPrefix(trimmed, "Error:") ||
			strings.HasPrefix(trimmed, "search_code timed out") {
			items[i].raw = part
			continue
		}
		items[i].result = &SearchResult{
			Tool:      CodeSearchToolName,
			Query:     requests[i].SearchText,
			Content:   part,
			NoMatches: searchHadNoMatches(part),
		}
		if outcome, ok := tool.ParseCodeSearchOutcome(part); ok {
			items[i].result.NoMatches = true
			items[i].result.SearchStatus = outcome.Status
			items[i].result.SearchedFiles = outcome.SearchedFiles
		}
	}
	*b = SearchBatch{items: items, toolCallID: result.ToolCallID}
	return true
}

func (b *SearchBatch) ToLLM() llm.Message { return b.render(b.representation) }

func (b *SearchBatch) render(representation searchBatchRepresentation) llm.Message {
	parts := make([]string, len(b.items))
	for i, item := range b.items {
		if item.result != nil {
			parts[i] = item.result.render(searchRepresentation(representation))
		} else {
			parts[i] = item.raw
		}
	}
	return llm.NewToolResultMessage(b.toolCallID, tool.EncodeCodeSearchResults(parts))
}

func (b *SearchBatch) ToolName() string { return CodeSearchToolName }

func (b *SearchBatch) Priority() int { return 0 }

func (b *SearchBatch) Compact(expect float64) (Msg, float64) {
	next := b.clone()
	next.representation, expect = compactRepresentation(expect, b.representation, searchBatchReference, next.render)
	return next, expect
}

// Results returns successful typed members in request order. Item-local
// errors remain in the wire batch but are not evidence-bearing results.
func (b *SearchBatch) Results() []*SearchResult {
	results := make([]*SearchResult, 0, len(b.items))
	for _, item := range b.items {
		if item.result != nil {
			results = append(results, item.result)
		}
	}
	return results
}

func (b *SearchBatch) clone() *SearchBatch {
	copyBatch := &SearchBatch{toolCallID: b.toolCallID, representation: b.representation, items: make([]searchBatchItem, len(b.items))}
	for i, item := range b.items {
		copyBatch.items[i].raw = item.raw
		if item.result != nil {
			result := *item.result
			result.Paths = append([]string(nil), item.result.Paths...)
			if item.result.SearchedFiles != nil {
				count := *item.result.SearchedFiles
				result.SearchedFiles = &count
			}
			copyBatch.items[i].result = &result
		}
	}
	return copyBatch
}

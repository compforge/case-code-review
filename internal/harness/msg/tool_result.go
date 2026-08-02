package msg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/qiankunli/case-code-review/internal/llm"
)

const (
	CodeSearchToolName   = "search_code"
	FileFindToolName     = "file_find"
	FileReadDiffToolName = "file_read_diff"
)

// LLMToolResult is the wire evidence needed to restore one typed result. The
// invocation arguments travel with the result because some identities (query,
// requested paths) are present only on the preceding assistant tool call.
type LLMToolResult struct {
	Tool       string
	ToolCallID string
	Arguments  map[string]any
	Content    string
	IsError    bool
}

func (r LLMToolResult) failed() bool {
	return r.IsError || strings.HasPrefix(strings.TrimSpace(r.Content), "Error:")
}

// SearchResult is one content-search member or one file-discovery result. Its
// raw hits can be re-derived, so compact forms retain the query and locations.
type SearchResult struct {
	Tool       string
	Query      string
	Content    string
	ToolCallID string
	NoMatches  bool
}

func (r *SearchResult) FromLLM(result LLMToolResult) bool {
	// search_code is batch-only and is decoded by SearchBatch; accepting a
	// bare successful result here would hide a broken provider envelope.
	if result.failed() || result.Tool != FileFindToolName {
		return false
	}
	*r = SearchResult{
		Tool:       result.Tool,
		Query:      stringArgument(result.Arguments, "query_name"),
		Content:    result.Content,
		ToolCallID: result.ToolCallID,
		NoMatches:  searchHadNoMatches(result.Content),
	}
	return true
}

func (r SearchResult) ToLLM(level CompactionLevel) llm.Message {
	return llm.NewToolResultMessage(r.ToolCallID, r.render(level))
}

func (r SearchResult) render(level CompactionLevel) string {
	text := r.Content
	if r.NoMatches && level >= CompactionReference {
		return fmt.Sprintf("%s %q returned no matches; this negative result is retained after compaction.", r.Tool, r.Query)
	}
	switch level {
	case CompactionCondensed:
		text = r.condensed()
	case CompactionReference:
		text = fmt.Sprintf("%s result for %q compacted to a reference; rerun %s if exact hits are needed.",
			r.Tool, r.Query, r.Tool)
	}
	return text
}

func (r SearchResult) ToolName() string { return r.Tool }

func (r SearchResult) MaxCompaction() CompactionLevel { return CompactionReference }

func (r SearchResult) condensed() string {
	if r.Tool == FileFindToolName {
		if strings.Contains(strings.ToLower(r.Content), "file was not found") {
			return fmt.Sprintf("file_find %q had no matches.", r.Query)
		}
		paths := nonEmptyLines(r.Content)
		if len(paths) == 0 {
			return r.Content
		}
		shown := paths
		if len(shown) > 20 {
			shown = shown[:20]
		}
		return fmt.Sprintf("file_find %q matched %d paths:\n%s", r.Query, len(paths), strings.Join(shown, "\n"))
	}

	var hits []string
	lines := strings.Split(r.Content, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "File: ") {
			continue
		}
		entry := strings.TrimSpace(strings.TrimPrefix(line, "File: "))
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "Match lines: ") {
			entry += " (" + strings.TrimSpace(strings.TrimPrefix(lines[i+1], "Match lines: ")) + " hits)"
		}
		hits = append(hits, entry)
	}
	if len(hits) == 0 {
		return r.Content
	}
	return fmt.Sprintf("search_code %q matched:\n- %s", r.Query, strings.Join(hits, "\n- "))
}

// Diff is a re-readable slice of the reviewed change. Its condensed form keeps
// file and hunk anchors while dropping changed-line bodies.
type Diff struct {
	Paths      []string
	Content    string
	ToolCallID string
}

func (d *Diff) FromLLM(result LLMToolResult) bool {
	if result.failed() || result.Tool != FileReadDiffToolName {
		return false
	}
	*d = Diff{
		Paths:      stringArguments(result.Arguments, "path_array"),
		Content:    result.Content,
		ToolCallID: result.ToolCallID,
	}
	return true
}

func (d Diff) ToLLM(level CompactionLevel) llm.Message {
	text := d.Content
	switch level {
	case CompactionCondensed:
		var anchors []string
		for _, line := range strings.Split(d.Content, "\n") {
			if strings.HasPrefix(line, "==== FILE: ") || strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "@@ ") {
				anchors = append(anchors, line)
			}
		}
		if len(anchors) > 0 {
			text = "Diff anchors retained after compaction:\n" + strings.Join(anchors, "\n")
		}
	case CompactionReference:
		paths := append([]string(nil), d.Paths...)
		sort.Strings(paths)
		text = fmt.Sprintf("Diff for [%s] compacted to a reference; call %s for exact hunks.",
			strings.Join(paths, ", "), FileReadDiffToolName)
	}
	return llm.NewToolResultMessage(d.ToolCallID, text)
}

func (d Diff) ToolName() string { return FileReadDiffToolName }

func (d Diff) MaxCompaction() CompactionLevel { return CompactionReference }

// ToolReceipt is the small protocol acknowledgement for result/terminal tools
// and recoverable tool errors. Domain artifacts live in Runner collectors, so
// duplicating their payload in conversation would create a second truth source.
type ToolReceipt struct {
	Tool       string
	Content    string
	ToolCallID string
}

func (r *ToolReceipt) FromLLM(result LLMToolResult) bool {
	*r = ToolReceipt{Tool: result.Tool, Content: result.Content, ToolCallID: result.ToolCallID}
	return true
}

func (r ToolReceipt) ToLLM(CompactionLevel) llm.Message {
	return llm.NewToolResultMessage(r.ToolCallID, r.Content)
}

func (r ToolReceipt) ToolName() string { return r.Tool }

type llmDecoder interface {
	Msg
	FromLLM(LLMToolResult) bool
}

// FromLLM restores one wire tool result to the first matching message type.
// The dispatcher contains no message-specific parsing; each decoder sits next
// to that type's ToLLM so the two directions evolve together.
func FromLLM(result LLMToolResult) Msg {
	decoders := []llmDecoder{&FileBatch{}, &File{}, &SearchBatch{}, &SearchResult{}, &Diff{}}
	for _, decoder := range decoders {
		if decoder.FromLLM(result) {
			return decoder
		}
	}
	receipt := &ToolReceipt{}
	receipt.FromLLM(result)
	return receipt
}

func searchHadNoMatches(result string) bool {
	lower := strings.ToLower(result)
	return strings.Contains(lower, "no matches found") || strings.Contains(lower, "file was not found")
}

func stringArgument(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func stringArguments(args map[string]any, key string) []string {
	var out []string
	switch values := args[key].(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	case []string:
		out = append(out, values...)
	}
	return out
}

func nonEmptyLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

package msg

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

const (
	CodeSearchToolName   = "search_code"
	FileFindToolName     = "file_find"
	FileReadDiffToolName = "read_diffs"
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
	Timestamp  time.Time
}

func (r LLMToolResult) messageMeta() messageMeta {
	if r.Timestamp.IsZero() {
		return newMessageMeta()
	}
	return messageMeta{timestamp: r.Timestamp}
}

func (r LLMToolResult) failed() bool {
	return r.IsError || strings.HasPrefix(strings.TrimSpace(r.Content), "Error:")
}

// SearchResult is one content-search member or one file-discovery result. Its
// raw hits can be re-derived, so compact forms retain the query and locations.
type SearchResult struct {
	messageMeta
	Tool           string
	Query          string
	Paths          []string
	Content        string
	ToolCallID     string
	NoMatches      bool
	SearchStatus   string
	SearchedFiles  *int
	Label          string
	representation searchRepresentation
	priority       int
}

type searchRepresentation uint8

const (
	searchFull searchRepresentation = iota
	searchCondensed
	searchReference
)

func (r *SearchResult) FromLLM(result LLMToolResult) bool {
	// search_code is batch-only and is decoded by SearchBatch; accepting a
	// bare successful result here would hide a broken provider envelope.
	if result.failed() || result.Tool != FileFindToolName {
		return false
	}
	*r = SearchResult{
		messageMeta: result.messageMeta(),
		Tool:        result.Tool,
		Query:       stringArgument(result.Arguments, "query_name"),
		Content:     result.Content,
		ToolCallID:  result.ToolCallID,
		NoMatches:   searchHadNoMatches(result.Content),
	}
	return true
}

func (r SearchResult) ToLLM() llm.Message {
	text := r.render(r.representation)
	if r.Label != "" {
		text = r.Label + ":\n" + text
	}
	if r.ToolCallID != "" {
		return llm.NewToolResultMessage(r.ToolCallID, text)
	}
	return llm.NewTextMessage("user", text)
}

func (r SearchResult) GetRole() agentgo.Role { return domainRole(r.ToLLM()) }
func (r SearchResult) Raw() agentgo.AgentMessage {
	raw := r
	raw.Paths = append([]string(nil), r.Paths...)
	raw.representation = searchFull
	return raw
}
func (r SearchResult) TextContent() string     { return domainText(r.ToLLM()) }
func (r SearchResult) ThinkingContent() string { return "" }
func (r SearchResult) HasToolCalls() bool      { return domainHasToolCalls(r.ToLLM()) }
func (r SearchResult) ToMessage() (agentgo.Message, bool) {
	return domainToMessage(r.ToLLM(), r.ToolName(), r.GetTimestamp())
}

func (r SearchResult) render(representation searchRepresentation) string {
	text := r.Content
	if r.NoMatches && representation >= searchReference {
		if r.SearchStatus == "scope_empty" {
			return fmt.Sprintf("%s %q searched no files because its path scope was empty; correct the scope before drawing a conclusion.", r.Tool, r.Query)
		}
		if r.SearchStatus == "no_matches" && r.SearchedFiles != nil {
			return fmt.Sprintf("%s %q returned no matches across %d scoped files; this negative result is retained after compaction.", r.Tool, r.Query, *r.SearchedFiles)
		}
		return fmt.Sprintf("%s %q returned no matches; this negative result is retained after compaction.", r.Tool, r.Query)
	}
	switch representation {
	case searchCondensed:
		text = r.condensed()
	case searchReference:
		text = fmt.Sprintf("%s result for %q compacted to a reference; rerun %s if exact hits are needed.",
			r.Tool, r.Query, r.Tool)
	}
	return text
}

func (r SearchResult) ToolName() string { return r.Tool }

func (r SearchResult) Priority() int { return r.priority }

func (r *SearchResult) ConfigurePresentation(label string, priority int) *SearchResult {
	if r.timestamp.IsZero() {
		r.messageMeta = newMessageMeta()
	}
	r.Label = label
	r.priority = priority
	return r
}

func (r SearchResult) Compact(expect float64) (agentgo.AgentMessage, float64) {
	next := r
	next.Paths = append([]string(nil), r.Paths...)
	next.representation, expect = compactRepresentation(expect, r.representation, searchReference, func(representation searchRepresentation) llm.Message {
		view := next
		view.representation = representation
		return view.ToLLM()
	})
	return next, expect
}

func (r SearchResult) condensed() string {
	if len(r.Paths) > 0 {
		return fmt.Sprintf("Search %q matched:\n- %s", r.Query, strings.Join(r.Paths, "\n- "))
	}
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
	if anchors := tool.CodeSearchSymbolAnchors(r.Content); len(anchors) > 0 {
		var symbols []string
		for _, anchor := range anchors {
			label := anchor.SymbolID
			if anchor.Signature != "" {
				label = anchor.Signature
			}
			symbols = append(symbols, fmt.Sprintf("%s — %s:L%d-L%d", label, anchor.Path, anchor.StartLine, anchor.EndLine))
		}
		text := fmt.Sprintf("Search %q matched symbol candidates:\n- %s", r.Query, strings.Join(symbols, "\n- "))
		if len(hits) > 0 {
			text += "\nHit files:\n- " + strings.Join(hits, "\n- ")
		}
		return text
	}
	if len(hits) == 0 {
		return r.Content
	}
	return fmt.Sprintf("search_code %q matched:\n- %s", r.Query, strings.Join(hits, "\n- "))
}

// Diff is a re-readable slice of the reviewed change. Its condensed form keeps
// file and hunk anchors while dropping changed-line bodies.
type Diff struct {
	messageMeta
	Paths          []string
	Content        string
	ToolCallID     string
	Label          string
	sources        []SourceArtifact
	required       bool
	representation diffRepresentation
	priority       int
}

type diffRepresentation uint8

const (
	diffFull diffRepresentation = iota
	diffAnchors
	diffReference
)

func (d *Diff) FromLLM(result LLMToolResult) bool {
	if result.failed() || result.Tool != FileReadDiffToolName {
		return false
	}
	*d = Diff{
		messageMeta: result.messageMeta(),
		Paths:       stringArguments(result.Arguments, "paths"),
		Content:     result.Content,
		ToolCallID:  result.ToolCallID,
	}
	return true
}

func (d Diff) ToLLM() llm.Message { return d.render(d.representation) }

func (d Diff) GetRole() agentgo.Role { return domainRole(d.ToLLM()) }
func (d Diff) Raw() agentgo.AgentMessage {
	raw := d
	raw.Paths = append([]string(nil), d.Paths...)
	raw.representation = diffFull
	return raw
}
func (d Diff) TextContent() string     { return domainText(d.ToLLM()) }
func (d Diff) ThinkingContent() string { return "" }
func (d Diff) HasToolCalls() bool      { return domainHasToolCalls(d.ToLLM()) }
func (d Diff) ToMessage() (agentgo.Message, bool) {
	return domainToMessage(d.ToLLM(), d.ToolName(), d.GetTimestamp())
}

func (d Diff) render(representation diffRepresentation) llm.Message {
	text := d.Content
	switch representation {
	case diffAnchors:
		var anchors []string
		for _, line := range strings.Split(d.Content, "\n") {
			if strings.HasPrefix(line, "==== FILE: ") || strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "@@ ") {
				anchors = append(anchors, line)
			}
		}
		if len(anchors) > 0 {
			text = "Diff anchors retained after compaction:\n" + strings.Join(anchors, "\n")
		}
	case diffReference:
		paths := append([]string(nil), d.Paths...)
		sort.Strings(paths)
		text = fmt.Sprintf("Diff for [%s] compacted to a reference; call %s for exact hunks.",
			strings.Join(paths, ", "), FileReadDiffToolName)
	}
	if d.Label != "" {
		text = d.Label + ":\n" + text
	}
	if d.ToolCallID != "" {
		return llm.NewToolResultMessage(d.ToolCallID, text)
	}
	return llm.NewTextMessage("user", text)
}

func (d Diff) ToolName() string { return FileReadDiffToolName }

func (d Diff) Priority() int { return d.priority }

func NewDiff(paths []string, content string) *Diff {
	return &Diff{messageMeta: newMessageMeta(), Paths: append([]string(nil), paths...), Content: content}
}

func (d *Diff) ConfigurePresentation(label string, priority int) *Diff {
	d.Label = label
	d.priority = priority
	return d
}

func (d Diff) Compact(expect float64) (agentgo.AgentMessage, float64) {
	next := d
	next.Paths = append([]string(nil), d.Paths...)
	next.representation, expect = compactRepresentation(expect, d.representation, diffReference, next.render)
	return next, expect
}

// ToolReceipt is the small protocol acknowledgement for result/terminal tools
// and recoverable tool errors. Domain artifacts live in Runner collectors, so
// duplicating their payload in conversation would create a second truth source.
type ToolReceipt struct {
	messageMeta
	Tool       string
	Content    string
	ToolCallID string
}

func (r *ToolReceipt) FromLLM(result LLMToolResult) bool {
	*r = ToolReceipt{messageMeta: result.messageMeta(), Tool: result.Tool, Content: result.Content, ToolCallID: result.ToolCallID}
	return true
}

func (r ToolReceipt) ToLLM() llm.Message {
	return llm.NewToolResultMessage(r.ToolCallID, r.Content)
}

func (r ToolReceipt) GetRole() agentgo.Role     { return domainRole(r.ToLLM()) }
func (r ToolReceipt) Raw() agentgo.AgentMessage { return r }
func (r ToolReceipt) TextContent() string       { return domainText(r.ToLLM()) }
func (r ToolReceipt) ThinkingContent() string   { return "" }
func (r ToolReceipt) HasToolCalls() bool        { return domainHasToolCalls(r.ToLLM()) }
func (r ToolReceipt) ToMessage() (agentgo.Message, bool) {
	return domainToMessage(r.ToLLM(), r.ToolName(), r.GetTimestamp())
}
func (r ToolReceipt) Compact(float64) (agentgo.AgentMessage, float64) { return r, 1 }

func (r ToolReceipt) ToolName() string { return r.Tool }

func (r ToolReceipt) Priority() int { return 0 }

type llmDecoder interface {
	agentgo.AgentMessage
	FromLLM(LLMToolResult) bool
}

// FromLLM restores one wire tool result to the first matching message type.
// The dispatcher contains no message-specific parsing; each decoder sits next
// to that type's ToLLM so the two directions evolve together.
func FromLLM(result LLMToolResult) agentgo.AgentMessage {
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

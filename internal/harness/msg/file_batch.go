package msg

import (
	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// FileBatch preserves one read_files tool call as one message while retaining
// the identity of every file range inside it. That keeps the wire protocol's
// one-call/one-result pairing intact without flattening batch members into
// opaque text that ContextManager cannot deduplicate or compact.
type FileBatch struct {
	messageMeta
	items          []fileBatchItem
	tool           string
	toolCallID     string
	representation fileBatchRepresentation
}

type fileBatchRepresentation uint8

const (
	fileBatchSource fileBatchRepresentation = iota
	fileBatchOutline
	fileBatchReference
)

type fileBatchItem struct {
	file *File
	raw  string
}

func (b *FileBatch) FromLLM(result LLMToolResult) bool {
	if result.IsError || (result.Tool != FileReadToolName && result.Tool != FileReadBaseToolName) {
		return false
	}
	parts, ok := tool.DecodeFileReadResults(result.Content)
	if !ok {
		return false
	}
	items := make([]fileBatchItem, len(parts))
	for i, part := range parts {
		file := &File{}
		if file.FromLLM(LLMToolResult{Tool: result.Tool, Content: part}) {
			items[i].file = file
		} else {
			items[i].raw = part
		}
	}
	*b = FileBatch{messageMeta: result.messageMeta(), items: items, tool: result.Tool, toolCallID: result.ToolCallID}
	return true
}

func (b *FileBatch) ToLLM() llm.Message { return b.render(b.representation) }

func (b *FileBatch) GetRole() agentgo.Role { return domainRole(b.ToLLM()) }
func (b *FileBatch) Raw() agentgo.AgentMessage {
	raw := b.clone()
	raw.representation = fileBatchSource
	for i := range raw.items {
		if raw.items[i].file != nil {
			raw.items[i].file.representation = fileSource
			raw.items[i].file.stubbed = ""
		}
	}
	return raw
}
func (b *FileBatch) TextContent() string     { return domainText(b.ToLLM()) }
func (b *FileBatch) ThinkingContent() string { return "" }
func (b *FileBatch) HasToolCalls() bool      { return domainHasToolCalls(b.ToLLM()) }
func (b *FileBatch) ToMessage() (agentgo.Message, bool) {
	return domainToMessage(b.ToLLM(), b.ToolName(), b.GetTimestamp())
}

func (b *FileBatch) render(representation fileBatchRepresentation) llm.Message {
	parts := make([]string, len(b.items))
	for i, item := range b.items {
		if item.file != nil {
			view := *item.file
			requested := fileRepresentation(representation)
			if requested > view.representation {
				view.representation = requested
			}
			parts[i] = view.render()
		} else {
			parts[i] = item.raw
		}
	}
	return llm.NewToolResultMessage(b.toolCallID, tool.EncodeFileReadResults(parts))
}

func (b *FileBatch) ToolName() string { return b.tool }

func (b *FileBatch) Priority() int { return 0 }

func (b *FileBatch) Compact(expect float64) (agentgo.AgentMessage, float64) {
	next := b.clone()
	next.representation, expect = compactRepresentation(expect, b.representation, fileBatchReference, next.render)
	return next, expect
}

func (b *FileBatch) ContextItems() []agentgo.ContextItem {
	var out []agentgo.ContextItem
	for _, file := range b.Files() {
		view := *file
		if requested := fileRepresentation(b.representation); requested > view.representation {
			view.representation = requested
		}
		out = append(out, view.ContextItems()...)
	}
	return out
}

// Files returns the typed file members in request order. Error members remain
// in the batch result but do not claim visible coverage or evidence.
func (b *FileBatch) Files() []*File {
	files := make([]*File, 0, len(b.items))
	for _, item := range b.items {
		if item.file != nil {
			files = append(files, item.file)
		}
	}
	return files
}

// VisibleFiles returns the batch members whose current batch projection still
// contains exact source. Returned values are views.
func (b *FileBatch) VisibleFiles() []*File {
	var visible []*File
	for _, file := range b.Files() {
		view := *file
		if requested := fileRepresentation(b.representation); requested > view.representation {
			view.representation = requested
		}
		if view.FullContentVisible() {
			visible = append(visible, &view)
		}
	}
	return visible
}

func (b *FileBatch) clone() *FileBatch {
	copyBatch := &FileBatch{messageMeta: b.messageMeta, tool: b.tool, toolCallID: b.toolCallID, representation: b.representation, items: make([]fileBatchItem, len(b.items))}
	for i, item := range b.items {
		copyBatch.items[i].raw = item.raw
		if item.file != nil {
			file := *item.file
			copyBatch.items[i].file = &file
		}
	}
	return copyBatch
}

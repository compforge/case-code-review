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
	items      []fileBatchItem
	tool       string
	toolCallID string
}

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
	*b = FileBatch{items: items, tool: result.Tool, toolCallID: result.ToolCallID}
	return true
}

func (b *FileBatch) ToLLM(level CompactionLevel) llm.Message {
	parts := make([]string, len(b.items))
	for i, item := range b.items {
		if item.file != nil {
			parts[i] = item.file.render(level)
		} else {
			parts[i] = item.raw
		}
	}
	return llm.NewToolResultMessage(b.toolCallID, tool.EncodeFileReadResults(parts))
}

func (b *FileBatch) ToolName() string { return b.tool }

func (b *FileBatch) MaxCompaction() CompactionLevel { return CompactionReference }

func (b *FileBatch) ContextItems(level CompactionLevel) []agentgo.ContextItem {
	var out []agentgo.ContextItem
	for _, file := range b.Files() {
		out = append(out, file.ContextItems(level)...)
	}
	return out
}

func (b *FileBatch) Reclaim() {
	for _, file := range b.Files() {
		file.Reclaim()
	}
}

func (b *FileBatch) Reclaimed() bool {
	files := b.Files()
	if len(files) == 0 {
		return true
	}
	for _, file := range files {
		if !file.Reclaimed() {
			return false
		}
	}
	return true
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

func (b *FileBatch) clone() *FileBatch {
	copyBatch := &FileBatch{tool: b.tool, toolCallID: b.toolCallID, items: make([]fileBatchItem, len(b.items))}
	for i, item := range b.items {
		copyBatch.items[i].raw = item.raw
		if item.file != nil {
			file := *item.file
			copyBatch.items[i].file = &file
		}
	}
	return copyBatch
}

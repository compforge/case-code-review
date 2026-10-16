package msg

import (
	"time"

	"github.com/compforge/agentgo"
	"github.com/compforge/agentgo/codec"
	agentcontext "github.com/compforge/agentgo/context"
)

// Codec preserves CCR message identity, raw evidence and current compaction view
// in local AgentGo Sessions. It never turns a domain message into plain text.
func Codec(extra ...codec.Option) (codec.Codec, error) {
	options := append(agentcontext.CodecOptions(), CodecOptions()...)
	return agentgo.NewCodec(append(options, extra...)...)
}
func CodecOptions() []codec.Option {
	return []codec.Option{
		codec.WithHandler[Instruction, instructionRecord]("ccr.instruction.v1", instructionCodec{}),
		codec.WithHandler[messageMeta, messageMetaRecord]("ccr.message_meta.v1", messageMetaCodec{}),
		codec.WithHandler[*File, fileRecord]("ccr.file.v1", fileCodec{}),
		codec.WithHandler[*FileBatch, fileBatchRecord]("ccr.file_batch.v1", fileBatchCodec{}),
		codec.WithHandler[fileBatchItem, fileBatchItemRecord]("ccr.file_batch_item.v1", fileBatchItemCodec{}),
		codec.WithHandler[*SearchResult, searchResultRecord]("ccr.search_result.v1", searchResultCodec{}),
		codec.WithHandler[*SearchBatch, searchBatchRecord]("ccr.search_batch.v1", searchBatchCodec{}),
		codec.WithHandler[searchBatchItem, searchBatchItemRecord]("ccr.search_batch_item.v1", searchBatchItemCodec{}),
		codec.WithHandler[*Diff, diffRecord]("ccr.diff.v1", diffCodec{}),
		codec.WithHandler[*FileContext, fileContextRecord]("ccr.file_context.v1", fileContextCodec{}),
		codec.WithHandler[FileContextEntry, fileContextEntryRecord]("ccr.file_context_entry.v1", fileContextEntryCodec{}),
		codec.WithHandler[*Board, boardRecord]("ccr.board.v1", boardCodec{}),
		codec.WithHandler[ToolReceipt, toolReceiptRecord]("ccr.tool_receipt.v1", toolReceiptCodec{}),
		codec.WithHandler[*ToolView, toolViewRecord]("ccr.tool_view.v1", toolViewCodec{}),
		codec.Type[SourceArtifact]("ccr.source-artifact.v1"),
		codec.Type[DiffArtifact]("ccr.diff-artifact.v1"),
	}
}

type instructionRecord struct {
	Message agentgo.Message `codec:"message"`
}
type instructionCodec struct{}

func (instructionCodec) Encode(v Instruction) (instructionRecord, error) {
	return instructionRecord{Message: v.Message}, nil
}
func (instructionCodec) Decode(v instructionRecord) (Instruction, error) {
	return Instruction{Message: v.Message}, nil
}

type messageMetaRecord struct {
	Timestamp time.Time `codec:"timestamp"`
}
type messageMetaCodec struct{}

func (messageMetaCodec) Encode(v messageMeta) (messageMetaRecord, error) {
	return messageMetaRecord{Timestamp: v.timestamp}, nil
}
func (messageMetaCodec) Decode(v messageMetaRecord) (messageMeta, error) {
	return messageMeta{timestamp: v.Timestamp}, nil
}

type fileRecord struct {
	MessageMeta    messageMeta        `codec:"message_meta"`
	Path           string             `codec:"path"`
	Start          int                `codec:"start"`
	End            int                `codec:"end"`
	Total          int                `codec:"total"`
	Content        string             `codec:"content"`
	Snapshot       FileSnapshot       `codec:"snapshot"`
	Ref            string             `codec:"ref"`
	Label          string             `codec:"label"`
	ContextReason  string             `codec:"context_reason"`
	ContextRef     string             `codec:"context_ref"`
	Outline        string             `codec:"outline"`
	Representation fileRepresentation `codec:"representation"`
	Priority       int                `codec:"priority"`
	ToolCallID     string             `codec:"tool_callid"`
	Stubbed        StubReason         `codec:"stubbed"`
}
type fileCodec struct{}

func (fileCodec) Encode(v *File) (fileRecord, error) {
	return fileRecord{MessageMeta: v.messageMeta, Path: v.Path, Start: v.Start, End: v.End, Total: v.Total, Content: v.Content, Snapshot: v.Snapshot, Ref: v.Ref, Label: v.Label, ContextReason: v.ContextReason, ContextRef: v.ContextRef, Outline: v.Outline, Representation: v.representation, Priority: v.priority, ToolCallID: v.toolCallID, Stubbed: v.stubbed}, nil
}
func (fileCodec) Decode(v fileRecord) (*File, error) {
	return &File{messageMeta: v.MessageMeta, Path: v.Path, Start: v.Start, End: v.End, Total: v.Total, Content: v.Content, Snapshot: v.Snapshot, Ref: v.Ref, Label: v.Label, ContextReason: v.ContextReason, ContextRef: v.ContextRef, Outline: v.Outline, representation: v.Representation, priority: v.Priority, toolCallID: v.ToolCallID, stubbed: v.Stubbed}, nil
}

type fileBatchRecord struct {
	MessageMeta    messageMeta             `codec:"message_meta"`
	Items          []fileBatchItem         `codec:"items"`
	Tool           string                  `codec:"tool"`
	ToolCallID     string                  `codec:"tool_callid"`
	Representation fileBatchRepresentation `codec:"representation"`
}
type fileBatchCodec struct{}

func (fileBatchCodec) Encode(v *FileBatch) (fileBatchRecord, error) {
	return fileBatchRecord{MessageMeta: v.messageMeta, Items: v.items, Tool: v.tool, ToolCallID: v.toolCallID, Representation: v.representation}, nil
}
func (fileBatchCodec) Decode(v fileBatchRecord) (*FileBatch, error) {
	return &FileBatch{messageMeta: v.MessageMeta, items: v.Items, tool: v.Tool, toolCallID: v.ToolCallID, representation: v.Representation}, nil
}

type fileBatchItemRecord struct {
	File *File  `codec:"file"`
	Raw  string `codec:"raw"`
}
type fileBatchItemCodec struct{}

func (fileBatchItemCodec) Encode(v fileBatchItem) (fileBatchItemRecord, error) {
	return fileBatchItemRecord{File: v.file, Raw: v.raw}, nil
}
func (fileBatchItemCodec) Decode(v fileBatchItemRecord) (fileBatchItem, error) {
	return fileBatchItem{file: v.File, raw: v.Raw}, nil
}

type searchResultRecord struct {
	MessageMeta    messageMeta          `codec:"message_meta"`
	Tool           string               `codec:"tool"`
	Query          string               `codec:"query"`
	Paths          []string             `codec:"paths"`
	Content        string               `codec:"content"`
	ToolCallID     string               `codec:"tool_callid"`
	NoMatches      bool                 `codec:"no_matches"`
	SearchStatus   string               `codec:"search_status"`
	SearchedFiles  *int                 `codec:"searched_files"`
	Label          string               `codec:"label"`
	Representation searchRepresentation `codec:"representation"`
	Priority       int                  `codec:"priority"`
}
type searchResultCodec struct{}

func (searchResultCodec) Encode(v *SearchResult) (searchResultRecord, error) {
	return searchResultRecord{MessageMeta: v.messageMeta, Tool: v.Tool, Query: v.Query, Paths: v.Paths, Content: v.Content, ToolCallID: v.ToolCallID, NoMatches: v.NoMatches, SearchStatus: v.SearchStatus, SearchedFiles: v.SearchedFiles, Label: v.Label, Representation: v.representation, Priority: v.priority}, nil
}
func (searchResultCodec) Decode(v searchResultRecord) (*SearchResult, error) {
	return &SearchResult{messageMeta: v.MessageMeta, Tool: v.Tool, Query: v.Query, Paths: v.Paths, Content: v.Content, ToolCallID: v.ToolCallID, NoMatches: v.NoMatches, SearchStatus: v.SearchStatus, SearchedFiles: v.SearchedFiles, Label: v.Label, representation: v.Representation, priority: v.Priority}, nil
}

type searchBatchRecord struct {
	MessageMeta    messageMeta               `codec:"message_meta"`
	Items          []searchBatchItem         `codec:"items"`
	ToolCallID     string                    `codec:"tool_callid"`
	SharedSource   string                    `codec:"shared_source"`
	Representation searchBatchRepresentation `codec:"representation"`
}
type searchBatchCodec struct{}

func (searchBatchCodec) Encode(v *SearchBatch) (searchBatchRecord, error) {
	return searchBatchRecord{MessageMeta: v.messageMeta, Items: v.items, ToolCallID: v.toolCallID, SharedSource: v.sharedSource, Representation: v.representation}, nil
}
func (searchBatchCodec) Decode(v searchBatchRecord) (*SearchBatch, error) {
	return &SearchBatch{messageMeta: v.MessageMeta, items: v.Items, toolCallID: v.ToolCallID, sharedSource: v.SharedSource, representation: v.Representation}, nil
}

type searchBatchItemRecord struct {
	Result *SearchResult `codec:"result"`
	Raw    string        `codec:"raw"`
}
type searchBatchItemCodec struct{}

func (searchBatchItemCodec) Encode(v searchBatchItem) (searchBatchItemRecord, error) {
	return searchBatchItemRecord{Result: v.result, Raw: v.raw}, nil
}
func (searchBatchItemCodec) Decode(v searchBatchItemRecord) (searchBatchItem, error) {
	return searchBatchItem{result: v.Result, raw: v.Raw}, nil
}

type diffRecord struct {
	MessageMeta    messageMeta        `codec:"message_meta"`
	Paths          []string           `codec:"paths"`
	Content        string             `codec:"content"`
	ToolCallID     string             `codec:"tool_callid"`
	Label          string             `codec:"label"`
	Sources        []SourceArtifact   `codec:"sources"`
	Required       bool               `codec:"required"`
	Representation diffRepresentation `codec:"representation"`
	Priority       int                `codec:"priority"`
}
type diffCodec struct{}

func (diffCodec) Encode(v *Diff) (diffRecord, error) {
	return diffRecord{MessageMeta: v.messageMeta, Paths: v.Paths, Content: v.Content, ToolCallID: v.ToolCallID, Label: v.Label, Sources: v.sources, Required: v.required, Representation: v.representation, Priority: v.priority}, nil
}
func (diffCodec) Decode(v diffRecord) (*Diff, error) {
	return &Diff{messageMeta: v.MessageMeta, Paths: v.Paths, Content: v.Content, ToolCallID: v.ToolCallID, Label: v.Label, sources: v.Sources, required: v.Required, representation: v.Representation, priority: v.Priority}, nil
}

type fileContextRecord struct {
	MessageMeta    messageMeta               `codec:"message_meta"`
	Entries        []FileContextEntry        `codec:"entries"`
	Priority       int                       `codec:"priority"`
	Representation fileContextRepresentation `codec:"representation"`
}
type fileContextCodec struct{}

func (fileContextCodec) Encode(v *FileContext) (fileContextRecord, error) {
	return fileContextRecord{MessageMeta: v.messageMeta, Entries: v.entries, Priority: v.priority, Representation: v.representation}, nil
}
func (fileContextCodec) Decode(v fileContextRecord) (*FileContext, error) {
	return &FileContext{messageMeta: v.MessageMeta, entries: v.Entries, priority: v.Priority, representation: v.Representation}, nil
}

type fileContextEntryRecord struct {
	Path    string          `codec:"path"`
	View    FileContextView `codec:"view"`
	Reason  string          `codec:"reason"`
	Ref     string          `codec:"ref"`
	Content string          `codec:"content"`
}
type fileContextEntryCodec struct{}

func (fileContextEntryCodec) Encode(v FileContextEntry) (fileContextEntryRecord, error) {
	return fileContextEntryRecord{Path: v.Path, View: v.View, Reason: v.Reason, Ref: v.Ref, Content: v.Content}, nil
}
func (fileContextEntryCodec) Decode(v fileContextEntryRecord) (FileContextEntry, error) {
	return FileContextEntry{Path: v.Path, View: v.View, Reason: v.Reason, Ref: v.Ref, Content: v.Content}, nil
}

type boardRecord struct {
	MessageMeta    messageMeta         `codec:"message_meta"`
	Digest         string              `codec:"digest"`
	Representation boardRepresentation `codec:"representation"`
}
type boardCodec struct{}

func (boardCodec) Encode(v *Board) (boardRecord, error) {
	return boardRecord{MessageMeta: v.messageMeta, Digest: v.digest, Representation: v.representation}, nil
}
func (boardCodec) Decode(v boardRecord) (*Board, error) {
	return &Board{messageMeta: v.MessageMeta, digest: v.Digest, representation: v.Representation}, nil
}

type toolReceiptRecord struct {
	MessageMeta messageMeta `codec:"message_meta"`
	Tool        string      `codec:"tool"`
	Content     string      `codec:"content"`
	ToolCallID  string      `codec:"tool_callid"`
}
type toolReceiptCodec struct{}

func (toolReceiptCodec) Encode(v ToolReceipt) (toolReceiptRecord, error) {
	return toolReceiptRecord{MessageMeta: v.messageMeta, Tool: v.Tool, Content: v.Content, ToolCallID: v.ToolCallID}, nil
}
func (toolReceiptCodec) Decode(v toolReceiptRecord) (ToolReceipt, error) {
	return ToolReceipt{messageMeta: v.MessageMeta, Tool: v.Tool, Content: v.Content, ToolCallID: v.ToolCallID}, nil
}

type toolViewRecord struct {
	AgentMessage agentgo.AgentMessage `codec:"agent_message"`
	Content      string               `codec:"content"`
}
type toolViewCodec struct{}

func (toolViewCodec) Encode(v *ToolView) (toolViewRecord, error) {
	return toolViewRecord{AgentMessage: v.AgentMessage, Content: v.content}, nil
}
func (toolViewCodec) Decode(v toolViewRecord) (*ToolView, error) {
	return &ToolView{AgentMessage: v.AgentMessage, content: v.Content}, nil
}

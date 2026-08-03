package unitreview

import (
	"encoding/json"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/go-stdx/slicesx"
)

// AttachMessages retains the exact repository snapshots that were admitted to
// a Unit Review context. Prompt text and execution steering remain in Session
// JSONL rather than becoming Unit state.
func AttachMessages(reviewUnit *unit.Unit, messages []msg.Msg) {
	if reviewUnit == nil {
		return
	}
	for _, message := range messages {
		switch value := message.(type) {
		case *msg.File:
			reviewUnit.AddFileSnapshot(fileSnapshot(value))
		case *msg.FileBatch:
			for _, file := range value.Files() {
				reviewUnit.AddFileSnapshot(fileSnapshot(file))
			}
		case *msg.SearchResult:
			reviewUnit.AddSearchResult(searchResult(value))
		case *msg.SearchBatch:
			for _, result := range value.Results() {
				reviewUnit.AddSearchResult(searchResult(result))
			}
		case *msg.Diff:
			reviewUnit.AddRelatedDiff(diffSnapshot(value))
		}
	}
}

// AttachToolResult reconstructs a successful typed tool result using Harness's
// decoder, then appends its immutable snapshots to the originating Unit.
func AttachToolResult(reviewUnit *unit.Unit, name string, arguments json.RawMessage, result string) {
	var args map[string]any
	if json.Unmarshal(arguments, &args) != nil {
		return
	}
	AttachResult(reviewUnit, name, args, result)
}

// AttachResult is shared by Review 1 and Review 2 so newly read facts remain
// available to later stages instead of being trapped in one Execution trace.
func AttachResult(reviewUnit *unit.Unit, name string, args map[string]any, result string) {
	decoded := msg.FromLLM(msg.LLMToolResult{
		Tool: name, Arguments: args, Content: result,
	})
	AttachMessages(reviewUnit, []msg.Msg{decoded})
}

func fileSnapshot(file *msg.File) unit.FileSnapshot {
	kind := unit.CurrentSnapshot
	if file.Snapshot == msg.SnapshotBaseline {
		kind = unit.BaselineSnapshot
	}
	snapshot := unit.FileSnapshot{
		Kind: kind, Path: file.Path, Start: file.Start, End: file.End, Total: file.Total,
		Ref: file.Ref, Content: file.Content,
	}
	snapshot.ID = unit.FileSnapshotIDFor(snapshot)
	return snapshot
}

func searchResult(result *msg.SearchResult) unit.SearchResult {
	kind := unit.CodeSearch
	if result.Tool == msg.FileFindToolName {
		kind = unit.FileDiscovery
	}
	snapshot := unit.SearchResult{
		Kind: kind, Query: result.Query,
		Paths: pathsFromSearch(result.Content), Content: result.Content,
	}
	snapshot.ID = unit.SearchResultIDFor(snapshot)
	return snapshot
}

func diffSnapshot(diff *msg.Diff) unit.DiffSnapshot {
	snapshot := unit.DiffSnapshot{
		Paths: slicesx.Uniq(diff.Paths), Content: diff.Content,
	}
	snapshot.ID = unit.DiffSnapshotIDFor(snapshot)
	return snapshot
}

func pathsFromSearch(content string) []string {
	return tool.CodeSearchResultPaths(content)
}

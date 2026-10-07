// Derived from compforge/agentgo context compactors (Apache-2.0).
// CCR owns history-only policy; ZoneCompactor owns all suffix protection.
package compactor

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/compforge/agentgo"
)

// summaryExecution derives a deterministic child coordinate when compaction is
// owned by AgentLoop, while keeping direct ContextEngine use standalone.
func summaryExecution(ctx context.Context, name string) agentgo.Execution {
	execution := agentgo.Execution{ID: name, Kind: agentgo.ExecutionKindModel, Attempt: 1}
	if parent, ok := agentgo.ExecutionFromContext(ctx); ok {
		execution.ID = parent.ID + "/" + name
		execution.ParentID = parent.ID
		execution.TurnIndex = parent.TurnIndex
	}
	return execution
}

// stripImageBlocks returns a copy of msgs with image content blocks removed.
// Text descriptions like "[image: screenshot.png]" are preserved if present.
// This reduces token usage during summarization — images can't be text-summarized.
func stripImageBlocks(msgs []agentgo.AgentMessage) []agentgo.AgentMessage {
	out := make([]agentgo.AgentMessage, 0, len(msgs))
	for _, m := range msgs {
		msg, ok := m.ToMessage()
		if !ok {
			out = append(out, m)
			continue
		}
		hasImage := false
		for _, b := range msg.Content {
			if b.Type == agentgo.ContentImage {
				hasImage = true
				break
			}
		}
		if !hasImage {
			out = append(out, m)
			continue
		}
		// Filter out image blocks, keep everything else.
		filtered := make([]agentgo.ContentBlock, 0, len(msg.Content))
		for _, b := range msg.Content {
			if b.Type == agentgo.ContentImage {
				// Replace with placeholder text so the summary knows an image was here.
				filtered = append(filtered, agentgo.TextBlock("[image content omitted for summarization]"))
				continue
			}
			filtered = append(filtered, b)
		}
		cp := msg
		cp.Content = filtered
		out = append(out, cp)
	}
	return out
}

// extractFileOps scans messages for tool calls and extracts file paths.
func extractFileOps(msgs []agentgo.AgentMessage) (readFiles, modifiedFiles []string) {
	readSet := make(map[string]struct{})
	modifiedSet := make(map[string]struct{})

	for _, m := range msgs {
		msg, include := m.ToMessage()
		if !include || msg.Role != agentgo.RoleAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls() {
			path := extractPathArg(tc.Args)
			if path == "" {
				continue
			}
			switch tc.Name {
			case "read":
				readSet[path] = struct{}{}
			case "write":
				modifiedSet[path] = struct{}{}
			case "edit":
				modifiedSet[path] = struct{}{}
			}
		}
	}

	// Read-only files: read but not modified
	for f := range readSet {
		if _, modified := modifiedSet[f]; !modified {
			readFiles = append(readFiles, f)
		}
	}
	for f := range modifiedSet {
		modifiedFiles = append(modifiedFiles, f)
	}

	slices.Sort(readFiles)
	slices.Sort(modifiedFiles)
	return
}

// extractPathArg extracts a file path from JSON tool args. Accepts both
// "file_path" (preferred, matches edit/read/write) and "path" (used by
// glob/grep/ls). file_path wins when both are present.
func extractPathArg(args json.RawMessage) string {
	var obj struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if json.Unmarshal(args, &obj) == nil {
		if obj.FilePath != "" {
			return obj.FilePath
		}
		return obj.Path
	}
	return ""
}

// formatFileOps formats file operation lists as XML tags appended to the summary.
func formatFileOps(readFiles, modifiedFiles []string) string {
	if len(readFiles) == 0 && len(modifiedFiles) == 0 {
		return ""
	}
	var s string
	if len(readFiles) > 0 {
		s += "\n\n<read-files>\n" + strings.Join(readFiles, "\n") + "\n</read-files>"
	}
	if len(modifiedFiles) > 0 {
		s += "\n\n<modified-files>\n" + strings.Join(modifiedFiles, "\n") + "\n</modified-files>"
	}
	return s
}

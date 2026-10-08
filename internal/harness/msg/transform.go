package msg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

type sourceLine struct {
	path     string
	snapshot FileSnapshot
	ref      string
	line     int
}

// TransformSource removes repeated exact source within this request. Each pass
// rebuilds coverage from actual visible text, never Raw, so compaction of an
// earlier owner cannot leave later messages pointing at absent evidence.
// CCR fixes the review snapshot for an execution; snapshot/ref and exact line
// text additionally prevent current/base or changed-content reuse.
func TransformSource(input agentgo.TransformContext) []agentgo.AgentMessage {
	messages := input.Messages
	registered := registeredSource(input.Artifacts)
	out := append([]agentgo.AgentMessage(nil), messages...)
	seen := make(map[sourceLine]string)
	for i, message := range out {
		// Rebuild derived references on each pass; keep independently compacted
		// representations in AgentMessage rather than restoring Raw here.
		if view, ok := message.(*ToolView); ok {
			message = view.AgentMessage
		}
		message = limitToolMessage(message)
		text := message.TextContent()
		next := text
		switch value := message.(type) {
		case *File:
			if value.FullContentVisible() {
				next = dedupSourceLines(text, value.Path, value.Snapshot, value.Ref, seen, registered)
			}
		case *FileBatch:
			if value.representation != fileBatchSource {
				break
			}
			parts := make([]string, len(value.items))
			for n, item := range value.items {
				parts[n] = item.raw
				if item.file != nil {
					parts[n] = item.file.render()
					if item.file.FullContentVisible() {
						parts[n] = dedupSourceLines(parts[n], item.file.Path, item.file.Snapshot, item.file.Ref, seen, registered)
					}
				}
			}
			next = tool.EncodeFileReadResults(parts)
		case *SearchBatch:
			if value.representation != searchBatchFull {
				break
			}
			queries, source := tool.SplitCodeSearchSource(text)
			if source == "" {
				break
			} // Legacy records retain their original format.
			var shared, block strings.Builder
			path := ""
			flush := func() {
				shared.WriteString(dedupSourceLines(block.String(), path, SnapshotCurrent, "", seen, registered))
				block.Reset()
			}
			for _, line := range strings.SplitAfter(source, "\n") {
				if strings.HasPrefix(line, "File: ") {
					flush()
					path = strings.TrimSpace(strings.TrimPrefix(line, "File: "))
				}
				// A body with references no longer proves a complete source range.
				if strings.HasPrefix(line, "Symbol source: ") {
					continue
				}
				block.WriteString(line)
			}
			flush()
			if strings.Contains(shared.String(), "already shown in this context") {
				next = tool.AppendCodeSearchSource(queries, shared.String())
			}
		}
		if message.GetRole() == agentgo.RoleTool && len(next) > tool.MaxResultBytes {
			next = text
		}
		if next != text {
			// Reuse notices must not expand a short result beyond its admission limit.
			out[i] = &ToolView{AgentMessage: message, content: next}
		} else {
			out[i] = message
		}
	}
	return out
}

func dedupSourceLines(text, path string, snapshot FileSnapshot, ref string, seen map[sourceLine]string, registered sourceInventory) string {
	var out strings.Builder
	first, last := 0, 0
	flush := func() {
		if first == 0 {
			return
		}
		fmt.Fprintf(&out, "[Lines %d-%d already shown in this context for %s.]\n", first, last, path)
		first, last = 0, 0
	}
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		number, body, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "|")
		n, err := strconv.Atoi(number)
		if !ok || err != nil || n < 1 || path == "" {
			flush()
			out.WriteString(line)
			continue
		}
		// Search caps annotate a shortened source line on the following line.
		clipped := i+1 < len(lines) && strings.HasPrefix(lines[i+1], "[Output truncated:")
		key := sourceLine{path, snapshot, ref, n}
		// Inventory membership alone never removes source. Both the registered
		// observation and an earlier visible line in this request must agree.
		if registered != nil && !registered[key][body] {
			flush()
			out.WriteString(line)
			continue
		}
		if previous, exists := seen[key]; exists && previous == body && !clipped {
			if first != 0 && n != last+1 {
				flush()
			}
			if first == 0 {
				first = n
			}
			last = n
			continue
		}
		flush()
		out.WriteString(line)
		if !clipped {
			seen[key] = body
		}
	}
	flush()
	return out.String()
}

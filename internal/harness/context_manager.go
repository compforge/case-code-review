package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// contextManager keeps CCR's typed messages alive until the provider boundary.
// Projection is deterministic: deduplicate covered file reads, then shed
// re-derivable content when the configured window crosses its warning level.
type contextManager struct {
	window       int
	dedupEnabled bool
	engine       *agentcontext.ContextEngine

	mu           sync.Mutex
	usage        *agentgo.ContextUsage
	snapshot     *agentgo.ContextSnapshot
	visibleFiles []visibleFile
}

type fileSource string

const (
	fileFromPreload  fileSource = "the initial source context"
	fileFromTool     fileSource = "an earlier read_files result"
	fileFromBaseline fileSource = "an earlier read_base_files result"
)

type visibleFile struct {
	path              string
	start, end, total int
	source            fileSource
	label             string
	snapshot          msg.FileSnapshot
}

func newContextManager(spec ExecutionSpec, model agentgo.ChatModel) *contextManager {
	window := spec.ContextWindow
	if window == 0 {
		window = spec.MaxTokens
	}
	manager := &contextManager{
		window:       window,
		dedupEnabled: spec.FileDedupEnabled,
	}
	if window > 0 && model != nil {
		// Match CCR's existing 80% warning threshold while delegating the
		// actual trim/summary mechanics to agentgo.
		reserve := max(window/5, 1)
		compactors := []agentcontext.Compactor{
			agentcontext.NewToolResultCompactor(agentcontext.ToolResultMicrocompactConfig{}),
			agentcontext.NewLightTrimCompactor(agentcontext.LightTrimConfig{}),
			agentcontext.NewSummaryCompactor(agentcontext.FullSummaryConfig{
				Model:               model,
				KeepRecentTokens:    max(window/4, 1),
				SystemPrompt:        spec.CompressionSystemPrompt,
				SummaryPrompt:       spec.CompressionPrompt,
				UpdateSummaryPrompt: spec.CompressionUpdatePrompt,
				TurnPrefixPrompt:    spec.CompressionPrefixPrompt,
			}),
		}
		if spec.FileEvictEnabled {
			compactors = append([]agentcontext.Compactor{agentcontext.NewMessageCompactor()}, compactors...)
		}
		manager.engine = agentcontext.NewEngine(agentcontext.EngineConfig{
			ContextWindow:   window,
			ReserveTokens:   reserve,
			CommitOnProject: true,
			Compactor:       agentcontext.Chain(compactors...),
		})
	}
	return manager
}

func (m *contextManager) Project(
	ctx context.Context,
	messages []agentgo.AgentMessage,
) (agentgo.ContextProjection, error) {
	input := append([]agentgo.AgentMessage(nil), messages...)
	view, usage, changed := m.rewrite(input, false)
	projection := agentgo.ContextProjection{Messages: view, Usage: usage}
	if m.engine != nil {
		engineProjection, err := m.engine.Project(ctx, view)
		if err != nil {
			// Compression is a context optimization, not permission to discard
			// an otherwise runnable review turn. The engine keeps its own
			// failure circuit breaker; this turn falls back to the deterministic
			// dedup/evict view.
			projection.Messages = view
			if changed {
				projection.CommitMessages = view
				projection.ShouldCommit = true
			}
			projection.Messages = appendVisibleFileInventory(projection.Messages)
			projection.Usage = m.estimateUsage(projection.Messages)
			m.remember(messages, projection.Messages, projection.Usage, "project_fallback", changed)
			return projection, nil
		}
		projection.Messages = engineProjection.Messages
		projection.Usage = engineProjection.Usage
		projection.Compaction = engineProjection.Compaction
		if engineProjection.ShouldCommit {
			projection.CommitMessages = engineProjection.CommitMessages
			projection.ShouldCommit = true
			changed = true
		} else if changed {
			projection.CommitMessages = view
			projection.ShouldCommit = true
		}
	} else if changed {
		projection.CommitMessages = view
		projection.ShouldCommit = true
	}
	projection.Messages = appendVisibleFileInventory(projection.Messages)
	projection.Usage = m.estimateUsage(projection.Messages)
	m.remember(messages, projection.Messages, projection.Usage, "project", changed)
	return projection, nil
}

func (m *contextManager) Compact(
	ctx context.Context,
	messages []agentgo.AgentMessage,
	reason agentgo.CompactReason,
) (agentgo.ContextCommitResult, error) {
	view, usage, changed := m.rewrite(messages, true)
	if m.engine != nil {
		result, err := m.engine.Compact(ctx, view, reason)
		if err != nil {
			return agentgo.ContextCommitResult{}, err
		}
		if result.Changed {
			m.remember(messages, result.Messages, result.Usage, "compact", true)
			return result, nil
		}
	}
	m.remember(messages, view, usage, "compact", changed)
	return agentgo.ContextCommitResult{
		Messages: view,
		Usage:    usage,
		Changed:  changed,
	}, nil
}

func (m *contextManager) RecoverOverflow(
	ctx context.Context,
	messages []agentgo.AgentMessage,
	cause error,
) (agentgo.ContextRecoveryResult, error) {
	view, usage, changed := m.rewrite(messages, true)
	if m.engine != nil {
		result, err := m.engine.RecoverOverflow(ctx, view, cause)
		if err != nil {
			return agentgo.ContextRecoveryResult{}, err
		}
		if result.Changed {
			m.remember(messages, result.View, result.Usage, "overflow", true)
			return result, nil
		}
	}
	m.remember(messages, view, usage, "overflow", changed)
	return agentgo.ContextRecoveryResult{
		View:           view,
		CommitMessages: view,
		Usage:          usage,
		Changed:        changed,
		ShouldCommit:   changed,
	}, nil
}

func (m *contextManager) Sync(messages []agentgo.AgentMessage) {
	usage := m.estimateUsage(messages)
	m.remember(messages, messages, usage, "baseline", false)
}

func (m *contextManager) Usage() *agentgo.ContextUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usage == nil {
		return nil
	}
	cp := *m.usage
	return &cp
}

func (m *contextManager) Snapshot() *agentgo.ContextSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshot == nil {
		return nil
	}
	cp := *m.snapshot
	if cp.Usage != nil {
		usage := *cp.Usage
		cp.Usage = &usage
	}
	if cp.BaselineUsage != nil {
		baseline := *cp.BaselineUsage
		cp.BaselineUsage = &baseline
	}
	return &cp
}

func (m *contextManager) EstimateContext(messages []agentgo.AgentMessage) (int, int, int) {
	return m.estimateTokens(messages), 0, 0
}

func (m *contextManager) ContextWindow() int { return m.window }

func (m *contextManager) rewrite(
	messages []agentgo.AgentMessage,
	_ bool,
) ([]agentgo.AgentMessage, *agentgo.ContextUsage, bool) {
	view, changed := normalizeContextMessages(messages)
	if m.dedupEnabled {
		var compacted int
		view, compacted = msg.DedupFiles(view)
		changed = changed || compacted > 0
	}

	return view, m.estimateUsage(view), changed
}

func (m *contextManager) estimateUsage(messages []agentgo.AgentMessage) *agentgo.ContextUsage {
	tokens := m.estimateTokens(messages)
	usage := &agentgo.ContextUsage{
		Tokens:        tokens,
		ContextWindow: m.window,
	}
	if m.window > 0 {
		usage.Percent = float64(tokens) / float64(m.window) * 100
	}
	return usage
}

func (m *contextManager) estimateTokens(messages []agentgo.AgentMessage) int {
	return countContextTokens(messages)
}

func (m *contextManager) remember(
	baseline []agentgo.AgentMessage,
	view []agentgo.AgentMessage,
	usage *agentgo.ContextUsage,
	scope string,
	changed bool,
) {
	baselineUsage := m.estimateUsage(baseline)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
	m.snapshot = &agentgo.ContextSnapshot{
		BaselineUsage:      baselineUsage,
		Usage:              usage,
		Scope:              scope,
		TranscriptMessages: len(baseline),
		ActiveMessages:     len(view),
		LastChanged:        changed,
	}
	m.visibleFiles = visibleFilesIn(view)
}

// coveredFileRead returns a lightweight result when the exact range requested
// by read_files is already visible to the model. It checks the post-projection
// view, so content evicted or summarized out of the prompt never blocks a
// legitimate re-read.
func (m *contextManager) coveredFileRead(request tool.FileReadRequest) (string, bool) {
	if !m.dedupEnabled {
		return "", false
	}
	filePath := path.Clean(request.FilePath)
	if filePath == "." || filePath == "" {
		return "", false
	}
	start := request.StartLine
	if start <= 0 {
		start = 1
	}
	requestedEnd := request.EndLine
	if requestedEnd > 0 && requestedEnd < start {
		return "", false
	}

	m.mu.Lock()
	files := append([]visibleFile(nil), m.visibleFiles...)
	m.mu.Unlock()

	var preload *visibleFile
	for i := range files {
		visible := &files[i]
		if visible.snapshot != msg.SnapshotCurrent || visible.path != filePath || start > visible.total {
			continue
		}
		end := min(start+tool.FileReadMaxLines-1, visible.total)
		if requestedEnd > 0 {
			end = min(end, requestedEnd)
		}
		if start < visible.start || end > visible.end {
			continue
		}
		if visible.source == fileFromTool {
			return coveredReadMessage(filePath, start, end, visible.description()), true
		}
		preload = visible
	}
	if preload != nil {
		end := min(start+tool.FileReadMaxLines-1, preload.total)
		if requestedEnd > 0 {
			end = min(end, requestedEnd)
		}
		return coveredReadMessage(filePath, start, end, preload.description()), true
	}
	return "", false
}

func (f visibleFile) description() string {
	if f.label != "" {
		return fmt.Sprintf("%s (%s)", f.source, f.label)
	}
	return string(f.source)
}

func coveredReadMessage(filePath string, start, end int, source string) string {
	return fmt.Sprintf(
		"Already available in the current context from %s: %s lines %d-%d. Reuse that content; call read_files only for a range not shown there.",
		source, filePath, start, end,
	)
}

func visibleFilesIn(messages []agentgo.AgentMessage) []visibleFile {
	var out []visibleFile
	for _, message := range messages {
		switch value := message.(type) {
		case *msg.File:
			files := []*msg.File{value}
			for _, file := range files {
				if !file.FullContentVisible() {
					continue
				}
				source := fileFromPreload
				if file.IsToolResult() {
					if file.Snapshot == msg.SnapshotBaseline {
						source = fileFromBaseline
					} else {
						source = fileFromTool
					}
				}
				out = append(out, visibleFile{
					path: path.Clean(file.Path), start: file.Start, end: file.End,
					total: file.Total, source: source, label: file.Label, snapshot: file.Snapshot,
				})
			}
		case *msg.FileBatch:
			for _, file := range value.VisibleFiles() {
				source := fileFromTool
				if file.Snapshot == msg.SnapshotBaseline {
					source = fileFromBaseline
				}
				out = append(out, visibleFile{
					path: path.Clean(file.Path), start: file.Start, end: file.End,
					total: file.Total, source: source, label: file.Label, snapshot: file.Snapshot,
				})
			}
		case *msg.SearchBatch:
			for _, source := range tool.CodeSearchSourceRanges(value.TextContent()) {
				out = append(out, visibleFile{
					path: path.Clean(source.Path), start: source.StartLine, end: source.EndLine,
					total: source.TotalLines, source: fileFromTool,
					label: "search symbol context", snapshot: msg.SnapshotCurrent,
				})
			}
		case agentgo.Message:
			parts, batch := tool.DecodeFileReadResults(value.TextContent())
			if !batch {
				parts = []string{value.TextContent()}
			}
			for _, part := range parts {
				filePath, start, end, total, ok := msg.VisibleFileRange(part)
				if !ok {
					continue
				}
				source := fileFromPreload
				snapshot := msg.SnapshotCurrent
				if toolName := metadataString(value.Metadata, "tool_name"); toolName == msg.FileReadBaseToolName {
					source = fileFromBaseline
					snapshot = msg.SnapshotBaseline
				} else if value.Role == agentgo.RoleTool || toolName == msg.FileReadToolName {
					source = fileFromTool
				}
				out = append(out, visibleFile{
					path: path.Clean(filePath), start: start, end: end,
					total: total, source: source, label: msg.VisibleFileLabel(part), snapshot: snapshot,
				})
			}
		}
	}
	return out
}

func normalizeContextMessages(messages []agentgo.AgentMessage) ([]agentgo.AgentMessage, bool) {
	invocations := toolInvocations(messages)
	out := make([]agentgo.AgentMessage, 0, len(messages))
	changed := false
	for _, message := range messages {
		switch value := message.(type) {
		case agentgo.Message:
			if value.Role == agentgo.RoleTool {
				toolCallID := metadataString(value.Metadata, "tool_call_id")
				invocation := invocations[toolCallID]
				if invocation.name == "" {
					invocation.name = metadataString(value.Metadata, "tool_name")
				}
				if invocation.name != "" {
					isError, _ := value.Metadata["is_error"].(bool)
					decoded := msg.FromLLM(msg.LLMToolResult{
						Tool: invocation.name, ToolCallID: toolCallID, Arguments: invocation.args,
						Content: value.TextContent(), IsError: isError, Timestamp: value.Timestamp,
					})
					out = append(out, decoded)
					changed = true
					continue
				}
			}
			out = append(out, value)
		default:
			out = append(out, value)
		}
	}
	return out, changed
}

func countContextTokens(messages []agentgo.AgentMessage) int {
	var total int
	for _, message := range messages {
		total += llm.CountTokens(message.TextContent())
	}
	return total
}

func appendVisibleFileInventory(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
	files := visibleFilesIn(messages)
	if len(files) == 0 {
		return messages
	}
	var b strings.Builder
	b.WriteString("Available file content already present in this request; reuse covered ranges instead of calling read_files:\n")
	for _, file := range files {
		fmt.Fprintf(&b, "- %s lines %d-%d", file.path, file.start, file.end)
		if file.label != "" {
			fmt.Fprintf(&b, " — %s", file.label)
		} else if file.snapshot == msg.SnapshotBaseline {
			b.WriteString(" — baseline source")
		}
		b.WriteByte('\n')
	}
	return append(messages, msg.Text("user", strings.TrimRight(b.String(), "\n")))
}

type toolInvocation struct {
	name string
	args map[string]any
}

func toolInvocations(messages []agentgo.AgentMessage) map[string]toolInvocation {
	out := make(map[string]toolInvocation)
	for _, message := range messages {
		lowered, include := message.ToMessage()
		if !include {
			continue
		}
		for _, call := range lowered.ToolCalls() {
			var args map[string]any
			_ = json.Unmarshal(call.Args, &args)
			out[call.ID] = toolInvocation{name: call.Name, args: args}
		}
	}
	return out
}

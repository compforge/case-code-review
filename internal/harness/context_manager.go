package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	agentcontext "github.com/voocel/agentcore/context"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

const contextWarningThreshold = 0.80
const wrapUpTimeReserve = 90 * time.Second
const wrapUpTurnReserve = 2

type domainMessage struct {
	value      msg.Msg
	timestamp  time.Time
	compaction msg.CompactionLevel
}

func (m domainMessage) GetRole() agentcore.Role {
	return agentcore.Role(m.value.ToLLM(m.compaction).Role)
}
func (m domainMessage) GetTimestamp() time.Time { return m.timestamp }
func (m domainMessage) TextContent() string {
	wire := m.value.ToLLM(m.compaction)
	return wire.ExtractText()
}
func (m domainMessage) ThinkingContent() string { return "" }
func (m domainMessage) HasToolCalls() bool {
	return len(m.value.ToLLM(m.compaction).ToolCalls) > 0
}

// contextManager keeps CCR's typed messages alive until the provider boundary.
// Projection is deterministic: deduplicate covered file reads, then shed
// re-derivable content when the configured window crosses its warning level.
type contextManager struct {
	window       int
	dedupEnabled bool
	evictEnabled bool
	maxTurns     int
	wrapUpPrompt string
	scope        session.Scope
	turnContext  TurnContextProvider
	engine       *agentcontext.ContextEngine

	mu           sync.Mutex
	usage        *agentcore.ContextUsage
	snapshot     *agentcore.ContextSnapshot
	projectCount int
	wrapUpIssued bool
	visibleFiles []visibleFile
}

type fileSource string

const (
	fileFromPreload  fileSource = "the initial source context"
	fileFromTool     fileSource = "an earlier file_read result"
	fileFromBaseline fileSource = "an earlier file_read_base result"
)

type visibleFile struct {
	path              string
	start, end, total int
	source            fileSource
	label             string
	snapshot          msg.FileSnapshot
}

func newContextManager(spec ExecutionSpec, model agentcore.ChatModel) *contextManager {
	window := spec.ContextWindow
	if window == 0 {
		window = spec.MaxTokens
	}
	manager := &contextManager{
		window:       window,
		dedupEnabled: spec.FileDedupEnabled,
		evictEnabled: spec.FileEvictEnabled,
		maxTurns:     spec.MaxTurns,
		wrapUpPrompt: spec.WrapUpPrompt,
		scope:        spec.Scope,
		turnContext:  spec.TurnContext,
	}
	if window > 0 && model != nil {
		// Match CCR's existing 80% warning threshold while delegating the
		// actual trim/summary mechanics to agentcore.
		reserve := max(window/5, 1)
		manager.engine = agentcontext.NewEngine(agentcontext.EngineConfig{
			ContextWindow:   window,
			ReserveTokens:   reserve,
			CommitOnProject: true,
			Strategies: []agentcontext.Strategy{
				agentcontext.NewToolResultMicrocompact(agentcontext.ToolResultMicrocompactConfig{}),
				agentcontext.NewLightTrim(agentcontext.LightTrimConfig{}),
				agentcontext.NewFullSummary(agentcontext.FullSummaryConfig{
					Model:               model,
					KeepRecentTokens:    max(window/4, 1),
					SystemPrompt:        spec.CompressionSystemPrompt,
					SummaryPrompt:       spec.CompressionPrompt,
					UpdateSummaryPrompt: spec.CompressionUpdatePrompt,
					TurnPrefixPrompt:    spec.CompressionPrefixPrompt,
				}),
			},
		})
	}
	return manager
}

func (m *contextManager) Project(
	ctx context.Context,
	messages []agentcore.AgentMessage,
) (agentcore.ContextProjection, error) {
	input := append([]agentcore.AgentMessage(nil), messages...)
	commit := false
	if m.turnContext != nil {
		if extra := m.turnContext.PullTurnContext(ctx, m.scope); len(extra) > 0 {
			input = append(input, wrapDomainMessages(msg.CloneAll(extra))...)
			commit = true
		}
	}
	if m.shouldWrapUp(ctx) {
		input = append(input, domainMessage{
			value:     msg.Text("user", m.wrapUpPrompt),
			timestamp: time.Now(),
		})
		commit = true
	}
	view, usage, changed := m.rewrite(input, false)
	projection := agentcore.ContextProjection{Messages: view, Usage: usage}
	if m.engine != nil {
		engineProjection, err := m.engine.Project(ctx, lowerContextMessages(view))
		if err != nil {
			// Compression is a context optimization, not permission to discard
			// an otherwise runnable review turn. The engine keeps its own
			// failure circuit breaker; this turn falls back to the deterministic
			// dedup/evict view.
			projection.Messages = lowerContextMessages(view)
			if changed || commit {
				projection.CommitMessages = view
				projection.ShouldCommit = true
			}
			projection.Messages = appendVisibleFileInventory(projection.Messages)
			projection.Usage = m.estimateUsage(projection.Messages)
			m.remember(messages, projection.Messages, projection.Usage, "project_fallback", changed || commit)
			return projection, nil
		}
		projection.Messages = engineProjection.Messages
		projection.Usage = engineProjection.Usage
		if engineProjection.ShouldCommit {
			projection.CommitMessages = engineProjection.CommitMessages
			projection.ShouldCommit = true
			changed = true
		} else if changed || commit {
			projection.CommitMessages = view
			projection.ShouldCommit = true
		}
	} else if changed || commit {
		projection.CommitMessages = view
		projection.ShouldCommit = true
	}
	projection.Messages = appendVisibleFileInventory(projection.Messages)
	projection.Usage = m.estimateUsage(projection.Messages)
	m.remember(messages, projection.Messages, projection.Usage, "project", changed || commit)
	return projection, nil
}

func (m *contextManager) Compact(
	ctx context.Context,
	messages []agentcore.AgentMessage,
	_ agentcore.CompactReason,
) (agentcore.ContextCommitResult, error) {
	view, usage, changed := m.rewrite(messages, true)
	if m.engine != nil {
		result, err := m.engine.Compact(ctx, lowerContextMessages(view), agentcore.CompactReasonManual)
		if err != nil {
			return agentcore.ContextCommitResult{}, err
		}
		if result.Changed {
			m.remember(messages, result.Messages, result.Usage, "compact", true)
			return result, nil
		}
	}
	m.remember(messages, view, usage, "compact", changed)
	return agentcore.ContextCommitResult{
		Messages: view,
		Usage:    usage,
		Changed:  changed,
		Strategy: "review_context",
	}, nil
}

func (m *contextManager) RecoverOverflow(
	ctx context.Context,
	messages []agentcore.AgentMessage,
	cause error,
) (agentcore.ContextRecoveryResult, error) {
	view, usage, changed := m.rewrite(messages, true)
	if m.engine != nil {
		result, err := m.engine.RecoverOverflow(ctx, lowerContextMessages(view), cause)
		if err != nil {
			return agentcore.ContextRecoveryResult{}, err
		}
		if result.Changed {
			m.remember(messages, result.View, result.Usage, "overflow", true)
			return result, nil
		}
	}
	m.remember(messages, view, usage, "overflow", changed)
	return agentcore.ContextRecoveryResult{
		View:           view,
		CommitMessages: view,
		Usage:          usage,
		Changed:        changed,
		ShouldCommit:   changed,
		Strategy:       "review_context",
	}, nil
}

func (m *contextManager) Sync(messages []agentcore.AgentMessage) {
	usage := m.estimateUsage(messages)
	m.remember(messages, messages, usage, "baseline", false)
}

func (m *contextManager) Usage() *agentcore.ContextUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usage == nil {
		return nil
	}
	cp := *m.usage
	return &cp
}

func (m *contextManager) Snapshot() *agentcore.ContextSnapshot {
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

func (m *contextManager) ConvertToLLM(messages []agentcore.AgentMessage) []agentcore.Message {
	out := make([]agentcore.Message, 0, len(messages))
	for _, message := range messages {
		switch value := message.(type) {
		case domainMessage:
			out = append(out, wireToAgentMessage(value.value.ToLLM(value.compaction)))
		case agentcore.Message:
			out = append(out, value)
		case agentcontext.ContextSummary:
			out = append(out, agentcontext.ContextConvertToLLM(
				[]agentcore.AgentMessage{value},
			)...)
		}
	}
	return out
}

func (m *contextManager) EstimateContext(messages []agentcore.AgentMessage) (int, int, int) {
	return m.estimateTokens(messages), 0, 0
}

func (m *contextManager) ContextWindow() int { return m.window }

func (m *contextManager) WrapUpIssued() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wrapUpIssued
}

func (m *contextManager) shouldWrapUp(ctx context.Context) bool {
	if m.wrapUpPrompt == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectCount++
	if m.wrapUpIssued {
		return false
	}

	nearTurnLimit := false
	if m.maxTurns > 0 {
		remaining := m.maxTurns - m.projectCount + 1
		nearTurnLimit = remaining <= wrapUpTurnReserve
	}
	nearDeadline := false
	if deadline, ok := ctx.Deadline(); ok {
		nearDeadline = time.Until(deadline) < wrapUpTimeReserve
	}
	if nearTurnLimit || nearDeadline {
		m.wrapUpIssued = true
		return true
	}
	return false
}

func (m *contextManager) rewrite(
	messages []agentcore.AgentMessage,
	force bool,
) ([]agentcore.AgentMessage, *agentcore.ContextUsage, bool) {
	view, changed := normalizeContextMessages(messages)
	if m.dedupEnabled && compactCoveredFiles(view) > 0 {
		changed = true
	}

	limit := int(float64(m.window) * contextWarningThreshold)
	if force {
		limit = 0
	}
	if m.evictEnabled && (force || limit > 0 && countContextTokens(view) > limit) {
		if compactUntil(view, limit) > 0 {
			changed = true
		}
	}

	return view, m.estimateUsage(view), changed
}

func (m *contextManager) estimateUsage(messages []agentcore.AgentMessage) *agentcore.ContextUsage {
	tokens := m.estimateTokens(messages)
	usage := &agentcore.ContextUsage{
		Tokens:        tokens,
		ContextWindow: m.window,
	}
	if m.window > 0 {
		usage.Percent = float64(tokens) / float64(m.window) * 100
	}
	return usage
}

func (m *contextManager) estimateTokens(messages []agentcore.AgentMessage) int {
	return countContextTokens(messages)
}

func (m *contextManager) remember(
	baseline []agentcore.AgentMessage,
	view []agentcore.AgentMessage,
	usage *agentcore.ContextUsage,
	scope string,
	changed bool,
) {
	baselineUsage := m.estimateUsage(baseline)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
	m.snapshot = &agentcore.ContextSnapshot{
		BaselineUsage:      baselineUsage,
		Usage:              usage,
		Scope:              scope,
		TranscriptMessages: len(baseline),
		ActiveMessages:     len(view),
		LastStrategy:       "review_context",
		LastChanged:        changed,
	}
	m.visibleFiles = visibleFilesIn(view)
}

// coveredFileRead returns a lightweight result when the exact range requested
// by file_read is already visible to the model. It checks the post-projection
// view, so content evicted or summarized out of the prompt never blocks a
// legitimate re-read.
func (m *contextManager) coveredFileRead(args map[string]any) (string, bool) {
	if !m.dedupEnabled {
		return "", false
	}
	filePath, _ := args["file_path"].(string)
	filePath = path.Clean(filePath)
	if filePath == "." || filePath == "" {
		return "", false
	}
	start := positiveInt(args["start_line"], 1)
	requestedEnd := positiveInt(args["end_line"], 0)
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
			return coveredReadMessage(filePath, start, end, visible.source), true
		}
		preload = visible
	}
	if preload != nil {
		end := min(start+tool.FileReadMaxLines-1, preload.total)
		if requestedEnd > 0 {
			end = min(end, requestedEnd)
		}
		return coveredReadMessage(filePath, start, end, preload.source), true
	}
	return "", false
}

func coveredReadMessage(filePath string, start, end int, source fileSource) string {
	return fmt.Sprintf(
		"Already available in the current context from %s: %s lines %d-%d. Reuse that content; call file_read only for a range not shown there.",
		source, filePath, start, end,
	)
}

func positiveInt(value any, fallback int) int {
	switch n := value.(type) {
	case float64:
		if n > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	}
	return fallback
}

func visibleFilesIn(messages []agentcore.AgentMessage) []visibleFile {
	var out []visibleFile
	for _, message := range messages {
		switch value := message.(type) {
		case domainMessage:
			file, ok := value.value.(*msg.File)
			if !ok || file.Stubbed() || value.compaction >= msg.CompactionReference {
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
		case agentcore.Message:
			filePath, start, end, total, ok := msg.VisibleFileRange(value.TextContent())
			if !ok {
				continue
			}
			source := fileFromPreload
			snapshot := msg.SnapshotCurrent
			if toolName := metadataString(value.Metadata, "tool_name"); toolName == msg.FileReadBaseToolName {
				source = fileFromBaseline
				snapshot = msg.SnapshotBaseline
			} else if value.Role == agentcore.RoleTool || toolName == msg.FileReadToolName {
				source = fileFromTool
			}
			out = append(out, visibleFile{
				path: path.Clean(filePath), start: start, end: end,
				total: total, source: source, label: msg.VisibleFileLabel(value.TextContent()), snapshot: snapshot,
			})
		}
	}
	return out
}

func wrapDomainMessages(messages []msg.Msg) []agentcore.AgentMessage {
	out := make([]agentcore.AgentMessage, len(messages))
	for i, message := range messages {
		out[i] = domainMessage{value: message, timestamp: time.Now(), compaction: msg.CompactionNone}
	}
	return out
}

func normalizeContextMessages(messages []agentcore.AgentMessage) ([]agentcore.AgentMessage, bool) {
	invocations := toolInvocations(messages)
	out := make([]agentcore.AgentMessage, 0, len(messages))
	changed := false
	for _, message := range messages {
		switch value := message.(type) {
		case domainMessage:
			cloned := msg.CloneAll([]msg.Msg{value.value})
			out = append(out, domainMessage{value: cloned[0], timestamp: value.timestamp, compaction: value.compaction})
		case agentcore.Message:
			changed = true
			wire := toLLMMessages([]agentcore.Message{value})
			if len(wire) == 0 {
				continue
			}
			if value.Role == agentcore.RoleTool {
				toolCallID := metadataString(value.Metadata, "tool_call_id")
				invocation := invocations[toolCallID]
				if invocation.name == "" {
					invocation.name = metadataString(value.Metadata, "tool_name")
				}
				if invocation.name != "" {
					isError, _ := value.Metadata["is_error"].(bool)
					decoded := msg.FromLLM(msg.LLMToolResult{
						Tool: invocation.name, ToolCallID: toolCallID, Arguments: invocation.args,
						Content: wire[0].ExtractText(), IsError: isError,
					})
					out = append(out, domainMessage{value: decoded, timestamp: value.Timestamp})
					continue
				}
			}
			out = append(out, domainMessage{
				value:     msg.Raw{M: wire[0]},
				timestamp: value.Timestamp,
			})
		default:
			out = append(out, value)
		}
	}
	return out, changed
}

func lowerContextMessages(messages []agentcore.AgentMessage) []agentcore.AgentMessage {
	out := make([]agentcore.AgentMessage, 0, len(messages))
	for _, message := range messages {
		value, ok := message.(domainMessage)
		if !ok {
			out = append(out, message)
			continue
		}
		lowered := wireToAgentMessage(value.value.ToLLM(value.compaction))
		lowered.Timestamp = value.timestamp
		if result, ok := value.value.(msg.ToolResultMsg); ok && lowered.Role == agentcore.RoleTool {
			if lowered.Metadata == nil {
				lowered.Metadata = make(map[string]any)
			}
			lowered.Metadata["tool_name"] = result.ToolName()
		}
		out = append(out, lowered)
	}
	return out
}

func countContextTokens(messages []agentcore.AgentMessage) int {
	var total int
	for _, message := range messages {
		total += llm.CountTokens(message.TextContent())
	}
	return total
}

func compactCoveredFiles(messages []agentcore.AgentMessage) (compacted int) {
	for i := len(messages) - 1; i >= 0; i-- {
		newer, ok := messages[i].(domainMessage)
		if !ok || newer.compaction >= msg.CompactionReference {
			continue
		}
		newerFile, ok := newer.value.(*msg.File)
		if !ok || newerFile.Stubbed() {
			continue
		}
		for j := 0; j < i; j++ {
			older, ok := messages[j].(domainMessage)
			if !ok || older.compaction >= msg.CompactionReference {
				continue
			}
			olderFile, ok := older.value.(*msg.File)
			if !ok || olderFile.Stubbed() || !newerFile.Covers(olderFile) ||
				olderFile.MaxCompaction() < msg.CompactionReference {
				continue
			}
			older.compaction = msg.CompactionReference
			messages[j] = older
			compacted++
		}
	}
	return compacted
}

// compactUntil raises message-owned projections monotonically, from the tail
// backward. Stable prefixes stay byte-identical for provider cache reuse; a
// committed projection never expands again within the execution.
func compactUntil(messages []agentcore.AgentMessage, limit int) (compacted int) {
	for _, level := range []msg.CompactionLevel{msg.CompactionCondensed, msg.CompactionReference} {
		for i := len(messages) - 1; i >= 0; i-- {
			if limit > 0 && countContextTokens(messages) <= limit {
				return compacted
			}
			value, ok := messages[i].(domainMessage)
			if !ok || value.compaction >= level {
				continue
			}
			candidate, ok := value.value.(msg.Compactable)
			if !ok || candidate.MaxCompaction() < level {
				continue
			}
			before := llm.CountTokens(value.TextContent())
			value.compaction = level
			after := llm.CountTokens(value.TextContent())
			if after >= before {
				continue
			}
			messages[i] = value
			compacted++
		}
	}
	return compacted
}

func appendVisibleFileInventory(messages []agentcore.AgentMessage) []agentcore.AgentMessage {
	files := visibleFilesIn(messages)
	if len(files) == 0 {
		return messages
	}
	var b strings.Builder
	b.WriteString("Available file content already present in this request; reuse covered ranges instead of calling file_read:\n")
	for _, file := range files {
		fmt.Fprintf(&b, "- %s lines %d-%d", file.path, file.start, file.end)
		if file.label != "" {
			fmt.Fprintf(&b, " — %s", file.label)
		} else if file.snapshot == msg.SnapshotBaseline {
			b.WriteString(" — baseline source")
		}
		b.WriteByte('\n')
	}
	return append(messages, wireToAgentMessage(llm.NewTextMessage("user", strings.TrimRight(b.String(), "\n"))))
}

type toolInvocation struct {
	name string
	args map[string]any
}

func toolInvocations(messages []agentcore.AgentMessage) map[string]toolInvocation {
	out := make(map[string]toolInvocation)
	for _, message := range messages {
		var calls []llm.ToolCall
		switch value := message.(type) {
		case domainMessage:
			calls = value.value.ToLLM(value.compaction).ToolCalls
		case agentcore.Message:
			for _, call := range value.ToolCalls() {
				calls = append(calls, llm.ToolCall{
					ID: call.ID, Type: "function",
					Function: llm.FunctionCall{Name: call.Name, Arguments: string(call.Args)},
				})
			}
		}
		for _, call := range calls {
			var args map[string]any
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
			out[call.ID] = toolInvocation{name: call.Function.Name, args: args}
		}
	}
	return out
}

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

type domainMessage struct {
	value      msg.Msg
	timestamp  time.Time
	compaction msg.CompactionLevel
}

func (m domainMessage) GetRole() agentgo.Role {
	return agentgo.Role(m.value.ToLLM(m.compaction).Role)
}
func (m domainMessage) GetTimestamp() time.Time { return m.timestamp }
func (m domainMessage) Raw() agentgo.AgentMessage {
	m.compaction = msg.CompactionNone
	return m
}
func (m domainMessage) Priority() int { return 0 }
func (m domainMessage) Compact(expect float64) (agentgo.AgentMessage, float64) {
	raw := m.value.ToLLM(msg.CompactionNone)
	rawTokens := llm.CountTokens(raw.ExtractText())
	if rawTokens <= 0 {
		return m, 1
	}
	currentTokens := llm.CountTokens(m.TextContent())
	if float64(currentTokens)/float64(rawTokens) <= expect {
		return m, float64(currentTokens) / float64(rawTokens)
	}
	compactable, ok := m.value.(msg.Compactable)
	if !ok {
		return m, float64(currentTokens) / float64(rawTokens)
	}
	for level := m.compaction + 1; level <= compactable.MaxCompaction(); level++ {
		m.compaction = level
		currentTokens = llm.CountTokens(m.TextContent())
		if float64(currentTokens)/float64(rawTokens) <= expect {
			break
		}
	}
	return m, float64(currentTokens) / float64(rawTokens)
}
func (m domainMessage) TextContent() string {
	wire := m.value.ToLLM(m.compaction)
	return wire.ExtractText()
}
func (m domainMessage) ThinkingContent() string { return "" }
func (m domainMessage) HasToolCalls() bool {
	return len(m.value.ToLLM(m.compaction).ToolCalls) > 0
}
func (m domainMessage) ToMessage() (agentgo.Message, bool) {
	lowered := wireToAgentMessage(m.value.ToLLM(m.compaction))
	lowered.Timestamp = m.timestamp
	if result, ok := m.value.(msg.ToolResultMsg); ok && lowered.Role == agentgo.RoleTool {
		if lowered.Metadata == nil {
			lowered.Metadata = make(map[string]any)
		}
		lowered.Metadata["tool_name"] = result.ToolName()
	}
	return lowered, true
}

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
	_ agentgo.CompactReason,
) (agentgo.ContextCommitResult, error) {
	view, usage, changed := m.rewrite(messages, true)
	if m.engine != nil {
		result, err := m.engine.Compact(ctx, view, agentgo.CompactReasonManual)
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
	if m.dedupEnabled && compactCoveredFiles(view) > 0 {
		changed = true
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
// by file_read is already visible to the model. It checks the post-projection
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

func visibleFilesIn(messages []agentgo.AgentMessage) []visibleFile {
	var out []visibleFile
	for _, message := range messages {
		switch value := message.(type) {
		case domainMessage:
			var files []*msg.File
			switch typed := value.value.(type) {
			case *msg.File:
				files = []*msg.File{typed}
			case *msg.FileBatch:
				files = typed.Files()
			}
			for _, file := range files {
				if file.Stubbed() || value.compaction >= msg.CompactionReference {
					continue
				}
				source := fileFromPreload
				if _, batch := value.value.(*msg.FileBatch); batch || file.IsToolResult() {
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

func wrapDomainMessages(messages []msg.Msg) []agentgo.AgentMessage {
	out := make([]agentgo.AgentMessage, len(messages))
	for i, message := range messages {
		out[i] = domainMessage{value: message, timestamp: time.Now(), compaction: msg.CompactionNone}
	}
	return out
}

func normalizeContextMessages(messages []agentgo.AgentMessage) ([]agentgo.AgentMessage, bool) {
	invocations := toolInvocations(messages)
	out := make([]agentgo.AgentMessage, 0, len(messages))
	changed := false
	for _, message := range messages {
		switch value := message.(type) {
		case domainMessage:
			cloned := msg.CloneAll([]msg.Msg{value.value})
			out = append(out, domainMessage{value: cloned[0], timestamp: value.timestamp, compaction: value.compaction})
		case agentgo.Message:
			changed = true
			wire := toLLMMessages([]agentgo.Message{value})
			if len(wire) == 0 {
				continue
			}
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

func countContextTokens(messages []agentgo.AgentMessage) int {
	var total int
	for _, message := range messages {
		total += llm.CountTokens(message.TextContent())
	}
	return total
}

func compactCoveredFiles(messages []agentgo.AgentMessage) (compacted int) {
	values := make([]msg.Msg, 0, len(messages))
	for _, message := range messages {
		if domain, ok := message.(domainMessage); ok && domain.compaction < msg.CompactionReference {
			values = append(values, domain.value)
		}
	}
	return msg.DedupFiles(values)
}

func appendVisibleFileInventory(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
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

func toolInvocations(messages []agentgo.AgentMessage) map[string]toolInvocation {
	out := make(map[string]toolInvocation)
	for _, message := range messages {
		var calls []llm.ToolCall
		switch value := message.(type) {
		case domainMessage:
			calls = value.value.ToLLM(value.compaction).ToolCalls
		case agentgo.Message:
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

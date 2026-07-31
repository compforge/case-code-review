package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/llmloop"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

// scanExecutor owns scan-wide execution aggregates. Each file still gets an
// isolated Harness Execution; scan orchestration and Finding semantics remain
// in the Runner layer.
type scanExecutor struct {
	llmClient               llm.LLMClient
	model                   string
	tools                   *tool.Registry
	toolDefs                []llm.ToolDef
	session                 *session.SessionHistory
	handler                 harness.ToolHandler
	maxTurns                int
	maxTokens               int
	wrapUpPrompt            string
	compressionSystemPrompt string
	compressionPrompt       string

	totalInputTokens      int64
	totalOutputTokens     int64
	totalCacheReadTokens  int64
	totalCacheWriteTokens int64

	warningsMu sync.Mutex
	warnings   []llmloop.Warning
	toolMu     sync.Mutex
	toolCalls  map[string]int64
	modelMu    sync.Mutex
	models     map[string]int
}

func newScanExecutor(args Args, handler harness.ToolHandler) *scanExecutor {
	systemPrompt, compressionPrompt := scanCompressionPrompts(args.Template)
	return &scanExecutor{
		llmClient:               args.LLMClient,
		model:                   args.Model,
		tools:                   args.Tools,
		toolDefs:                slices.Clone(args.MainToolDefs),
		session:                 args.Session,
		handler:                 handler,
		maxTurns:                args.Template.MaxToolRequestTimes,
		maxTokens:               args.Template.MaxTokens,
		wrapUpPrompt:            finding.WrapUpPrompt,
		compressionSystemPrompt: systemPrompt,
		compressionPrompt:       compressionPrompt,
	}
}

func (e *scanExecutor) Run(
	ctx context.Context,
	messages []msg.Msg,
	scope session.Scope,
) (harness.ExecutionResult, error) {
	run := &scanExecution{executor: e, ctx: ctx}
	result, err := harness.Execute(ctx, harness.ExecutionSpec{
		LLMClient:               e.llmClient,
		Model:                   e.model,
		Messages:                messages,
		ToolDefs:                e.toolDefs,
		Tools:                   e.tools,
		ToolHandler:             e.handler,
		Session:                 e.session,
		Scope:                   scope,
		TaskType:                session.MainTask,
		Events:                  run,
		MaxTurns:                e.maxTurns,
		MaxTokens:               e.maxTokens,
		ContextWindow:           e.maxTokens,
		WrapUpPrompt:            e.wrapUpPrompt,
		CompressionSystemPrompt: e.compressionSystemPrompt,
		CompressionPrompt:       e.compressionPrompt,
		CompressionUpdatePrompt: e.compressionPrompt,
		CompressionPrefixPrompt: e.compressionPrompt,
	})
	e.RecordUsage(&result.Usage)

	reason := result.Reason
	if result.State == harness.OutcomeTruncated && reason == "max_turns" {
		reason = "tool-round budget exhausted"
	}
	if result.State == harness.OutcomeTimeout {
		reason = "deadline exceeded"
	}
	if result.State != harness.OutcomeCompleted {
		e.RecordWarning(
			"unit_incomplete",
			scope.Path(),
			fmt.Sprintf("review ended without task_done (%s); verdict is partial — do not read as clean", reason),
		)
	}
	result.Reason = reason
	return result, err
}

func scanCompressionPrompts(tpl template.ScanTemplate) (string, string) {
	var system, instruction []string
	for _, message := range tpl.MemoryCompressionTask.Messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if message.Role == "system" {
			system = append(system, content)
			continue
		}
		content = strings.ReplaceAll(
			content,
			"{{context}}",
			"Summarize the conversation supplied above according to the system instructions.",
		)
		instruction = append(instruction, content)
	}
	return strings.Join(system, "\n\n"), strings.Join(instruction, "\n\n")
}

func (e *scanExecutor) RecordUsage(usage *llm.UsageInfo) {
	if usage == nil {
		return
	}
	atomic.AddInt64(&e.totalInputTokens, usage.PromptTokens)
	atomic.AddInt64(&e.totalOutputTokens, usage.CompletionTokens)
	atomic.AddInt64(&e.totalCacheReadTokens, usage.CacheReadTokens)
	atomic.AddInt64(&e.totalCacheWriteTokens, usage.CacheWriteTokens)
}

func (e *scanExecutor) TotalInputTokens() int64 {
	return atomic.LoadInt64(&e.totalInputTokens)
}

func (e *scanExecutor) TotalOutputTokens() int64 {
	return atomic.LoadInt64(&e.totalOutputTokens)
}

func (e *scanExecutor) TotalCacheReadTokens() int64 {
	return atomic.LoadInt64(&e.totalCacheReadTokens)
}

func (e *scanExecutor) TotalCacheWriteTokens() int64 {
	return atomic.LoadInt64(&e.totalCacheWriteTokens)
}

func (e *scanExecutor) TotalTokensUsed() int64 {
	return e.TotalInputTokens() + e.TotalOutputTokens()
}

func (e *scanExecutor) RecordWarning(warningType, file, message string) {
	e.warningsMu.Lock()
	e.warnings = append(e.warnings, llmloop.Warning{
		Type: warningType, File: file, Message: message,
	})
	e.warningsMu.Unlock()
}

func (e *scanExecutor) Warnings() []llmloop.Warning {
	e.warningsMu.Lock()
	defer e.warningsMu.Unlock()
	return slices.Clone(e.warnings)
}

func (e *scanExecutor) recordToolCall(name string) {
	e.toolMu.Lock()
	if e.toolCalls == nil {
		e.toolCalls = make(map[string]int64)
	}
	e.toolCalls[name]++
	e.toolMu.Unlock()
}

func (e *scanExecutor) ToolCalls() map[string]int64 {
	e.toolMu.Lock()
	defer e.toolMu.Unlock()
	out := make(map[string]int64, len(e.toolCalls))
	for name, count := range e.toolCalls {
		out[name] = count
	}
	return out
}

func (e *scanExecutor) recordModel(alias string) {
	if alias == "" {
		return
	}
	e.modelMu.Lock()
	if e.models == nil {
		e.models = make(map[string]int)
	}
	e.models[alias]++
	e.modelMu.Unlock()
}

func (e *scanExecutor) ModelsUsed() map[string]int {
	e.modelMu.Lock()
	defer e.modelMu.Unlock()
	out := make(map[string]int, len(e.models))
	for alias, count := range e.models {
		out[alias] = count
	}
	return out
}

func (e *scanExecutor) countableTool(name string) bool {
	if name == tool.TaskDone.Name() || e.tools == nil {
		return false
	}
	_, ok := e.tools.Get(name)
	return ok
}

type scanExecution struct {
	executor *scanExecutor
	ctx      context.Context
}

func (r *scanExecution) OnExecutionEvent(event harness.ExecutionEvent) {
	switch event.Type {
	case harness.EventModelResponse:
		r.executor.recordModel(event.Alias)
		model := event.Model
		if model == "" {
			model = r.executor.model
		}
		totalTokens := int64(0)
		if event.Usage != nil {
			totalTokens = event.Usage.TotalTokens
		}
		telemetry.RecordLLMRequest(r.ctx, model, event.Duration, totalTokens, "ok")
	case harness.EventToolStart:
		if r.executor.countableTool(event.Tool) {
			r.executor.recordToolCall(event.Tool)
		}
		if r.observesGenericTool(event.Tool) {
			var args map[string]any
			_ = json.Unmarshal(event.Arguments, &args)
			telemetry.PrintToolCallStarted(event.Tool, args)
		}
	case harness.EventToolEnd:
		if !r.observesGenericTool(event.Tool) {
			return
		}
		if event.IsError {
			telemetry.PrintToolCallError(event.Tool, fmt.Errorf("%s", scanEventResultText(event.Result)))
		} else {
			telemetry.PrintToolCallFinished(event.Tool, event.Duration)
		}
		telemetry.RecordToolCall(r.ctx, event.Tool, event.Duration, !event.IsError)
	}
}

func (r *scanExecution) observesGenericTool(name string) bool {
	return r.executor.countableTool(name) && name != finding.CodeComment.Name()
}

func scanEventResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

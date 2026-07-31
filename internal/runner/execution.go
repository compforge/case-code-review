package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/review"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

const (
	maxBulletinsPerExecution = 3
	maxBulletinTextRunes     = 300
)

type unitExecutionOutcome struct {
	State               string
	Reason              string
	BoardPulled         int
	BoardInjectedTokens int
	BoardPosted         int
}

// unitExecutor is the Runner-side assembly for one Harness execution. It owns
// review semantics such as Board publication and run-level aggregation; the
// Harness receives only its domain-neutral interfaces.
type unitExecutor struct {
	llmClient               llm.LLMClient
	model                   string
	tools                   *tool.Registry
	toolDefs                []llm.ToolDef
	session                 *session.SessionHistory
	handler                 harness.ToolHandler
	board                   *board.Registry
	postBulletin            bool
	maxTurns                int
	maxTokens               int
	fileDedup               bool
	fileEvict               bool
	wrapUpPrompt            string
	compressionSystemPrompt string
	compressionPrompt       string

	totalInputTokens      int64
	totalOutputTokens     int64
	totalCacheReadTokens  int64
	totalCacheWriteTokens int64

	warningsMu sync.Mutex
	warnings   []Warning
	toolMu     sync.Mutex
	toolCalls  map[string]int64
	modelMu    sync.Mutex
	models     map[string]int
}

func newUnitExecutor(
	args Args,
	handler harness.ToolHandler,
	sharedBoard *board.Registry,
) *unitExecutor {
	defs := review.InvestigationToolDefs(slices.Clone(args.MainToolDefs))
	compressionSystemPrompt, compressionPrompt := reviewCompressionPrompts(args)
	postBulletin := sharedBoard != nil && args.Features.Enabled(feature.PostBulletin)
	if !postBulletin {
		defs = slices.DeleteFunc(defs, func(def llm.ToolDef) bool {
			return def.Function.Name == tool.PostBulletin.Name()
		})
	}
	return &unitExecutor{
		llmClient:               args.LLMClient,
		model:                   args.Model,
		tools:                   args.Tools,
		toolDefs:                defs,
		session:                 args.Session,
		handler:                 handler,
		board:                   sharedBoard,
		postBulletin:            postBulletin,
		maxTurns:                args.Template.MaxToolRequestTimes,
		maxTokens:               args.Template.MaxTokens,
		fileDedup:               args.Features.Enabled(feature.FileDedup),
		fileEvict:               args.Features.Enabled(feature.FileEvict),
		wrapUpPrompt:            review.InvestigationWrapUpPrompt,
		compressionSystemPrompt: compressionSystemPrompt,
		compressionPrompt:       compressionPrompt,
	}
}

func (e *unitExecutor) Run(
	ctx context.Context,
	messages []msg.Msg,
	scope session.Scope,
) (unitExecutionOutcome, error) {
	run := &unitExecution{
		executor:       e,
		ctx:            ctx,
		scope:          scope,
		bulletinBudget: maxBulletinsPerExecution,
	}
	var turnContext harness.TurnContextProvider
	if e.board != nil {
		turnContext = run
	}
	result, err := harness.Execute(ctx, harness.ExecutionSpec{
		LLMClient:               e.llmClient,
		Model:                   e.model,
		Messages:                messages,
		ToolDefs:                e.toolDefs,
		Tools:                   e.tools,
		ToolHandler:             run,
		Session:                 e.session,
		Scope:                   scope,
		TaskType:                session.MainTask,
		Events:                  run,
		TurnContext:             turnContext,
		MaxTurns:                e.maxTurns,
		MaxTokens:               e.maxTokens,
		ContextWindow:           e.maxTokens,
		FileDedupEnabled:        e.fileDedup,
		FileEvictEnabled:        e.fileEvict,
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
	return unitExecutionOutcome{
		State:               result.State,
		Reason:              reason,
		BoardPulled:         int(atomic.LoadInt64(&run.boardPulled)),
		BoardInjectedTokens: int(atomic.LoadInt64(&run.boardTokens)),
		BoardPosted:         int(atomic.LoadInt64(&run.boardPosted)),
	}, err
}

func reviewCompressionPrompts(args Args) (string, string) {
	var system, instruction []string
	for _, message := range args.Template.MemoryCompressionTask.Messages {
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

func (e *unitExecutor) RecordUsage(usage *llm.UsageInfo) {
	if usage == nil {
		return
	}
	atomic.AddInt64(&e.totalInputTokens, usage.PromptTokens)
	atomic.AddInt64(&e.totalOutputTokens, usage.CompletionTokens)
	atomic.AddInt64(&e.totalCacheReadTokens, usage.CacheReadTokens)
	atomic.AddInt64(&e.totalCacheWriteTokens, usage.CacheWriteTokens)
}

func (e *unitExecutor) TotalInputTokens() int64 {
	return atomic.LoadInt64(&e.totalInputTokens)
}

func (e *unitExecutor) TotalOutputTokens() int64 {
	return atomic.LoadInt64(&e.totalOutputTokens)
}

func (e *unitExecutor) TotalCacheReadTokens() int64 {
	return atomic.LoadInt64(&e.totalCacheReadTokens)
}

func (e *unitExecutor) TotalCacheWriteTokens() int64 {
	return atomic.LoadInt64(&e.totalCacheWriteTokens)
}

func (e *unitExecutor) TotalTokensUsed() int64 {
	return e.TotalInputTokens() + e.TotalOutputTokens()
}

func (e *unitExecutor) RecordWarning(warningType, file, message string) {
	e.warningsMu.Lock()
	e.warnings = append(e.warnings, Warning{
		Type: warningType, File: file, Message: message,
	})
	e.warningsMu.Unlock()
}

func (e *unitExecutor) Warnings() []Warning {
	e.warningsMu.Lock()
	defer e.warningsMu.Unlock()
	return slices.Clone(e.warnings)
}

func (e *unitExecutor) recordToolCall(name string) {
	e.toolMu.Lock()
	if e.toolCalls == nil {
		e.toolCalls = make(map[string]int64)
	}
	e.toolCalls[name]++
	e.toolMu.Unlock()
}

func (e *unitExecutor) ToolCalls() map[string]int64 {
	e.toolMu.Lock()
	defer e.toolMu.Unlock()
	out := make(map[string]int64, len(e.toolCalls))
	for name, count := range e.toolCalls {
		out[name] = count
	}
	return out
}

func (e *unitExecutor) recordModel(alias string) {
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

func (e *unitExecutor) ModelsUsed() map[string]int {
	e.modelMu.Lock()
	defer e.modelMu.Unlock()
	out := make(map[string]int, len(e.models))
	for alias, count := range e.models {
		out[alias] = count
	}
	return out
}

func (e *unitExecutor) countableTool(name string) bool {
	if name == tool.TaskDone.Name() {
		return false
	}
	if name == review.ReportHypothesis.Name() {
		return true
	}
	if name == tool.PostBulletin.Name() {
		return e.postBulletin
	}
	if e.tools == nil {
		return false
	}
	_, ok := e.tools.Get(name)
	return ok
}

type unitExecution struct {
	executor *unitExecutor
	ctx      context.Context
	scope    session.Scope

	mu             sync.Mutex
	turn           int
	bulletinBudget int
	boardPulled    int64
	boardTokens    int64
	boardPosted    int64
}

func (r *unitExecution) PullTurnContext(_ context.Context, _ session.Scope) []msg.Msg {
	r.mu.Lock()
	r.turn++
	r.mu.Unlock()

	digest, count := r.executor.board.Pull(r.scope.ID)
	if count == 0 {
		return nil
	}
	atomic.AddInt64(&r.boardPulled, int64(count))
	atomic.AddInt64(&r.boardTokens, int64(llm.CountTokens(digest)))
	return []msg.Msg{msg.NewBoard(digest)}
}

func (r *unitExecution) HandleTool(
	ctx context.Context,
	request harness.ToolRequest,
) (tool.TaskCheckpoint, bool) {
	if request.Tool == tool.PostBulletin && r.executor.postBulletin {
		return r.postToBoard(request.Args), true
	}
	if r.executor.handler == nil {
		return tool.TaskCheckpoint{}, false
	}
	return r.executor.handler.HandleTool(ctx, request)
}

func (r *unitExecution) OnExecutionEvent(event harness.ExecutionEvent) {
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
		r.publishToolFact(event.Tool, event.Arguments)
	case harness.EventToolEnd:
		if !r.observesGenericTool(event.Tool) {
			return
		}
		if event.IsError {
			telemetry.PrintToolCallError(event.Tool, fmt.Errorf("%s", eventResultText(event.Result)))
		} else {
			telemetry.PrintToolCallFinished(event.Tool, event.Duration)
		}
		telemetry.RecordToolCall(r.ctx, event.Tool, event.Duration, !event.IsError)
	}
}

func (r *unitExecution) observesGenericTool(name string) bool {
	return r.executor.countableTool(name) &&
		name != review.ReportHypothesis.Name() &&
		name != tool.PostBulletin.Name()
}

func (r *unitExecution) postToBoard(args map[string]any) tool.TaskCheckpoint {
	text, _ := args["text"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return tool.Of("Error: post_bulletin requires a non-empty text.")
	}
	paths := argumentStrings(args["paths"])
	symbols := argumentStrings(args["symbols"])
	if len(paths) == 0 && len(symbols) == 0 {
		return tool.Of("Error: post_bulletin requires at least one routing key (paths or symbols) — without one the note cannot reach the peers reviewing that code.")
	}

	r.mu.Lock()
	if r.bulletinBudget <= 0 {
		r.mu.Unlock()
		return tool.Of("Bulletin budget for this unit is exhausted — note NOT posted. Continue the current task and use its result tool for final output.")
	}
	r.bulletinBudget--
	turn := r.turn
	r.mu.Unlock()

	runes := []rune(text)
	if len(runes) > maxBulletinTextRunes {
		text = string(runes[:maxBulletinTextRunes]) + "…"
	}
	r.executor.board.Publish(board.Bulletin{
		From: r.scope.ID, Turn: turn, Level: board.LevelObservation,
		Paths: paths, Symbols: symbols, Text: text,
	})
	atomic.AddInt64(&r.boardPosted, 1)
	return tool.Of("Posted to the team board as an unverified observation; peers reviewing the referenced code will see it. They cannot reply — continue your own review now.")
}

func (r *unitExecution) publishToolFact(name string, raw json.RawMessage) {
	if r.executor.board == nil {
		return
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return
	}

	var bulletin board.Bulletin
	switch name {
	case tool.FileRead.Name():
		path, _ := args["file_path"].(string)
		if path == "" {
			return
		}
		text := "read " + path
		start := argumentInt(args["start_line"])
		if start > 0 {
			text = fmt.Sprintf("read %s:%d-%d", path, start, argumentInt(args["end_line"]))
		}
		bulletin = board.Bulletin{
			Level: board.LevelConfirmed, Paths: []string{path}, Text: text,
		}
	default:
		return
	}

	r.mu.Lock()
	bulletin.From = r.scope.ID
	bulletin.Turn = r.turn
	r.mu.Unlock()
	r.executor.board.Publish(bulletin)
	atomic.AddInt64(&r.boardPosted, 1)
}

func argumentStrings(value any) []string {
	values, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return slices.Clone(strings)
		}
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func argumentInt(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func eventResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

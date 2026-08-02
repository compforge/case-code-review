package unitreview

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

const (
	maxBulletinsPerExecution = 3
	maxBulletinTextRunes     = 300
)

type Outcome struct {
	State               string
	Reason              string
	BoardPulled         int
	BoardInjectedTokens int
	BoardPosted         int
}

// Executor is the Runner-side assembly for one Unit Review execution. It owns
// review semantics such as Board publication and run-level aggregation; the
// Harness receives only its domain-neutral interfaces.
type Executor struct {
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
	warnings   []harness.Warning
	toolMu     sync.Mutex
	toolCalls  map[string]int64
	modelMu    sync.Mutex
	models     map[string]int
	readMu     sync.Mutex
	readPaths  map[string]map[string]bool
}

type ExecutorConfig struct {
	LLMClient               llm.LLMClient
	Model                   string
	Tools                   *tool.Registry
	ToolDefs                []llm.ToolDef
	Session                 *session.SessionHistory
	MaxTurns                int
	MaxTokens               int
	FileDedup               bool
	FileEvict               bool
	PostBulletin            bool
	CompressionSystemPrompt string
	CompressionPrompt       string
}

func NewExecutor(
	config ExecutorConfig,
	handler harness.ToolHandler,
	sharedBoard *board.Registry,
) *Executor {
	defs := InvestigationToolDefs(slices.Clone(config.ToolDefs))
	postBulletin := sharedBoard != nil && config.PostBulletin
	if !postBulletin {
		defs = slices.DeleteFunc(defs, func(def llm.ToolDef) bool {
			return def.Function.Name == tool.PostBulletin.Name()
		})
	}
	return &Executor{
		llmClient:               config.LLMClient,
		model:                   config.Model,
		tools:                   config.Tools,
		toolDefs:                defs,
		session:                 config.Session,
		handler:                 handler,
		board:                   sharedBoard,
		postBulletin:            postBulletin,
		maxTurns:                config.MaxTurns,
		maxTokens:               config.MaxTokens,
		fileDedup:               config.FileDedup,
		fileEvict:               config.FileEvict,
		wrapUpPrompt:            InvestigationWrapUpPrompt,
		compressionSystemPrompt: config.CompressionSystemPrompt,
		compressionPrompt:       config.CompressionPrompt,
	}
}

func (e *Executor) Run(
	ctx context.Context,
	messages []msg.Msg,
	scope session.Scope,
) (Outcome, error) {
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
	execution, err := harness.NewExecution(harness.ExecutionSpec{
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
		WrapUpAllowedTools:      []string{SubmitHypotheses.Name()},
		CompletionTool:          SubmitHypotheses.Name(),
		CompletionPrompt:        "The Unit Review is not complete until you call submit_hypotheses exactly once. Submit an empty hypotheses array when no material issue remains.",
		CompressionSystemPrompt: e.compressionSystemPrompt,
		CompressionPrompt:       e.compressionPrompt,
		CompressionUpdatePrompt: e.compressionPrompt,
		CompressionPrefixPrompt: e.compressionPrompt,
	})
	if err != nil {
		return Outcome{}, err
	}
	result, err := execution.Run(ctx)
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
			fmt.Sprintf("review ended without submit_hypotheses (%s); verdict is partial — do not read as clean", reason),
		)
	}
	return Outcome{
		State:               result.State,
		Reason:              reason,
		BoardPulled:         int(atomic.LoadInt64(&run.boardPulled)),
		BoardInjectedTokens: int(atomic.LoadInt64(&run.boardTokens)),
		BoardPosted:         int(atomic.LoadInt64(&run.boardPosted)),
	}, err
}

func (e *Executor) RecordUsage(usage *llm.UsageInfo) {
	if usage == nil {
		return
	}
	atomic.AddInt64(&e.totalInputTokens, usage.PromptTokens)
	atomic.AddInt64(&e.totalOutputTokens, usage.CompletionTokens)
	atomic.AddInt64(&e.totalCacheReadTokens, usage.CacheReadTokens)
	atomic.AddInt64(&e.totalCacheWriteTokens, usage.CacheWriteTokens)
}

func (e *Executor) TotalInputTokens() int64 {
	return atomic.LoadInt64(&e.totalInputTokens)
}

func (e *Executor) TotalOutputTokens() int64 {
	return atomic.LoadInt64(&e.totalOutputTokens)
}

func (e *Executor) TotalCacheReadTokens() int64 {
	return atomic.LoadInt64(&e.totalCacheReadTokens)
}

func (e *Executor) TotalCacheWriteTokens() int64 {
	return atomic.LoadInt64(&e.totalCacheWriteTokens)
}

func (e *Executor) TotalTokensUsed() int64 {
	return e.TotalInputTokens() + e.TotalOutputTokens()
}

func (e *Executor) CompressionSystemPrompt() string { return e.compressionSystemPrompt }

func (e *Executor) CompressionPrompt() string { return e.compressionPrompt }

func (e *Executor) RecordWarning(warningType, file, message string) {
	e.warningsMu.Lock()
	e.warnings = append(e.warnings, harness.Warning{
		Type: warningType, File: file, Message: message,
	})
	e.warningsMu.Unlock()
}

func (e *Executor) Warnings() []harness.Warning {
	e.warningsMu.Lock()
	defer e.warningsMu.Unlock()
	return slices.Clone(e.warnings)
}

func (e *Executor) RecordToolCall(name string) {
	e.toolMu.Lock()
	if e.toolCalls == nil {
		e.toolCalls = make(map[string]int64)
	}
	e.toolCalls[name]++
	e.toolMu.Unlock()
}

func (e *Executor) ToolCalls() map[string]int64 {
	e.toolMu.Lock()
	defer e.toolMu.Unlock()
	out := make(map[string]int64, len(e.toolCalls))
	for name, count := range e.toolCalls {
		out[name] = count
	}
	return out
}

func (e *Executor) RecordModel(alias string) {
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

func (e *Executor) ModelsUsed() map[string]int {
	e.modelMu.Lock()
	defer e.modelMu.Unlock()
	out := make(map[string]int, len(e.models))
	for alias, count := range e.models {
		out[alias] = count
	}
	return out
}

// ReadPaths returns the successful repository paths inspected by one Unit
// Review. Runner uses this runtime footprint only to relate Hypotheses; it is
// not promoted into Harness or the Hypothesis domain model.
func (e *Executor) ReadPaths(scopeID string) []string {
	e.readMu.Lock()
	defer e.readMu.Unlock()
	set := e.readPaths[scopeID]
	out := make([]string, 0, len(set))
	for path := range set {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func (e *Executor) recordReadPaths(scopeID, name string, arguments json.RawMessage, result string) {
	var args map[string]any
	if json.Unmarshal(arguments, &args) != nil {
		return
	}
	var paths []string
	switch name {
	case tool.FileRead.Name(), tool.FileReadBase.Name():
		requests, err := tool.ParseFileReadRequests(args)
		if err != nil {
			return
		}
		parts, ok := tool.DecodeFileReadResults(result)
		if !ok || len(parts) != len(requests) {
			return
		}
		for i, request := range requests {
			if request.FilePath != "" && strings.Contains(parts[i], "File: ") {
				paths = append(paths, request.FilePath)
			}
		}
	case tool.FileReadDiff.Name():
		paths = append(paths, argumentStrings(args["path_array"])...)
	default:
		return
	}
	if len(paths) == 0 {
		return
	}
	e.readMu.Lock()
	if e.readPaths == nil {
		e.readPaths = make(map[string]map[string]bool)
	}
	if e.readPaths[scopeID] == nil {
		e.readPaths[scopeID] = make(map[string]bool)
	}
	for _, path := range paths {
		e.readPaths[scopeID][path] = true
	}
	e.readMu.Unlock()
}

func (e *Executor) countableTool(name string) bool {
	if name == tool.TaskDone.Name() {
		return false
	}
	if name == SubmitHypotheses.Name() {
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
	executor *Executor
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
	return []msg.Msg{NewBoardDigest(digest)}
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
		r.executor.RecordModel(event.Alias)
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
			r.executor.RecordToolCall(event.Tool)
		}
		if r.observesGenericTool(event.Tool) {
			var args map[string]any
			_ = json.Unmarshal(event.Arguments, &args)
			telemetry.PrintToolCallStarted(event.Tool, args)
		}
		r.publishToolFact(event.Tool, event.Arguments)
	case harness.EventToolEnd:
		if !event.IsError && !strings.HasPrefix(strings.TrimSpace(eventResultText(event.Result)), "Error:") {
			r.executor.recordReadPaths(r.scope.ID, event.Tool, event.Arguments, eventResultText(event.Result))
		}
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
		name != SubmitHypotheses.Name() &&
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
		requests, err := tool.ParseFileReadRequests(args)
		if err != nil {
			return
		}
		paths := make([]string, 0, len(requests))
		for _, request := range requests {
			if request.FilePath != "" {
				paths = append(paths, request.FilePath)
			}
		}
		if len(paths) == 0 {
			return
		}
		bulletin = board.Bulletin{
			Level: board.LevelConfirmed, Paths: paths,
			Text: fmt.Sprintf("read %d file range(s): %s", len(requests), strings.Join(paths, ", ")),
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

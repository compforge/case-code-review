package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/compforge/agentgo"

	"github.com/compforge/go-stdx/uuid"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

const defaultCompletionPrompt = "The review is not complete until you call task_done. Finish any required result tool calls, then call task_done."

const (
	OutcomeCompleted = "completed"
	OutcomeTruncated = "truncated"
	OutcomeTimeout   = "timeout"
	OutcomeAborted   = "aborted"
	OutcomeLLMError  = "llm_error"
)

// ExecutionSpec is the immutable input for one Harness execution.
//
// +spec=`Messages contains full AgentMessages; Harness must not pre-project them before AgentGo ContextManager requests compaction`
// +case:id=full_messages_enter_harness,desc=`execution starts with compactable domain messages`,expect=`first unconstrained model request sees full content and Raw remains full after later compaction`
type ExecutionSpec struct {
	LLMClient        llm.LLMClient
	Model            string
	Messages         []agentgo.AgentMessage
	ToolDefs         []llm.ToolDef
	Tools            *tool.Registry
	ToolHandler      ToolHandler
	Session          *session.SessionHistory
	Scope            session.Scope
	TaskType         session.TaskType
	Events           EventSink
	TurnContext      TurnContextProvider
	MaxTurns         int
	MaxTokens        int
	ContextWindow    int
	FileDedupEnabled bool
	FileEvictEnabled bool
	WrapUpPrompt     string
	// WrapUpAfterTurns ends open-ended investigation after this many complete
	// turns. Zero preserves the default behavior of reserving only the final
	// turns or deadline window for completion.
	WrapUpAfterTurns int
	// WrapUpAllowedTools are result-submission tools advertised alongside the
	// completion tool on the first wrap-up request. A final corrective request
	// advertises only the completion tool. Nil leaves tool visibility unchanged
	// for callers that only need a textual reminder.
	WrapUpAllowedTools []string
	// CompletionTool is the domain-selected terminal tool. Empty defaults to
	// task_done unless NaturalCompletion is enabled. A non-default tool completes
	// only when its ToolHandler returns a completed checkpoint, so invalid
	// submissions remain recoverable.
	CompletionTool   string
	CompletionPrompt string
	// NaturalCompletion lets a final assistant turn complete the execution
	// without a terminal tool. Result tools remain non-terminal during normal
	// work; once wrap-up closes investigation, a successfully accepted allowed
	// result also ends the run without spending another model turn.
	NaturalCompletion bool
	// ContinueFrom resumes with the exact committed context of an earlier
	// ExecutionResult. The context remains opaque outside Harness.
	ContinueFrom *ExecutionResult
	// CompletionCheck lets a domain reject its completion tool until required
	// outputs exist. Harness owns the stop mechanics but does not interpret
	// what "complete" means for the caller's execution.
	CompletionCheck         func(context.Context) (complete bool, guidance string)
	CompressionSystemPrompt string
	CompressionPrompt       string
	CompressionUpdatePrompt string
	CompressionPrefixPrompt string
}

// ExecutionResult contains runtime facts only. Unit coverage and Finding
// judgment remain Runner responsibilities.
type ExecutionResult struct {
	ID         string
	Duration   time.Duration
	State      string
	Reason     string
	Usage      llm.UsageInfo
	Turns      int
	ToolCalls  int
	ToolErrors int

	context []agentgo.AgentMessage
}

// Execution owns one Harness run from immutable input through terminal result.
// AgentGo, context projection, recording, tools, and completion state remain
// private children of this lifecycle; callers only construct and Run it.
type Execution struct {
	id   string
	spec ExecutionSpec

	recorder             *executionRecorder
	contextManager       *contextManager
	model                *chatModel
	tools                []agentgo.Tool
	turns                *turnController
	completionTool       string
	completionPrompt     string
	naturalCompletion    bool
	wrapUpAllowed        map[string]bool
	wrapUpResultAccepted atomic.Bool
	// wrapUpRequestCount advances once per logical model call after wrap-up.
	// AgentGo retries reuse the same call options, so retries do not consume the
	// single corrective request.
	wrapUpRequestCount atomic.Int32
	// wrapUpFinalTurnGranted bounds a model that ignores the hard close: after
	// one corrective completion turn, another non-terminal response stops as
	// truncated instead of spending the remaining provider turn budget.
	wrapUpFinalTurnGranted atomic.Bool
	wrapUpForcedStop       atomic.Bool

	started   atomic.Bool
	completed atomic.Bool
	summary   *agentgo.RunSummary
	runErr    error

	contextMu       sync.Mutex
	contextMessages []agentgo.AgentMessage
}

// NewExecution validates and assembles one isolated Harness execution.
func NewExecution(spec ExecutionSpec) (*Execution, error) {
	if spec.LLMClient == nil {
		return nil, fmt.Errorf("harness: LLM client is required")
	}
	if len(spec.Messages) == 0 {
		return nil, fmt.Errorf("harness: at least one message is required")
	}

	// The execution owns its input snapshot; later caller mutations cannot
	// change a run that has already been assembled.
	spec.Messages = rawMessages(spec.Messages)
	e := &Execution{
		id:                uuid.V4(),
		spec:              spec,
		completionTool:    spec.CompletionTool,
		completionPrompt:  spec.CompletionPrompt,
		naturalCompletion: spec.NaturalCompletion,
	}
	if e.completionTool == "" && !e.naturalCompletion {
		e.completionTool = tool.TaskDone.Name()
	}
	if e.completionPrompt == "" && !e.naturalCompletion {
		if e.completionTool == tool.TaskDone.Name() {
			e.completionPrompt = defaultCompletionPrompt
		} else {
			e.completionPrompt = fmt.Sprintf(
				"The task is not complete until you successfully call %s.", e.completionTool,
			)
		}
	}
	if e.completionTool != "" && e.completionTool != tool.TaskDone.Name() && !hasToolDef(spec.ToolDefs, e.completionTool) {
		return nil, fmt.Errorf("harness: completion tool %q is not defined", e.completionTool)
	}
	e.recorder = newExecutionRecorder(spec, e.id)
	taskType := spec.TaskType
	if taskType == "" {
		taskType = session.MainTask
	}
	e.model = &chatModel{
		client:    spec.LLMClient,
		model:     spec.Model,
		maxTokens: spec.MaxTokens,
		recorder:  e.recorder,
		taskType:  taskType,
		events:    true,
	}
	contextModel := &chatModel{
		client:    spec.LLMClient,
		model:     spec.Model,
		maxTokens: spec.MaxTokens,
		recorder:  e.recorder,
		taskType:  session.MemoryCompressionTask,
	}
	e.contextManager = newContextManager(spec, contextModel)
	e.turns = newTurnController(spec)
	e.tools = adaptTools(spec.ToolDefs, spec.Tools, &e.completed, e.completionTool)
	if len(spec.WrapUpAllowedTools) > 0 {
		e.wrapUpAllowed = make(map[string]bool, len(spec.WrapUpAllowedTools))
		for _, name := range spec.WrapUpAllowedTools {
			e.wrapUpAllowed[name] = true
		}
		if e.completionTool != "" {
			e.wrapUpAllowed[e.completionTool] = true
		}
		// AgentGo keeps one immutable execution tool registry. Projecting schemas
		// at the model adapter changes what the model can choose without changing
		// the local execution lookup; middleware remains the enforcement fallback.
		e.model.toolProjection = e.projectWrapUpTools
	}
	return e, nil
}

func rawMessages(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
	out := make([]agentgo.AgentMessage, len(messages))
	for i, message := range messages {
		out[i] = message.Raw()
	}
	return out
}

// Run drives the Execution to one terminal result. An Execution is single-use:
// reusing it would mix recorder, context, and completion state across loops.
func (e *Execution) Run(ctx context.Context) (ExecutionResult, error) {
	if !e.started.CompareAndSwap(false, true) {
		return ExecutionResult{}, fmt.Errorf("harness: execution has already run")
	}

	startedAt := time.Now()
	e.recorder.startExecution()
	config := agentgo.LoopConfig{
		Model:                    e.model,
		MaxTurns:                 e.spec.MaxTurns,
		ContextManager:           e.contextManager,
		ToolResultMessageFactory: e.toolResultMessage,
		CommitContext:            e.replaceContext,
		CommitMessage:            e.appendContext,
		BeforeTurn:               e.turns.BeforeTurn,
		StopAfterTool:            e.shouldStopAfterTool,
		StopGuard:                e.stopGuard,
		ModelMiddlewares:         []agentgo.ModelMiddleware{e.modelMiddleware()},
		ToolMiddlewares:          []agentgo.ToolMiddleware{e.toolMiddleware()},
	}

	history := e.continuationContext()
	e.replaceContext(history, nil)
	e.contextManager.Sync(history)
	events := agentgo.AgentLoop(
		ctx,
		e.spec.Messages,
		agentgo.AgentContext{Messages: history, Tools: e.tools},
		config,
	)
	for event := range events {
		emitExecutionEvent(e.spec.Events, e.recorder, event)
		switch event.Type {
		case agentgo.EventError:
			if event.Err != nil {
				e.runErr = event.Err
			}
		case agentgo.EventToolExecEnd:
			if event.Tool != tool.TaskDone.Name() {
				e.recorder.finishTool(event.ToolID, event.Tool, event.Result, event.IsError)
			}
		case agentgo.EventAgentEnd:
			e.summary = event.Summary
		}
	}
	result, err := e.finish(ctx)
	result.ID = e.id
	result.Duration = time.Since(startedAt)
	taskType := e.spec.TaskType
	if taskType == "" {
		taskType = session.MainTask
	}
	e.recorder.finishExecution(taskType, result, result.Duration)
	return result, err
}

func (e *Execution) modelMiddleware() agentgo.ModelMiddleware {
	return func(
		ctx context.Context,
		execution agentgo.ModelExecution,
		next agentgo.ModelExecuteFunc,
	) (agentgo.ModelResult, error) {
		if execution.Kind != agentgo.ExecutionKindModel {
			return next(ctx, execution)
		}
		execution.Options = append(execution.Options, e.modelCallOptions(execution)...)
		return next(ctx, execution)
	}
}

func (e *Execution) modelCallOptions(execution agentgo.ModelExecution) []agentgo.CallOption {
	if !e.turns.WrapUpIssued() {
		return nil
	}
	wrapUpRequest := int32(0)
	if len(e.wrapUpAllowed) > 0 {
		// Model middleware runs for every physical retry. Advance the wrap-up
		// request only on the first attempt so retries preserve the logical call.
		if execution.Attempt <= 1 {
			wrapUpRequest = e.wrapUpRequestCount.Add(1)
		} else {
			wrapUpRequest = e.wrapUpRequestCount.Load()
		}
		if wrapUpRequest > 1 {
			// A result-only first response may naturally continue without consulting
			// StopGuard. Mark this request as the one final correction either way.
			e.wrapUpFinalTurnGranted.Store(true)
		}
	}
	if e.naturalCompletion {
		return nil
	}
	// The first wrap-up request must still be able to flush result tools before
	// completion. Only the single corrective turn targets the terminal tool
	// itself; forcing it earlier would skip valid final results such as comments.
	if (wrapUpRequest > 1 || e.wrapUpFinalTurnGranted.Load()) && e.completionTool != "" {
		return []agentgo.CallOption{agentgo.WithToolChoice(map[string]any{
			"type": "tool",
			"name": e.completionTool,
		})}
	}
	return []agentgo.CallOption{agentgo.WithToolChoice("required")}
}

func (e *Execution) projectWrapUpTools(tools []agentgo.ToolSpec) []agentgo.ToolSpec {
	request := e.wrapUpRequestCount.Load()
	if request == 0 {
		return tools
	}
	if request == 1 {
		return filterToolSpecs(tools, e.wrapUpAllowed)
	}
	if e.completionTool == "" {
		return nil
	}
	return filterToolSpecs(tools, map[string]bool{e.completionTool: true})
}

func filterToolSpecs(tools []agentgo.ToolSpec, allowed map[string]bool) []agentgo.ToolSpec {
	out := make([]agentgo.ToolSpec, 0, len(allowed))
	for _, spec := range tools {
		if allowed[spec.Name] {
			out = append(out, spec)
		}
	}
	return out
}

func (e *Execution) shouldStopAfterTool(name string) bool {
	if name == e.completionTool && e.completed.Load() {
		return true
	}
	if !e.turns.WrapUpIssued() {
		return false
	}
	if e.naturalCompletion && e.wrapUpAllowed[name] {
		return true
	}
	// A blocked investigation call, or an invalid completion call during
	// wrap-up, should immediately enter the single corrective final turn.
	return name == e.completionTool || (len(e.wrapUpAllowed) > 0 && !e.wrapUpAllowed[name])
}

func (e *Execution) stopGuard(_ context.Context, stop agentgo.StopInfo) agentgo.StopDecision {
	if e.completed.Load() {
		return agentgo.StopDecision{Allow: true}
	}
	if e.naturalCompletion {
		if stop.Trigger == agentgo.StopTriggerEndTurn || e.wrapUpResultAccepted.Load() {
			return agentgo.StopDecision{Allow: true}
		}
	}
	if !e.turns.WrapUpIssued() {
		return agentgo.StopDecision{InjectMessage: e.completionPrompt}
	}
	if e.wrapUpFinalTurnGranted.CompareAndSwap(false, true) {
		return agentgo.StopDecision{InjectMessage: e.spec.WrapUpPrompt + "\n" + e.completionPrompt}
	}
	e.wrapUpForcedStop.Store(true)
	return agentgo.StopDecision{Allow: true}
}

func (e *Execution) toolResultMessage(call agentgo.ToolCall, result agentgo.ToolResult) agentgo.AgentMessage {
	var args map[string]any
	_ = json.Unmarshal(call.Args, &args)
	decoded := msg.FromLLM(msg.LLMToolResult{
		Tool: call.Name, ToolCallID: call.ID, Arguments: args,
		Content: string(result.Content), IsError: result.IsError, Timestamp: time.Now(),
	})
	return decoded
}

func (e *Execution) continuationContext() []agentgo.AgentMessage {
	if e.spec.ContinueFrom == nil {
		return nil
	}
	return append([]agentgo.AgentMessage(nil), e.spec.ContinueFrom.context...)
}

func (e *Execution) appendContext(message agentgo.AgentMessage) error {
	e.contextMu.Lock()
	defer e.contextMu.Unlock()
	e.contextMessages = append(e.contextMessages, message)
	return nil
}

func (e *Execution) replaceContext(messages []agentgo.AgentMessage, _ *agentgo.ContextUsage) error {
	e.contextMu.Lock()
	defer e.contextMu.Unlock()
	e.contextMessages = append([]agentgo.AgentMessage(nil), messages...)
	return nil
}

func (e *Execution) contextSnapshot() []agentgo.AgentMessage {
	e.contextMu.Lock()
	defer e.contextMu.Unlock()
	return append([]agentgo.AgentMessage(nil), e.contextMessages...)
}

func (e *Execution) toolMiddleware() agentgo.ToolMiddleware {
	return func(
		ctx context.Context,
		execution agentgo.ToolExecution,
		next agentgo.ToolExecuteFunc,
	) (agentgo.ToolResult, error) {
		call := execution.Call
		started := time.Now()
		defer func() {
			e.recorder.finishToolExecution(call.ID, time.Since(started))
		}()
		if e.turns.WrapUpIssued() && len(e.wrapUpAllowed) > 0 && !e.wrapUpAllowed[call.Name] {
			return handledToolResult(call, json.RawMessage(
				"Investigation is closed. Do not repeat results already accepted. Submit only supported results that have not yet been accepted; if none remain, finish without another tool call.",
			)), nil
		}
		if call.Name == e.completionTool && e.spec.CompletionCheck != nil {
			complete, guidance := e.spec.CompletionCheck(ctx)
			if !complete {
				if guidance == "" {
					guidance = e.completionPrompt
				}
				return handledToolResult(call, json.RawMessage(guidance)), nil
			}
		}
		var args map[string]any
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return next(ctx, execution)
		}
		execute := func(raw json.RawMessage, parsed map[string]any) (agentgo.ToolResult, error) {
			if e.spec.ToolHandler == nil {
				execution.Call.Args = raw
				return next(ctx, execution)
			}
			recorded := e.recorder.call(call.ID)
			checkpoint, handled := e.spec.ToolHandler.HandleTool(ctx, ToolRequest{
				Scope: e.spec.Scope,
				Tool:  tool.OfName(call.Name),
				Call: llm.ToolCall{
					ID:   call.ID,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      call.Name,
						Arguments: string(raw),
					},
				},
				Args:  parsed,
				Alias: recorded.alias,
			})
			if !handled {
				execution.Call.Args = raw
				return next(ctx, execution)
			}
			if checkpoint.Completed {
				if call.Name == e.completionTool {
					e.completed.Store(true)
				}
				if e.naturalCompletion && e.turns.WrapUpIssued() && e.wrapUpAllowed[call.Name] {
					e.wrapUpResultAccepted.Store(true)
				}
				if checkpoint.Data != "" {
					return handledToolResult(call, json.RawMessage(checkpoint.Data)), nil
				}
				return handledToolResult(call, json.RawMessage("Task completed successfully.")), nil
			}
			return handledToolResult(call, json.RawMessage(checkpoint.Data)), nil
		}

		if call.Name != tool.FileRead.Name() {
			return execute(call.Args, args)
		}
		requests, err := tool.ParseFileReadRequests(args)
		if err != nil {
			return execute(call.Args, args)
		}
		results := make([]string, len(requests))
		remaining := make([]tool.FileReadRequest, 0, len(requests))
		positions := make([]int, 0, len(requests))
		for i, request := range requests {
			if result, covered := e.contextManager.coveredFileRead(request); covered {
				results[i] = result
				continue
			}
			remaining = append(remaining, request)
			positions = append(positions, i)
		}
		if len(remaining) == 0 {
			return handledToolResult(call, json.RawMessage(tool.EncodeFileReadResults(results))), nil
		}
		subset := tool.FileReadArgs(remaining)
		raw, _ := json.Marshal(subset)
		response, err := execute(raw, subset)
		if err != nil || len(remaining) == len(requests) {
			return response, err
		}
		fresh, ok := tool.DecodeFileReadResults(string(response.Content))
		if !ok || len(fresh) != len(remaining) {
			return response, nil
		}
		for i, result := range fresh {
			results[positions[i]] = result
		}
		response.Content = json.RawMessage(tool.EncodeFileReadResults(results))
		return response, nil
	}
}

func handledToolResult(call agentgo.ToolCall, content json.RawMessage) agentgo.ToolResult {
	return agentgo.ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    content,
	}
}

func (e *Execution) finish(ctx context.Context) (ExecutionResult, error) {
	result := ExecutionResult{Usage: e.recorder.Usage()}
	if e.summary != nil {
		result.Turns = e.summary.TurnCount
		result.ToolCalls = e.summary.ToolCalls
		result.ToolErrors = e.summary.ToolErrors
	}
	result.context = e.contextSnapshot()
	if e.completed.Load() && e.summary != nil && e.summary.EndReason == agentgo.EndReasonStop {
		result.State = OutcomeCompleted
		return result, nil
	}
	if e.wrapUpForcedStop.Load() {
		result.State = OutcomeTruncated
		result.Reason = "wrap-up completion not submitted"
		return result, nil
	}
	if e.naturalCompletion && e.summary != nil && e.summary.EndReason == agentgo.EndReasonStop {
		result.State = OutcomeCompleted
		return result, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.State = OutcomeTimeout
		result.Reason = context.DeadlineExceeded.Error()
		return result, ctx.Err()
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		result.State = OutcomeAborted
		result.Reason = context.Canceled.Error()
		return result, ctx.Err()
	}
	if e.summary != nil && e.summary.EndReason == agentgo.EndReasonMaxTurns {
		result.State = OutcomeTruncated
		result.Reason = string(e.summary.EndReason)
		return result, nil
	}
	if errors.Is(e.runErr, llm.ErrTokenBudget) {
		result.State = OutcomeTruncated
		result.Reason = llm.ErrTokenBudget.Error()
		return result, nil
	}
	if e.runErr != nil {
		result.State = OutcomeLLMError
		result.Reason = e.runErr.Error()
		return result, e.runErr
	}
	if e.summary == nil {
		err := fmt.Errorf("harness: agentgo ended without a run summary")
		result.State = OutcomeLLMError
		result.Reason = err.Error()
		return result, err
	}

	result.State = OutcomeTruncated
	result.Reason = string(e.summary.EndReason)
	return result, nil
}

func wireToAgentMessage(message llm.Message) agentgo.Message {
	content := []agentgo.ContentBlock{agentgo.TextBlock(message.ExtractText())}
	for _, call := range message.ToolCalls {
		args := json.RawMessage(call.Function.Arguments)
		content = append(content, agentgo.ToolCallBlock(agentgo.ToolCall{
			ID:   call.ID,
			Name: call.Function.Name,
			Args: args,
		}))
	}
	agentMessage := agentgo.Message{
		Role:    agentgo.Role(message.Role),
		Content: content,
	}
	if message.ToolCallID != "" {
		agentMessage.Metadata = map[string]any{"tool_call_id": message.ToolCallID}
	}
	return agentMessage
}

type adaptedTool struct {
	def      llm.FunctionDef
	provider tool.Provider
}

func (t adaptedTool) Name() string           { return t.def.Name }
func (t adaptedTool) Description() string    { return t.def.Description }
func (t adaptedTool) Schema() map[string]any { return t.def.Parameters }
func (t adaptedTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if t.provider == nil {
		return json.RawMessage(tool.NotAvailableMsg), nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("parse %s arguments: %w", t.def.Name, err)
	}
	result, err := t.provider.Execute(ctx, args)
	return json.RawMessage(result), err
}

type taskDoneTool struct {
	def       llm.FunctionDef
	completed *atomic.Bool
}

func (t taskDoneTool) Name() string           { return tool.TaskDone.Name() }
func (t taskDoneTool) Description() string    { return t.def.Description }
func (t taskDoneTool) Schema() map[string]any { return t.def.Parameters }
func (t taskDoneTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	t.completed.Store(true)
	return json.RawMessage("Task completed successfully."), nil
}

func adaptTools(
	defs []llm.ToolDef,
	registry *tool.Registry,
	completed *atomic.Bool,
	completionTool string,
) []agentgo.Tool {
	out := make([]agentgo.Tool, 0, len(defs)+1)
	hasTaskDone := false
	for _, def := range defs {
		if def.Function.Name == tool.TaskDone.Name() && completionTool == tool.TaskDone.Name() {
			hasTaskDone = true
			out = append(out, taskDoneTool{def: normalizedToolDef(def.Function), completed: completed})
			continue
		}
		var provider tool.Provider
		if registry != nil {
			provider, _ = registry.Get(def.Function.Name)
		}
		out = append(out, adaptedTool{def: normalizedToolDef(def.Function), provider: provider})
	}
	if completionTool == tool.TaskDone.Name() && !hasTaskDone {
		out = append(out, taskDoneTool{
			def: normalizedToolDef(llm.FunctionDef{
				Name:        tool.TaskDone.Name(),
				Description: "Signal that the current task is complete.",
			}),
			completed: completed,
		})
	}
	return out
}

func hasToolDef(defs []llm.ToolDef, name string) bool {
	for _, def := range defs {
		if def.Function.Name == name {
			return true
		}
	}
	return false
}

func normalizedToolDef(def llm.FunctionDef) llm.FunctionDef {
	if def.Parameters == nil {
		def.Parameters = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return def
}

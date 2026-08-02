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

// ExecutionSpec is the immutable input for one Harness execution. It uses CCR
// types at the boundary so callers never need to import agentgo directly.
type ExecutionSpec struct {
	LLMClient        llm.LLMClient
	Model            string
	Messages         []msg.Msg
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
	// WrapUpAllowedTools hard-closes investigation after WrapUpPrompt is
	// injected without changing the advertised tool schemas. Nil leaves tool
	// execution unchanged for callers that only need a textual reminder.
	WrapUpAllowedTools []string
	CompletionPrompt   string
	// ContinueFrom resumes with the exact committed context of an earlier
	// ExecutionResult. The context remains opaque outside Harness.
	ContinueFrom *ExecutionResult
	// CompletionCheck lets a domain reject task_done until its required
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
	spec ExecutionSpec

	recorder         *executionRecorder
	contextManager   *contextManager
	model            *chatModel
	tools            []agentgo.Tool
	completionPrompt string
	wrapUpAllowed    map[string]bool

	started   atomic.Bool
	completed atomic.Bool
	summary   *agentgo.RunSummary
	runErr    error

	contextMu       sync.Mutex
	contextMessages []agentgo.AgentMessage
}

// NewExecution validates and assembles one isolated Harness execution without
// exposing AgentGo types to the review domain.
func NewExecution(spec ExecutionSpec) (*Execution, error) {
	if spec.LLMClient == nil {
		return nil, fmt.Errorf("harness: LLM client is required")
	}
	if len(spec.Messages) == 0 {
		return nil, fmt.Errorf("harness: at least one message is required")
	}

	// The execution owns its input snapshot; later caller mutations cannot
	// change a run that has already been assembled.
	spec.Messages = msg.CloneAll(spec.Messages)
	e := &Execution{spec: spec, completionPrompt: spec.CompletionPrompt}
	if e.completionPrompt == "" {
		e.completionPrompt = defaultCompletionPrompt
	}
	e.recorder = newExecutionRecorder(spec)
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
	e.tools = adaptTools(spec.ToolDefs, spec.Tools, &e.completed)
	if len(spec.WrapUpAllowedTools) > 0 {
		e.wrapUpAllowed = make(map[string]bool, len(spec.WrapUpAllowedTools))
		for _, name := range spec.WrapUpAllowedTools {
			e.wrapUpAllowed[name] = true
		}
		e.wrapUpAllowed[tool.TaskDone.Name()] = true
	}
	return e, nil
}

// Run drives the Execution to one terminal result. An Execution is single-use:
// reusing it would mix recorder, context, and completion state across loops.
func (e *Execution) Run(ctx context.Context) (ExecutionResult, error) {
	if !e.started.CompareAndSwap(false, true) {
		return ExecutionResult{}, fmt.Errorf("harness: execution has already run")
	}

	config := agentgo.LoopConfig{
		Model:                    e.model,
		MaxTurns:                 e.spec.MaxTurns,
		ContextManager:           e.contextManager,
		ToolResultMessageFactory: e.toolResultMessage,
		CommitContext:            e.replaceContext,
		CommitMessage:            e.appendContext,
		StopAfterTool: func(name string) bool {
			return name == tool.TaskDone.Name() && e.completed.Load()
		},
		StopGuard: func(_ context.Context, _ agentgo.StopInfo) agentgo.StopDecision {
			if e.completed.Load() {
				return agentgo.StopDecision{Allow: true}
			}
			return agentgo.StopDecision{InjectMessage: e.completionPrompt}
		},
		Middlewares: []agentgo.ToolMiddleware{e.toolMiddleware()},
	}

	history := e.continuationContext()
	e.replaceContext(history, nil)
	e.contextManager.Sync(history)
	events := agentgo.AgentLoop(
		ctx,
		wrapDomainMessages(e.spec.Messages),
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
	return e.finish(ctx)
}

func (e *Execution) toolResultMessage(call agentgo.ToolCall, result agentgo.ToolResult) agentgo.AgentMessage {
	var args map[string]any
	_ = json.Unmarshal(call.Args, &args)
	decoded := msg.FromLLM(msg.LLMToolResult{
		Tool: call.Name, ToolCallID: call.ID, Arguments: args,
		Content: string(result.Content), IsError: result.IsError,
	})
	return domainMessage{value: decoded, timestamp: time.Now()}
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
		call agentgo.ToolCall,
		next agentgo.ToolExecuteFunc,
	) (json.RawMessage, error) {
		started := time.Now()
		defer func() {
			e.recorder.finishToolExecution(call.ID, time.Since(started))
		}()
		if e.contextManager.WrapUpIssued() && len(e.wrapUpAllowed) > 0 && !e.wrapUpAllowed[call.Name] {
			return json.RawMessage(
				"Investigation is closed. Submit the results already supported by the current context, then finish the task.",
			), nil
		}
		if call.Name == tool.TaskDone.Name() && e.spec.CompletionCheck != nil {
			complete, guidance := e.spec.CompletionCheck(ctx)
			if !complete {
				if guidance == "" {
					guidance = e.completionPrompt
				}
				return json.RawMessage(guidance), nil
			}
		}
		var args map[string]any
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return next(ctx, call.Args)
		}
		if call.Name == tool.FileRead.Name() {
			if result, covered := e.contextManager.coveredFileRead(args); covered {
				return json.RawMessage(result), nil
			}
		}
		if e.spec.ToolHandler == nil {
			return next(ctx, call.Args)
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
					Arguments: string(call.Args),
				},
			},
			Args:  args,
			Alias: recorded.alias,
		})
		if !handled {
			return next(ctx, call.Args)
		}
		if checkpoint.Completed {
			return json.RawMessage("Task completed successfully."), nil
		}
		return json.RawMessage(checkpoint.Data), nil
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

func adaptTools(defs []llm.ToolDef, registry *tool.Registry, completed *atomic.Bool) []agentgo.Tool {
	out := make([]agentgo.Tool, 0, len(defs)+1)
	hasTaskDone := false
	for _, def := range defs {
		if def.Function.Name == tool.TaskDone.Name() {
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
	if !hasTaskDone {
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

func normalizedToolDef(def llm.FunctionDef) llm.FunctionDef {
	if def.Parameters == nil {
		def.Parameters = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return def
}

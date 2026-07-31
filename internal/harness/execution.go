package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"

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
// types at the boundary so callers never need to import agentcore directly.
type ExecutionSpec struct {
	LLMClient               llm.LLMClient
	Model                   string
	Messages                []msg.Msg
	ToolDefs                []llm.ToolDef
	Tools                   *tool.Registry
	ToolHandler             ToolHandler
	Session                 *session.SessionHistory
	Scope                   session.Scope
	TaskType                session.TaskType
	Events                  EventSink
	TurnContext             TurnContextProvider
	MaxTurns                int
	MaxTokens               int
	ContextWindow           int
	FileDedupEnabled        bool
	FileEvictEnabled        bool
	WrapUpPrompt            string
	CompletionPrompt        string
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
}

// Execute runs one isolated agentcore loop without exposing agentcore to the
// review domain. Runner supplies domain behavior through Harness contracts.
func Execute(ctx context.Context, spec ExecutionSpec) (ExecutionResult, error) {
	if spec.LLMClient == nil {
		return ExecutionResult{}, fmt.Errorf("harness: LLM client is required")
	}
	if len(spec.Messages) == 0 {
		return ExecutionResult{}, fmt.Errorf("harness: at least one message is required")
	}

	var completed atomic.Bool
	tools := adaptTools(spec.ToolDefs, spec.Tools, &completed)
	recorder := newExecutionRecorder(spec)
	taskType := spec.TaskType
	if taskType == "" {
		taskType = session.MainTask
	}
	model := &chatModel{
		client:    spec.LLMClient,
		model:     spec.Model,
		maxTokens: spec.MaxTokens,
		recorder:  recorder,
		taskType:  taskType,
		events:    true,
	}
	contextModel := &chatModel{
		client:    spec.LLMClient,
		model:     spec.Model,
		maxTokens: spec.MaxTokens,
		recorder:  recorder,
		taskType:  session.MemoryCompressionTask,
	}
	contextManager := newContextManager(spec, contextModel)
	prompt := spec.CompletionPrompt
	if prompt == "" {
		prompt = defaultCompletionPrompt
	}

	config := agentcore.LoopConfig{
		Model:          model,
		MaxTurns:       spec.MaxTurns,
		ContextManager: contextManager,
		ConvertToLLM:   contextManager.ConvertToLLM,
		StopAfterTool: func(name string) bool {
			return name == tool.TaskDone.Name()
		},
		StopGuard: func(_ context.Context, _ agentcore.StopInfo) agentcore.StopDecision {
			if completed.Load() {
				return agentcore.StopDecision{Allow: true}
			}
			return agentcore.StopDecision{InjectMessage: prompt}
		},
	}
	config.Middlewares = []agentcore.ToolMiddleware{
		toolMiddleware(spec.Scope, spec.ToolHandler, recorder),
	}

	events := agentcore.AgentLoop(
		ctx,
		toAgentMessages(spec.Messages),
		agentcore.AgentContext{Tools: tools},
		config,
	)

	var (
		result  ExecutionResult
		runErr  error
		summary *agentcore.RunSummary
	)
	for event := range events {
		emitExecutionEvent(spec.Events, recorder, event)
		switch event.Type {
		case agentcore.EventError:
			if event.Err != nil {
				runErr = event.Err
			}
		case agentcore.EventToolExecEnd:
			if event.Tool != tool.TaskDone.Name() {
				recorder.finishTool(event.ToolID, event.Tool, event.Result, event.IsError)
			}
		case agentcore.EventAgentEnd:
			summary = event.Summary
		}
	}

	if summary != nil {
		result.Turns = summary.TurnCount
		result.ToolCalls = summary.ToolCalls
		result.ToolErrors = summary.ToolErrors
	}
	result.Usage = recorder.Usage()
	return finishExecution(ctx, result, summary, completed.Load(), runErr)
}

func toolMiddleware(
	scope session.Scope,
	handler ToolHandler,
	recorder *executionRecorder,
) agentcore.ToolMiddleware {
	return func(
		ctx context.Context,
		call agentcore.ToolCall,
		next agentcore.ToolExecuteFunc,
	) (json.RawMessage, error) {
		started := time.Now()
		defer func() {
			recorder.finishToolExecution(call.ID, time.Since(started))
		}()
		if handler == nil {
			return next(ctx, call.Args)
		}
		var args map[string]any
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return next(ctx, call.Args)
		}
		recorded := recorder.call(call.ID)
		checkpoint, handled := handler.HandleTool(ctx, ToolRequest{
			Scope: scope,
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

func finishExecution(
	ctx context.Context,
	result ExecutionResult,
	summary *agentcore.RunSummary,
	completed bool,
	runErr error,
) (ExecutionResult, error) {
	if completed && summary != nil && summary.EndReason == agentcore.EndReasonStop {
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
	if summary != nil && summary.EndReason == agentcore.EndReasonMaxTurns {
		result.State = OutcomeTruncated
		result.Reason = string(summary.EndReason)
		return result, nil
	}
	if runErr != nil {
		result.State = OutcomeLLMError
		result.Reason = runErr.Error()
		return result, runErr
	}
	if summary == nil {
		err := fmt.Errorf("harness: agentcore ended without a run summary")
		result.State = OutcomeLLMError
		result.Reason = err.Error()
		return result, err
	}

	result.State = OutcomeTruncated
	result.Reason = string(summary.EndReason)
	return result, nil
}

func toAgentMessages(messages []msg.Msg) []agentcore.AgentMessage {
	return wrapDomainMessages(msg.CloneAll(messages))
}

func wireToAgentMessage(message llm.Message) agentcore.Message {
	content := []agentcore.ContentBlock{agentcore.TextBlock(message.ExtractText())}
	for _, call := range message.ToolCalls {
		args := json.RawMessage(call.Function.Arguments)
		content = append(content, agentcore.ToolCallBlock(agentcore.ToolCall{
			ID:   call.ID,
			Name: call.Function.Name,
			Args: args,
		}))
	}
	agentMessage := agentcore.Message{
		Role:    agentcore.Role(message.Role),
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

func adaptTools(defs []llm.ToolDef, registry *tool.Registry, completed *atomic.Bool) []agentcore.Tool {
	out := make([]agentcore.Tool, 0, len(defs)+1)
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

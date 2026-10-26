package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"path/filepath"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

// executionOperations translates runtime facts at the Harness boundary. Middleware
// only propagates stage identity into actual calls; Event timestamps own timing.
// The channel consumer owns these maps, including concurrent tool events.
type executionOperations struct {
	run      *agentsession.Run
	ctx      context.Context
	reviewID string
	id       string
	runID    string
	started  time.Time
	root     operation.Handle
	stages   map[operation.ID]operation.Handle
	parents  map[string]operation.ID
}

func (e *Execution) beginOperations(ctx context.Context, started time.Time) (context.Context, *executionOperations) {
	ctx = e.spec.Session.Context(ctx)
	ref, ok := operation.FromContext(ctx)
	if !ok || ref.ReviewID == "" {
		return ctx, nil
	}
	ctx, root := telemetry.BeginOperation(ctx, "execution", operation.WithParent(ref.OperationID), operation.WithID(operation.ID(e.id)), operation.WithStartTime(started), operation.WithAttributes(
		operation.Attribute{Key: "context_reused", Value: e.spec.LoopSession.HasContext()}, operation.Attribute{Key: "session_file", Value: filepath.Base(e.sessionPath)}, operation.Attribute{Key: "session_id", Value: e.sessionID()}, operation.Attribute{Key: "execution_id", Value: e.id}, operation.Attribute{Key: "scope_id", Value: e.spec.Scope.ID}, operation.Attribute{Key: "task_type", Value: e.recorder.taskType}, operation.Attribute{Key: "kind", Value: e.spec.Scope.Kind}, operation.Attribute{Key: "scope", Value: e.spec.Scope.Type}, operation.Attribute{Key: "paths", Value: e.spec.Scope.Paths}, operation.Attribute{Key: "filePath", Value: e.spec.Scope.Path()}))
	return ctx, &executionOperations{ctx: ctx, reviewID: ref.ReviewID, id: e.id, runID: e.id, started: started, root: root, stages: map[operation.ID]operation.Handle{}, parents: map[string]operation.ID{}}
}

func (r *executionOperations) coordinate(kind string, e agentgo.Execution) operation.ID {
	return executionOperationID(r.runID, kind, e)
}
func (r *executionOperations) turn(index int) operation.ID {
	return operation.ID(fmt.Sprintf("%s/turn/%d", r.runID, index))
}
func (r *executionOperations) bind(ctx context.Context, kind string, e agentgo.Execution) context.Context {
	if r == nil {
		return ctx
	}
	return operation.NewContext(ctx, operation.Ref{ReviewID: r.reviewID, OperationID: r.coordinate(kind, e)})
}
func (r *executionOperations) model(ctx context.Context, e agentgo.ModelExecution, next agentgo.ModelExecuteFunc) (agentgo.ModelResult, error) {
	return next(r.bind(ctx, "model", e.Execution), e)
}
func (r *executionOperations) tool(ctx context.Context, e agentgo.ToolExecution, next agentgo.ToolExecuteFunc) (agentgo.ToolResult, error) {
	return next(r.bind(ctx, "tool", e.Execution), e)
}

func (r *executionOperations) observe(ev agentgo.Event) {
	if r == nil {
		return
	}
	var id, parent operation.ID
	var name string
	var begin bool
	attributes := []operation.Attribute{{Key: "execution_id", Value: r.id}}
	if ev.Execution != nil {
		e := *ev.Execution
		if e.Kind == agentgo.ExecutionKindCompact {
			attributes = append(attributes, operation.Attribute{Key: "task_type", Value: "memory_compression_task"})
		}
		if r.run != nil {
			attributes = append(attributes, operation.Attribute{Key: "session_ref", Value: r.run.Ref(e)})
		}
		parent = r.turn(e.TurnIndex)
		if e.ParentID != "" {
			if p, ok := r.parents[e.ParentID]; ok {
				parent = p
			}
		}
		attributes = append(attributes, operation.Attribute{Key: "agent_execution_id", Value: e.ID}, operation.Attribute{Key: "attempt", Value: e.Attempt}, operation.Attribute{Key: "turn", Value: e.TurnIndex})
		switch ev.Type {
		case agentgo.EventModelExecStart, agentgo.EventModelExecEnd:
			id, name, begin = r.coordinate("model", e), "model.attempt", ev.Type == agentgo.EventModelExecStart
		case agentgo.EventContextPrepareStart, agentgo.EventContextPrepareEnd:
			id, name, begin = r.coordinate("context", e), "context."+string(ev.ContextOperation), ev.Type == agentgo.EventContextPrepareStart
		case agentgo.EventRetryWaitStart, agentgo.EventRetryWaitEnd:
			id, name, begin = r.coordinate("retry", e), "retry.wait", ev.Type == agentgo.EventRetryWaitStart
		case agentgo.EventToolQueued:
			id, name, begin = r.coordinate("queue", e), "tool.queue", true
		case agentgo.EventToolExecStart, agentgo.EventToolExecEnd:
			if ev.Type == agentgo.EventToolExecStart {
				r.end(r.coordinate("queue", e), ev)
			}
			id, name, begin = r.coordinate("tool", e), "tool.execution", ev.Type == agentgo.EventToolExecStart
		case agentgo.EventToolInvokeStart, agentgo.EventToolInvokeEnd:
			id, name, begin = r.coordinate("invoke", e), "tool.invoke", ev.Type == agentgo.EventToolInvokeStart
			parent = r.coordinate("tool", e)
		default:
			return
		}
		if begin && (ev.Type == agentgo.EventModelExecStart || ev.Type == agentgo.EventContextPrepareStart || ev.Type == agentgo.EventToolExecStart) {
			r.parents[e.ID] = id
		}
		if ev.Tool != "" {
			attributes = append(attributes, operation.Attribute{Key: "tool", Value: ev.Tool})
		}
	} else if ev.Type == agentgo.EventTurnStart || ev.Type == agentgo.EventTurnEnd {
		id, parent, name, begin = r.turn(ev.TurnIndex), r.root.ID(), "turn", ev.Type == agentgo.EventTurnStart
		attributes = append(attributes, operation.Attribute{Key: "turn", Value: ev.TurnIndex})
	} else {
		return
	}
	if begin {
		stage, err := operation.Begin(r.reviewID, name, operation.WithID(id), operation.WithParent(parent), operation.WithStartTime(ev.Timestamp), operation.WithAttributes(attributes...))
		if err != nil {
			telemetry.ReportRecordingError(err)
			return
		}
		r.stages[id] = stage
	} else {
		r.end(id, ev)
	}
}

func (r *executionOperations) end(id operation.ID, ev agentgo.Event) {
	if stage, ok := r.stages[id]; ok {
		err := ev.Err
		if err == nil && ev.IsError {
			err = errors.New("tool returned an error result")
		}
		telemetry.ReportRecordingError(stage.End(err, operation.WithEndTime(ev.Timestamp), operation.WithEndAttributes(operation.Attribute{Key: "disposition", Value: ev.Disposition}, operation.Attribute{Key: "failure", Value: llm.DescribeError(ev.Err)})))
		delete(r.stages, id)
	}
	// Missing starts/ends remain missing evidence; do not invent successful work.
}
func (r *executionOperations) finish(result ExecutionResult, err error) {
	if r == nil {
		return
	}
	if err == nil && r.ctx.Err() != nil {
		err = r.ctx.Err()
	}
	if err == nil && result.State != OutcomeCompleted {
		err = fmt.Errorf("%s: %s", result.State, result.Reason)
	}
	options := []operation.EndOption{}
	if !r.started.IsZero() && result.Duration > 0 {
		options = append(options, operation.WithEndTime(r.started.Add(result.Duration)))
	}
	options = append(options, operation.WithEndAttributes(operation.Attribute{Key: "outcome", Value: result.State}, operation.Attribute{Key: "reason", Value: result.Reason}, operation.Attribute{Key: "turns", Value: result.Turns}, operation.Attribute{Key: "tool_calls", Value: result.ToolCalls}, operation.Attribute{Key: "tool_errors", Value: result.ToolErrors}, operation.Attribute{Key: "incomplete_stages", Value: len(r.stages)}))
	telemetry.EndOperation(r.ctx, r.root, err, options...)
}

func executionOperationID(executionID, kind string, e agentgo.Execution) operation.ID {
	return operation.ID(fmt.Sprintf("%s/%s/%s/%d", executionID, kind, e.ID, e.Attempt))
}

func (e *Execution) sessionID() string {
	if e.localSession != nil {
		return e.localSession.ID()
	}
	return ""
}

package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

// executionTimeline translates runtime facts at the Harness boundary. Middleware
// only propagates stage identity into actual calls; Event timestamps own timing.
// The channel consumer owns these maps, including concurrent tool events.
type executionTimeline struct {
	ctx        context.Context
	timelineID string
	id         string
	started    time.Time
	root       timeline.StageHandle
	stages     map[timeline.StageID]timeline.StageHandle
	parents    map[string]timeline.StageID
}

func (e *Execution) beginTimeline(ctx context.Context, started time.Time) (context.Context, *executionTimeline) {
	ctx = e.spec.Session.Context(ctx)
	ref, ok := timeline.StageFromContext(ctx)
	if !ok || ref.TimelineID == "" {
		return ctx, nil
	}
	ctx, root := telemetry.BeginTimelineStage(ctx, "execution", timeline.WithParent(ref.StageID), timeline.WithStageID(timeline.StageID(e.id)), timeline.WithStartTime(started), timeline.WithAttributes(
		timeline.Attribute{Key: "execution_id", Value: e.id}, timeline.Attribute{Key: "scope_id", Value: e.spec.Scope.ID}, timeline.Attribute{Key: "task_type", Value: e.recorder.taskType}, timeline.Attribute{Key: "kind", Value: e.spec.Scope.Kind}, timeline.Attribute{Key: "scope", Value: e.spec.Scope.Type}, timeline.Attribute{Key: "paths", Value: e.spec.Scope.Paths}, timeline.Attribute{Key: "filePath", Value: e.spec.Scope.Path()}))
	return ctx, &executionTimeline{ctx: ctx, timelineID: ref.TimelineID, id: e.id, started: started, root: root, stages: map[timeline.StageID]timeline.StageHandle{}, parents: map[string]timeline.StageID{}}
}

func (r *executionTimeline) coordinate(kind string, e agentgo.Execution) timeline.StageID {
	return executionStageID(r.id, kind, e)
}
func (r *executionTimeline) turn(index int) timeline.StageID {
	return timeline.StageID(fmt.Sprintf("%s/turn/%d", r.id, index))
}
func (r *executionTimeline) bind(ctx context.Context, kind string, e agentgo.Execution) context.Context {
	if r == nil {
		return ctx
	}
	return timeline.NewStageContext(ctx, timeline.StageRef{TimelineID: r.timelineID, StageID: r.coordinate(kind, e)})
}
func (r *executionTimeline) model(ctx context.Context, e agentgo.ModelExecution, next agentgo.ModelExecuteFunc) (agentgo.ModelResult, error) {
	return next(r.bind(ctx, "model", e.Execution), e)
}
func (r *executionTimeline) tool(ctx context.Context, e agentgo.ToolExecution, next agentgo.ToolExecuteFunc) (agentgo.ToolResult, error) {
	return next(r.bind(ctx, "tool", e.Execution), e)
}

func (r *executionTimeline) observe(ev agentgo.Event) {
	if r == nil {
		return
	}
	var id, parent timeline.StageID
	var name string
	var begin bool
	attributes := []timeline.Attribute{{Key: "execution_id", Value: r.id}}
	if ev.Execution != nil {
		e := *ev.Execution
		parent = r.turn(e.TurnIndex)
		if e.ParentID != "" {
			if p, ok := r.parents[e.ParentID]; ok {
				parent = p
			}
		}
		attributes = append(attributes, timeline.Attribute{Key: "agent_execution_id", Value: e.ID}, timeline.Attribute{Key: "attempt", Value: e.Attempt}, timeline.Attribute{Key: "turn", Value: e.TurnIndex})
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
			attributes = append(attributes, timeline.Attribute{Key: "tool", Value: ev.Tool})
		}
	} else if ev.Type == agentgo.EventTurnStart || ev.Type == agentgo.EventTurnEnd {
		id, parent, name, begin = r.turn(ev.TurnIndex), r.root.ID(), "turn", ev.Type == agentgo.EventTurnStart
		attributes = append(attributes, timeline.Attribute{Key: "turn", Value: ev.TurnIndex})
	} else {
		return
	}
	if begin {
		stage, err := timeline.Begin(r.timelineID, name, timeline.WithStageID(id), timeline.WithParent(parent), timeline.WithStartTime(ev.Timestamp), timeline.WithAttributes(attributes...))
		if err != nil {
			telemetry.ReportTimelineError(err)
			return
		}
		r.stages[id] = stage
	} else {
		r.end(id, ev)
	}
}

func (r *executionTimeline) end(id timeline.StageID, ev agentgo.Event) {
	if stage, ok := r.stages[id]; ok {
		err := ev.Err
		if err == nil && ev.IsError {
			err = errors.New("tool returned an error result")
		}
		telemetry.ReportTimelineError(stage.End(err, timeline.WithEndTime(ev.Timestamp), timeline.WithEndAttributes(timeline.Attribute{Key: "disposition", Value: ev.Disposition})))
		delete(r.stages, id)
	}
	// Missing starts/ends remain missing evidence; do not invent successful work.
}
func (r *executionTimeline) finish(result ExecutionResult, err error) {
	if r == nil {
		return
	}
	if err == nil && r.ctx.Err() != nil {
		err = r.ctx.Err()
	}
	if err == nil && result.State != OutcomeCompleted {
		err = fmt.Errorf("%s: %s", result.State, result.Reason)
	}
	options := []timeline.EndOption{}
	if !r.started.IsZero() && result.Duration > 0 {
		options = append(options, timeline.WithEndTime(r.started.Add(result.Duration)))
	}
	options = append(options, timeline.WithEndAttributes(timeline.Attribute{Key: "outcome", Value: result.State}, timeline.Attribute{Key: "reason", Value: result.Reason}, timeline.Attribute{Key: "turns", Value: result.Turns}, timeline.Attribute{Key: "tool_calls", Value: result.ToolCalls}, timeline.Attribute{Key: "tool_errors", Value: result.ToolErrors}, timeline.Attribute{Key: "incomplete_stages", Value: len(r.stages)}))
	telemetry.EndTimelineStage(r.ctx, r.root, err, options...)
}

// eventStage is shared by runtime timing and content recording, including attempts.
func (r *executionTimeline) eventStage(ev agentgo.Event) timeline.StageID {
	if r == nil || ev.Execution == nil {
		return ""
	}
	return r.coordinate("tool", *ev.Execution)
}

func executionStageID(executionID, kind string, e agentgo.Execution) timeline.StageID {
	return timeline.StageID(fmt.Sprintf("%s/%s/%s/%d", executionID, kind, e.ID, e.Attempt))
}
func executionEventStage(executionID, kind string, ev agentgo.Event) timeline.StageID {
	if ev.Execution == nil {
		return timeline.StageID(executionID)
	}
	return executionStageID(executionID, kind, *ev.Execution)
}

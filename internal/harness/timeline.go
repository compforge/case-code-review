package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/session"
)

// executionTimeline translates runtime facts at the Harness boundary. Middleware
// only propagates stage identity into actual calls; Event timestamps own timing.
// The channel consumer owns these maps, including concurrent tool events.
type executionTimeline struct {
	ctx      context.Context
	timeline timeline.Timeline
	id       string
	root     timeline.StageHandle
	stages   map[timeline.StageID]timeline.StageHandle
	parents  map[string]timeline.StageID
}

func (e *Execution) beginTimeline(ctx context.Context) (context.Context, *executionTimeline) {
	ctx = e.spec.Session.Context(ctx)
	t, ok := timeline.FromContext(ctx)
	if !ok {
		return ctx, nil
	}
	ctx, root := timeline.BeginContext(ctx, t, "execution", timeline.WithStageID(timeline.StageID(e.id)), timeline.WithFields(
		timeline.Field{Key: "execution_id", Value: e.id}, timeline.Field{Key: "scope_id", Value: e.spec.Scope.ID}, timeline.Field{Key: "task_type", Value: e.spec.TaskType}))
	session.FlushTimeline(ctx)
	return ctx, &executionTimeline{ctx: ctx, timeline: t, id: e.id, root: root, stages: map[timeline.StageID]timeline.StageHandle{}, parents: map[string]timeline.StageID{}}
}

func (r *executionTimeline) coordinate(kind string, e agentgo.Execution) timeline.StageID {
	return timeline.StageID(fmt.Sprintf("%s/%s/%s/%d", r.id, kind, e.ID, e.Attempt))
}
func (r *executionTimeline) turn(index int) timeline.StageID {
	return timeline.StageID(fmt.Sprintf("%s/turn/%d", r.id, index))
}
func (r *executionTimeline) bind(ctx context.Context, kind string, e agentgo.Execution) context.Context {
	if r == nil {
		return ctx
	}
	return timeline.NewStageContext(ctx, timeline.StageRef{TimelineID: r.timeline.ID(), StageID: r.coordinate(kind, e)})
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
	fields := []timeline.Field{{Key: "execution_id", Value: r.id}}
	if ev.Execution != nil {
		e := *ev.Execution
		parent = r.turn(e.TurnIndex)
		if e.ParentID != "" {
			if p, ok := r.parents[e.ParentID]; ok {
				parent = p
			}
		}
		fields = append(fields, timeline.Field{Key: "agent_execution_id", Value: e.ID}, timeline.Field{Key: "attempt", Value: e.Attempt}, timeline.Field{Key: "turn", Value: e.TurnIndex})
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
			fields = append(fields, timeline.Field{Key: "tool", Value: ev.Tool})
		}
	} else if ev.Type == agentgo.EventTurnStart || ev.Type == agentgo.EventTurnEnd {
		id, parent, name, begin = r.turn(ev.TurnIndex), r.root.ID(), "turn", ev.Type == agentgo.EventTurnStart
		fields = append(fields, timeline.Field{Key: "turn", Value: ev.TurnIndex})
	} else {
		return
	}
	if begin {
		r.stages[id] = r.timeline.Begin(name, timeline.WithStageID(id), timeline.WithParent(parent), timeline.WithStartTime(ev.Timestamp), timeline.WithFields(fields...))
	} else {
		r.end(id, ev)
	}
	session.FlushTimeline(r.ctx)
}

func (r *executionTimeline) end(id timeline.StageID, ev agentgo.Event) {
	if stage, ok := r.stages[id]; ok {
		err := ev.Err
		if err == nil && ev.IsError {
			err = errors.New("tool returned an error result")
		}
		stage.End(err, timeline.WithEndTime(ev.Timestamp), timeline.WithEndFields(timeline.Field{Key: "disposition", Value: ev.Disposition}))
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
	if err == nil && len(r.stages) > 0 {
		err = errors.New("execution ended with missing lifecycle facts")
	}
	if err == nil && result.State != OutcomeCompleted {
		err = fmt.Errorf("%s: %s", result.State, result.Reason)
	}
	r.root.End(err, timeline.WithEndFields(timeline.Field{Key: "outcome", Value: result.State}, timeline.Field{Key: "incomplete_stages", Value: len(r.stages)}))
	session.FlushTimeline(r.ctx)
}

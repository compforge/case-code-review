package llm

import (
	"context"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/go-stdx/uuid"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

// Standalone calls own a temporary ID; calls within a Session inherit its ID.
func beginRequest(ctx context.Context) (context.Context, func(error)) {
	if ref, ok := timeline.StageFromContext(ctx); ok && ref.TimelineID != "" {
		return ctx, func(error) {}
	}
	id := uuid.V4()
	if err := timeline.Start(id, "llm.request"); err != nil {
		telemetry.ReportTimelineError(err)
		return ctx, func(error) {}
	}
	ctx = timeline.NewStageContext(ctx, timeline.StageRef{TimelineID: id})
	return ctx, func(err error) {
		telemetry.ReportTimelineError(timeline.Finish(id, err))
		telemetry.ReleaseTimeline(id)
	}
}

func beginStage(ctx context.Context, name string, attributes ...timeline.Attribute) (context.Context, timeline.StageHandle) {
	ref, _ := timeline.StageFromContext(ctx)
	return telemetry.BeginTimelineStage(ctx, name, timeline.WithParent(ref.StageID), timeline.WithAttributes(attributes...))
}

func endStage(ctx context.Context, stage timeline.StageHandle, err error) {
	var options []timeline.EndOption
	if details := DescribeError(err); details != nil {
		options = append(options, timeline.WithCode(details.Code))
	}
	telemetry.EndTimelineStage(ctx, stage, err, options...)
}

func remainingBudget(ctx context.Context) []timeline.Attribute {
	if deadline, ok := ctx.Deadline(); ok {
		return []timeline.Attribute{{Key: "remaining_budget_ms", Value: max(int64(0), time.Until(deadline).Milliseconds())}}
	}
	return nil
}

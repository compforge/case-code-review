package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/go-stdx/uuid"

	"github.com/qiankunli/case-code-review/internal/console"
)

// beginRequest reuses the caller's recording boundary. Clients without a Session
// still use the same timeline facts to classify transport failures.
func beginRequest(ctx context.Context) (context.Context, func(error)) {
	if _, ok := timeline.FromContext(ctx); ok {
		return ctx, func(error) {}
	}
	t, _ := timeline.New(uuid.V4())
	_ = t.Start(context.Background(), "llm.request")
	return timeline.NewContext(ctx, t), func(err error) {
		_, collectErr := t.Finish(context.Background(), err)
		reportTimelineError(t, collectErr)
	}
}

func beginStage(ctx context.Context, name string, attributes ...timeline.Attribute) (context.Context, timeline.StageHandle) {
	t, ok := timeline.FromContext(ctx)
	if !ok {
		return ctx, timeline.Noop("").Begin(name)
	}
	ctx, stage := timeline.BeginContext(ctx, t, name, timeline.WithAttributes(attributes...))
	flushTimeline(t)
	return ctx, stage
}

func endStage(ctx context.Context, stage timeline.StageHandle, err error) {
	var options []timeline.EndOption
	if details := DescribeError(err); details != nil {
		options = append(options, timeline.WithCode(details.Code))
	}
	stage.End(err, options...)
	if t, ok := timeline.FromContext(ctx); ok {
		flushTimeline(t)
	}
}

func remainingBudget(ctx context.Context) []timeline.Attribute {
	if deadline, ok := ctx.Deadline(); ok {
		return []timeline.Attribute{{Key: "remaining_budget_ms", Value: max(int64(0), time.Until(deadline).Milliseconds())}}
	}
	return nil
}

func flushTimeline(t timeline.Timeline) {
	// This backend is local Session IO. Cancellation must not discard the facts
	// explaining a timeout; it never extends the provider's request deadline.
	reportTimelineError(t, t.Flush(context.Background()))
}

func reportTimelineError(t timeline.Timeline, err error) {
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr timeline] request=%s persistence failed: %v\n", t.ID(), err)
	}
}

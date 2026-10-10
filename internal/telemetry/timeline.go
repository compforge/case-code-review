package telemetry

import (
	"context"
	"fmt"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/console"
)

// The process shares one cache-only Manager. Session owns JSONL export and
// releases its ID after the final snapshot; transport-only calls release theirs.
var timelineManager *timeline.Manager

// InitTimeline installs the process manager before any recording starts. Call
// the returned cleanup after producers stop. Serial tests can use the same
// lifetime inside a synctest bubble, keeping its workers on the fake clock.
func InitTimeline() (func(context.Context) error, error) {
	m, err := timeline.NewManager(nil, timeline.Config{Actor: timeline.Actor{ID: "ccr"}})
	if err != nil {
		return nil, err
	}
	previous := timelineManager
	timelineManager = m
	previousDefault := timeline.SetDefault(m)
	return func(ctx context.Context) error {
		err := m.Shutdown(ctx)
		timeline.SetDefault(previousDefault)
		timelineManager = previous
		return err
	}, nil
}

func ReleaseTimeline(id string) {
	if timelineManager != nil {
		timelineManager.Evict(id)
	}
}

// BeginTimelineStage binds stage identity; callers supply WithParent explicitly.
// It preserves cancellation. Recording errors remain diagnostics, not business errors.
func BeginTimelineStage(ctx context.Context, name string, opts ...timeline.StageOption) (context.Context, timeline.StageHandle) {
	ref, ok := timeline.StageFromContext(ctx)
	if !ok || ref.TimelineID == "" {
		return ctx, timeline.Noop("").Begin(name)
	}
	stage, err := timeline.Begin(ref.TimelineID, name, opts...)
	if err != nil {
		ReportTimelineError(err)
		return ctx, timeline.Noop(ref.TimelineID).Begin(name)
	}
	ctx = timeline.NewStageContext(ctx, timeline.StageRef{TimelineID: ref.TimelineID, StageID: stage.ID()})
	return ctx, stage
}

func EndTimelineStage(ctx context.Context, stage timeline.StageHandle, err error, opts ...timeline.EndOption) {
	ReportTimelineError(stage.End(err, opts...))
}

func ReportTimelineError(err error) {
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr timeline] recording failed: %v\n", err)
	}
}

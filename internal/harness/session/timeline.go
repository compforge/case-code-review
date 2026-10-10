package session

import (
	"context"
	"fmt"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

// Context carries only Session/stage identity.
// Existing parentage is preserved when it belongs to this Session.
func (sh *SessionHistory) Context(ctx context.Context) context.Context {
	if sh == nil {
		return ctx
	}
	ref, ok := timeline.StageFromContext(ctx)
	if !ok || ref.TimelineID != sh.SessionID {
		ctx = timeline.NewStageContext(ctx, timeline.StageRef{TimelineID: sh.SessionID, StageID: ""})
	}
	return ctx
}

func (sh *SessionHistory) startTimeline() {
	operation := "review"
	if sh.ReviewMode == ReviewModeFullScan {
		operation = "scan"
	}
	if err := timeline.Start(sh.SessionID, operation); err != nil {
		telemetry.ReportTimelineError(err)
		return
	}
	sh.CheckpointTimeline()
	if sh.persist != nil {
		sh.checkpointStop = make(chan struct{})
		sh.checkpointDone = make(chan struct{})
		go sh.checkpointLoop()
	}
}

func (sh *SessionHistory) checkpointTimeline() (timeline.Snapshot, error) {
	if sh.persist != nil {
		return sh.persist.writeTimeline(sh.SessionID)
	}
	return timeline.Read(context.Background(), sh.SessionID, false)
}

// CheckpointTimeline exports at Session-owned request and execution boundaries.
func (sh *SessionHistory) CheckpointTimeline() {
	if sh == nil || sh.SessionID == "" {
		return
	}
	_, err := sh.checkpointTimeline()
	telemetry.ReportTimelineError(err)
}

// A periodic checkpoint exposes long waits without putting file IO on HTTP or
// AgentGo event paths. Abrupt exit can lose facts since the last completed write.
func (sh *SessionHistory) checkpointLoop() {
	defer close(sh.checkpointDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-sh.checkpointStop:
			return
		case <-ticker.C:
			sh.CheckpointTimeline()
		}
	}
}

// Begin records work with explicit parentage supplied by this Session adapter.
func Begin(ctx context.Context, name string, attributes ...timeline.Attribute) (context.Context, func(error)) {
	return BeginStage(ctx, name, timeline.WithAttributes(attributes...))
}

func BeginStage(ctx context.Context, name string, opts ...timeline.StageOption) (context.Context, func(error)) {
	ref, _ := timeline.StageFromContext(ctx)
	opts = append([]timeline.StageOption{timeline.WithParent(ref.StageID)}, opts...)
	ctx, stage := telemetry.BeginTimelineStage(ctx, name, opts...)
	return ctx, func(err error) { telemetry.EndTimelineStage(ctx, stage, err) }
}

// Call records a request under its execution, or under the Session for auxiliary
// calls. Routing and HTTP instrumentation inherit this stage and timeline ID.
func (tr *TaskRecord) Call(ctx context.Context, client llm.LLMClient, request llm.ChatRequest) (*llm.ChatResponse, error) {
	if tr == nil {
		return client.CompletionsWithCtx(ctx, request)
	}
	if tr.scopeSession != nil {
		ctx = tr.scopeSession.session.Context(ctx)
	}
	attributes := []timeline.Attribute{{Key: "execution_id", Value: tr.ExecutionID}, {Key: "task_type", Value: tr.Type}, {Key: "request_no", Value: tr.RequestNo}}
	if tr.scopeSession != nil {
		attributes = append(attributes, timeline.Attribute{Key: "scope_id", Value: tr.scopeSession.ID})
	}
	started := time.Now()
	ref, _ := timeline.StageFromContext(ctx)
	ctx, stage := telemetry.BeginTimelineStage(ctx, "llm.request", timeline.WithParent(ref.StageID), timeline.WithStageID(tr.StageID), timeline.WithStartTime(started), timeline.WithAttributes(attributes...))
	if tr.scopeSession != nil {
		tr.scopeSession.session.CheckpointTimeline()
	}
	response, err := client.CompletionsWithCtx(ctx, request)
	finished := time.Now()
	duration := finished.Sub(started)
	if err == nil && (response == nil || len(response.Choices) == 0) {
		err = fmt.Errorf("empty response")
	}
	if err != nil {
		tr.SetError(err, duration)
	} else {
		tr.SetResponse(response, duration)
	}
	// Export the terminal stage after its response/error, even on cancellation.
	telemetry.EndTimelineStage(ctx, stage, err, timeline.WithEndTime(finished))
	if tr.scopeSession != nil {
		tr.scopeSession.session.CheckpointTimeline()
	}
	return response, err
}

func (ss *ScopeSession) RecordingClient(client llm.LLMClient, taskType TaskType) llm.LLMClient {
	return &recordingClient{scope: ss, client: client, taskType: taskType}
}

type recordingClient struct {
	scope    *ScopeSession
	client   llm.LLMClient
	taskType TaskType
}

func (c *recordingClient) CompletionsWithCtx(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.scope.AppendTaskRecord(c.taskType, request.Messages).Call(ctx, c.client, request)
}

// Read under the JSONL lock so concurrent checkpoints cannot append an older
// snapshot after a newer one. Export is CCR-owned; timeline never performs file IO.
func (jw *jsonlWriter) writeTimeline(id string) (timeline.Snapshot, error) {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	snapshot, err := timeline.Read(context.Background(), id, false)
	if err != nil {
		return snapshot, err
	}
	_, err = jw.appendLocked(map[string]any{"type": "timeline_snapshot", "timeline_id": id, "snapshot": snapshot})
	return snapshot, err
}

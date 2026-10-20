package recording

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/compforge/go-stdx/timeline"

	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

// Context carries only review timeline/stage identity.
// Existing parentage is preserved when it belongs to this Review.
func (sh *Review) Context(ctx context.Context) context.Context {
	if sh == nil {
		return ctx
	}
	ref, ok := timeline.StageFromContext(ctx)
	if !ok || ref.TimelineID != sh.SessionID {
		ctx = timeline.NewStageContext(ctx, timeline.StageRef{TimelineID: sh.SessionID, StageID: ""})
	}
	return ctx
}

func (sh *Review) startTimeline() {
	if sh.persist != nil {
		path, err := sh.artifactPath(".timeline")
		if err == nil {
			err = telemetry.AttachTimeline(sh.SessionID, path)
		}
		telemetry.ReportTimelineError(err)
	}
	operation := "review"
	if sh.ReviewMode == ReviewModeFullScan {
		operation = "scan"
	}
	if err := timeline.Start(sh.SessionID, operation); err != nil {
		telemetry.ReportTimelineError(err)
		return
	}
}

// Begin records work with explicit parentage supplied by this Review adapter.
func Begin(ctx context.Context, name string, attributes ...timeline.Attribute) (context.Context, func(error)) {
	return BeginStage(ctx, name, timeline.WithAttributes(attributes...))
}

func BeginStage(ctx context.Context, name string, opts ...timeline.StageOption) (context.Context, func(error)) {
	ref, _ := timeline.StageFromContext(ctx)
	opts = append([]timeline.StageOption{timeline.WithParent(ref.StageID)}, opts...)
	ctx, stage := telemetry.BeginTimelineStage(ctx, name, opts...)
	return ctx, func(err error) { telemetry.EndTimelineStage(ctx, stage, err) }
}

// Call records bodies in a local framework Session and references it from the
// business timeline. Routing and HTTP instrumentation inherit this stage and timeline ID.
func (tr *TaskRecord) Call(ctx context.Context, client agentgo.ChatModel, request agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
	if tr == nil {
		return llm.Call(ctx, client, request)
	}
	if tr.scopeSession != nil {
		ctx = tr.scopeSession.session.Context(ctx)
	}
	if tr.ExecutionID != "" {
		started := time.Now()
		response, err := llm.Call(ctx, client, request)
		if err != nil {
			tr.SetError(err, time.Since(started))
		} else {
			tr.SetResponse(response, time.Since(started))
		}
		return response, err
	}
	var captureErr error
	reportCapture := func(err error) {
		if err == nil || captureErr != nil {
			return
		}
		captureErr = err
		telemetry.ReportTimelineError(fmt.Errorf("scope %s auxiliary Session: %w", tr.scopeSession.ID, err))
		tr.scopeSession.session.WriteArtifact("recording_gap", map[string]any{"scope_id": tr.scopeSession.ID, "stage_id": tr.StageID, "error": err.Error()})
	}
	local, path, err := tr.scopeSession.session.OpenLoop("")
	var run *agentsession.Run
	if err == nil && local != nil {
		defer func() { reportCapture(local.Close()) }()
		run, err = local.BeginRun()
	}
	reportCapture(err)
	if run != nil {
		defer func() { reportCapture(run.Close()) }()
	}
	execution := agentgo.Execution{ID: "model-1", Kind: agentgo.ExecutionKindModel, Attempt: 1}
	attributes := []timeline.Attribute{{Key: "execution_id", Value: tr.ExecutionID}, {Key: "task_type", Value: tr.Type}, {Key: "request_no", Value: tr.RequestNo}}
	if tr.scopeSession != nil {
		attributes = append(attributes, timeline.Attribute{Key: "scope_id", Value: tr.scopeSession.ID}, timeline.Attribute{Key: "kind", Value: tr.scopeSession.Kind}, timeline.Attribute{Key: "scope", Value: tr.scopeSession.Scope}, timeline.Attribute{Key: "filePath", Value: tr.scopeSession.Path}, timeline.Attribute{Key: "paths", Value: tr.scopeSession.Paths})
	}
	if run != nil {
		attributes = append(attributes, timeline.Attribute{Key: "session_ref", Value: run.Ref(execution)}, timeline.Attribute{Key: "session_id", Value: local.ID()}, timeline.Attribute{Key: "session_file", Value: filepath.Base(path)})
	}
	started := time.Now()
	ref, _ := timeline.StageFromContext(ctx)
	ctx, stage := telemetry.BeginTimelineStage(ctx, "llm.request", timeline.WithParent(ref.StageID), timeline.WithStageID(tr.StageID), timeline.WithStartTime(started), timeline.WithAttributes(attributes...))
	if run != nil {
		reportCapture(run.Record(agentgo.Event{Type: agentgo.EventModelExecStart, Execution: &execution, Timestamp: started}))
		reportCapture(run.Record(agentgo.Event{Type: agentgo.EventModelRequest, Execution: &execution, ModelRequest: &request}))
	}
	if run != nil {
		ctx = agentgo.ContextWithExecution(ctx, execution)
		ctx = llm.ObserveAttempts(ctx, func(_ context.Context, fact agentgo.ModelAttempt, err error) {
			reportCapture(run.Record(agentgo.Event{Type: agentgo.EventModelAttempt, Execution: &execution, ModelAttempt: &fact, Err: err}))
		})
	}
	response, err := llm.Call(ctx, client, request)
	finished := time.Now()
	duration := finished.Sub(started)
	if err == nil && response == nil {
		err = fmt.Errorf("empty response")
	}
	if err != nil {
		tr.SetError(err, duration)
	} else {
		tr.SetResponse(response, duration)
	}
	if run != nil {
		var message agentgo.AgentMessage
		if response != nil {
			message = response.Message
		}
		reportCapture(run.Record(agentgo.Event{Type: agentgo.EventModelExecEnd, Execution: &execution, Timestamp: finished, Message: message, Err: err}))
	}
	// Export the terminal stage after its response/error, even on cancellation.
	telemetry.EndTimelineStage(ctx, stage, err, timeline.WithEndTime(finished), timeline.WithEndAttributes(timeline.Attribute{Key: "failure", Value: llm.DescribeError(err)}))
	return response, err
}

func (ss *ReviewScope) RecordingModel(client agentgo.ChatModel, taskType TaskType) agentgo.ChatModel {
	return &recordingModel{scope: ss, client: client, taskType: taskType}
}

type recordingModel struct {
	scope    *ReviewScope
	client   agentgo.ChatModel
	taskType TaskType
}

func (c *recordingModel) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	config := agentgo.ResolveCallConfig(opts)
	request := agentgo.LLMRequest{Messages: messages,
		Tools: tools, Config: &config}

	return c.scope.AppendTaskRecord(c.taskType, request.Messages).Call(ctx, c.client, request)
}

func (c *recordingModel) SupportsTools() bool { return c.client.SupportsTools() }
func (c *recordingModel) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return llm.CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return c.Generate(ctx, messages, tools, opts...) })
}

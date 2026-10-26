package recording

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/recording/operation"

	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

// Context carries only Review/Operation identity.
// Existing parentage is preserved when it belongs to this Review.
func (sh *Review) Context(ctx context.Context) context.Context {
	if sh == nil {
		return ctx
	}
	ref, ok := operation.FromContext(ctx)
	if !ok || ref.ReviewID != sh.SessionID {
		ctx = operation.NewContext(ctx, operation.Ref{ReviewID: sh.SessionID, OperationID: ""})
	}
	return ctx
}

func (sh *Review) startOperations() {
	name := "review"
	if sh.ReviewMode == ReviewModeFullScan {
		name = "scan"
	}
	var sink operation.Sink
	if sh.persist != nil {
		sink = sh.persist.writeOperation
	}
	telemetry.ReportRecordingError(operation.Start(sh.SessionID, name, sink))
}

// Begin records work with explicit parentage supplied by this Review adapter.
func Begin(ctx context.Context, name string, attributes ...operation.Attribute) (context.Context, func(error)) {
	return BeginOperation(ctx, name, operation.WithAttributes(attributes...))
}

func BeginOperation(ctx context.Context, name string, opts ...operation.Option) (context.Context, func(error)) {
	ref, _ := operation.FromContext(ctx)
	opts = append([]operation.Option{operation.WithParent(ref.OperationID)}, opts...)
	ctx, stage := telemetry.BeginOperation(ctx, name, opts...)
	return ctx, func(err error) { telemetry.EndOperation(ctx, stage, err) }
}

// Call records bodies in a local framework Session and references it from the
// business journal. Transport phases remain model error diagnostics.
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
		tr.SetResult(response, err, time.Since(started))
		return response, err
	}
	var captureErr error
	reportCapture := func(err error) {
		if err == nil || captureErr != nil {
			return
		}
		captureErr = err
		telemetry.ReportRecordingError(fmt.Errorf("scope %s auxiliary Session: %w", tr.scopeSession.ID, err))
		tr.scopeSession.session.WriteArtifact("recording_gap", map[string]any{"scope_id": tr.scopeSession.ID, "operation_id": tr.OperationID, "error": err.Error()})
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
	attributes := []operation.Attribute{{Key: "execution_id", Value: tr.ExecutionID}, {Key: "task_type", Value: tr.Type}, {Key: "request_no", Value: tr.RequestNo}}
	if tr.scopeSession != nil {
		attributes = append(attributes, operation.Attribute{Key: "scope_id", Value: tr.scopeSession.ID}, operation.Attribute{Key: "kind", Value: tr.scopeSession.Kind}, operation.Attribute{Key: "scope", Value: tr.scopeSession.Scope}, operation.Attribute{Key: "filePath", Value: tr.scopeSession.Path}, operation.Attribute{Key: "paths", Value: tr.scopeSession.Paths})
	}
	if run != nil {
		attributes = append(attributes, operation.Attribute{Key: "session_ref", Value: run.Ref(execution)}, operation.Attribute{Key: "session_id", Value: local.ID()}, operation.Attribute{Key: "session_file", Value: filepath.Base(path)})
	}
	started := time.Now()
	ref, _ := operation.FromContext(ctx)
	ctx, stage := telemetry.BeginOperation(ctx, "llm.request", operation.WithParent(ref.OperationID), operation.WithID(tr.OperationID), operation.WithStartTime(started), operation.WithAttributes(attributes...))
	executor := agentgo.NewModelExecutor(agentgo.ModelExecutorConfig{Middlewares: []agentgo.ModelMiddleware{llm.ModelRequestMiddleware}, OnEvent: func(event agentgo.Event) {
		if run != nil {
			reportCapture(run.Record(event))
		}
	}})
	result, err := executor.Call(ctx, client, agentgo.ModelExecution{Execution: execution, Request: request})
	response := &agentgo.LLMResponse{Message: result.Message}
	finished := time.Now()
	duration := finished.Sub(started)
	tr.SetResult(response, err, duration)
	// Export the terminal stage after its response/error, even on cancellation.
	telemetry.EndOperation(ctx, stage, err, operation.WithEndTime(finished), operation.WithEndAttributes(operation.Attribute{Key: "failure", Value: llm.DescribeError(err)}))
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

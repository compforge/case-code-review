package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/go-stdx/uuid"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// Context binds the Session's single timeline without replacing a stage already
// carried by this operation. Session owns Start/Finish; nested work owns stages.
func (sh *SessionHistory) Context(ctx context.Context) context.Context {
	if sh == nil || sh.timeline == nil {
		return ctx
	}
	return timeline.NewContext(ctx, sh.timeline)
}

func (sh *SessionHistory) startTimeline() {
	t, _ := timeline.New(sh.SessionID, timeline.WithStore(&sessionTimelineStore{MemoryStore: timeline.NewMemoryStore(), session: sh}))
	sh.timeline = t
	operation := "review"
	if sh.ReviewMode == ReviewModeFullScan {
		operation = "scan"
	}
	reportTimelineError(t.Start(context.Background(), operation))
}

// Begin flushes both transitions at the owning boundary. Recording failures
// remain diagnostics; they do not change the operation's business result.
func Begin(ctx context.Context, name string, fields ...timeline.Field) (context.Context, func(error)) {
	t, ok := timeline.FromContext(ctx)
	if !ok {
		return ctx, func(error) {}
	}
	ctx, stage := timeline.BeginContext(ctx, t, name, timeline.WithFields(fields...))
	FlushTimeline(ctx)
	return ctx, func(err error) { stage.End(err); FlushTimeline(ctx) }
}

func FlushTimeline(ctx context.Context) {
	if t, ok := timeline.FromContext(ctx); ok {
		reportTimelineError(t.Flush(context.Background()))
	}
}

func reportTimelineError(err error) {
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr timeline] persistence failed: %v\n", err)
	}
}

// Call records a request under its execution, or under the Session for auxiliary
// calls. Routing and HTTP instrumentation inherit this stage and recording store.
func (tr *TaskRecord) Call(ctx context.Context, client llm.LLMClient, request llm.ChatRequest) (*llm.ChatResponse, error) {
	if tr == nil {
		return client.CompletionsWithCtx(ctx, request)
	}
	if tr.scopeSession != nil {
		ctx = tr.scopeSession.session.Context(ctx)
	}
	fields := []timeline.Field{{Key: "execution_id", Value: tr.ExecutionID}, {Key: "task_type", Value: tr.Type}, {Key: "request_no", Value: tr.RequestNo}}
	if tr.scopeSession != nil {
		fields = append(fields, timeline.Field{Key: "scope_id", Value: tr.scopeSession.ID})
	}
	ctx, finish := Begin(ctx, "llm.request", fields...)
	started := time.Now()
	response, err := client.CompletionsWithCtx(ctx, request)
	duration := time.Since(started)
	if err == nil && (response == nil || len(response.Choices) == 0) {
		err = fmt.Errorf("empty response")
	}
	if err != nil {
		tr.SetError(err, duration)
	} else {
		tr.SetResponse(response, duration)
	}
	// The stage flush includes its preceding response/error, even on cancellation.
	finish(err)
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

type sessionTimelineStore struct {
	*timeline.MemoryStore
	session *SessionHistory
}

func (s *sessionTimelineStore) Merge(ctx context.Context, id string, update timeline.Update) error {
	if err := s.MemoryStore.Merge(ctx, id, update); err != nil {
		return err
	}
	if s.session.persist != nil {
		return s.session.persist.writeTimeline(id, update)
	}
	return nil
}

func (jw *jsonlWriter) writeTimeline(id string, update timeline.Update) error {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	uid := uuid.V4()
	rec := map[string]any{
		"uuid": uid, "parentUuid": jw.lastUUID, "type": "timeline_update",
		"sessionId": jw.sessionID, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"elapsed_ms": jw.elapsedMilliseconds(), "timeline_id": id, "update": update,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err = jw.writer.Write(append(data, '\n')); err != nil {
		return err
	}
	jw.lastUUID = uid
	return jw.writer.Flush()
}

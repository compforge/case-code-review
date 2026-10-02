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

// Call records a request's incremental timeline and terminal model result.
// A nil record supports executions that deliberately have no Session recorder.
func (tr *TaskRecord) Call(ctx context.Context, client llm.LLMClient, request llm.ChatRequest) (*llm.ChatResponse, error) {
	if tr == nil {
		return client.CompletionsWithCtx(ctx, request)
	}
	store := &requestTimelineStore{MemoryStore: timeline.NewMemoryStore(), record: tr}
	t, _ := timeline.New(uuid.V4(), timeline.WithStore(store))
	if err := t.Start(context.Background(), "llm.request"); err != nil {
		tr.timelineError(err)
	}
	ctx = timeline.NewContext(ctx, t)
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
	// The terminal timeline flush also persists the preceding response/error.
	// Cancellation must not discard the evidence explaining a timed-out call.
	if _, collectErr := t.Finish(context.Background(), err); collectErr != nil {
		tr.timelineError(collectErr)
	}
	return response, err
}

// RecordingClient is used where the caller delegates message construction,
// such as relocation. Recording starts before the provider call, not afterward.
func (ss *ScopeSession) RecordingClient(client llm.LLMClient, taskType TaskType) llm.LLMClient {
	return &recordingClient{scope: ss, client: client, taskType: taskType}
}

type recordingClient struct {
	scope    *ScopeSession
	client   llm.LLMClient
	taskType TaskType
}

func (c *recordingClient) CompletionsWithCtx(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	record := c.scope.AppendTaskRecord(c.taskType, request.Messages)
	return record.Call(ctx, c.client, request)
}

type requestTimelineStore struct {
	*timeline.MemoryStore
	record *TaskRecord
}

func (s *requestTimelineStore) Merge(ctx context.Context, id string, update timeline.Update) error {
	if err := s.MemoryStore.Merge(ctx, id, update); err != nil {
		return err
	}
	ss := s.record.scopeSession
	if ss != nil && ss.session.persist != nil {
		return ss.session.persist.writeTimeline(ss, s.record, id, update)
	}
	return nil
}

func (tr *TaskRecord) timelineError(err error) {
	fmt.Fprintf(console.Err(), "[ccr timeline] execution=%s task=%s request=%d persistence failed: %v\n", tr.ExecutionID, tr.Type, tr.RequestNo, err)
}

func (jw *jsonlWriter) writeTimeline(ss *ScopeSession, request *TaskRecord, id string, update timeline.Update) error {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	uid := uuid.V4()
	rec := map[string]any{
		"uuid": uid, "parentUuid": jw.lastUUID, "type": "timeline_update",
		"sessionId": jw.sessionID, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"elapsed_ms": jw.elapsedMilliseconds(), "taskType": string(request.Type),
		"request_no": request.RequestNo, "timeline_id": id, "update": update,
	}
	addScopeFields(rec, ss)
	addExecutionField(rec, request.ExecutionID)
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err = jw.writer.Write(append(data, '\n')); err != nil {
		return err
	}
	jw.lastUUID = uid
	// Begin and each transition must survive a process interruption. Buffered
	// model prompts preceding this event become visible in the same flush.
	return jw.writer.Flush()
}

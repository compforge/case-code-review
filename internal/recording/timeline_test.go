package recording

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/recording/operation"

	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

type waitingTimelineClient struct{ ready chan struct{} }

func (c waitingTimelineClient) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	ref, _ := operation.FromContext(ctx)
	ctx, phase := telemetry.BeginOperation(ctx, "await_response", operation.WithParent(ref.OperationID))
	close(c.ready)
	<-ctx.Done()
	telemetry.EndOperation(ctx, phase, ctx.Err())
	return nil, ctx.Err()
}

func readRequestTimeline(t *testing.T, path string) (operation.Snapshot, []map[string]any) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	transcript, err := ReadReview(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return transcript.Journal, transcript.Records
}

func TestTimelinePersistsBeforeRequestReturnsAndAfterCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	history := New(t.TempDir(), "test", "model", SessionOptions{})
	defer history.Finalize()
	scope := history.GetOrCreateScope(Scope{ID: "unit-1", Kind: "unit", Type: "file"})
	record := scope.AppendTaskRecord(MainTask, []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("test")}}})
	path, err := history.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := waitingTimelineClient{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := record.Call(ctx, client, agentgo.LLMRequest{Config: &agentgo.CallConfig{}})
		done <- err
	}()
	select {
	case <-client.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	doc, records := readRequestTimeline(t, path)
	deadline := time.Now().Add(5 * time.Second)
	for len(doc.Operations) != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)

		doc, records = readRequestTimeline(t, path)
	}
	if doc.Status != operation.Unknown || len(doc.Operations) != 2 || doc.Operations[0].Status != operation.Running {
		t.Fatalf("running facts not persisted: %+v", doc)
	}
	var request operation.Record
	for _, stage := range doc.Operations {
		if stage.Name == "llm.request" {
			request = stage
		}
	}
	executionID, _ := operation.AttributeValue[string](request.Attributes, "execution_id")
	scopeID, _ := operation.AttributeValue[string](request.Attributes, "scope_id")
	requestNo, _ := operation.AttributeValue[int](request.Attributes, "request_no")
	if executionID != "" || scopeID != "unit-1" || requestNo != 1 {
		t.Fatalf("lost request identity: %+v", request)
	}
	for _, stage := range doc.Operations {
		if stage.Name == "await_response" && stage.ParentID != request.ID {
			t.Fatal("HTTP phase escaped request")
		}
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not stop")
	}

	doc, records = readRequestTimeline(t, path)
	if doc.Status != operation.Unknown || doc.Operations[0].Status != operation.Canceled {
		t.Fatalf("terminal facts lost after cancellation: %+v", doc)
	}
	errorPersisted := false
	for _, event := range records {
		if event["type"] == "llm_error" {
			errorPersisted = true
		}
	}
	if !errorPersisted {
		t.Fatal("terminal timeline did not flush the LLM error")
	}
	history.Finalize(context.Canceled)
	doc, _ = readRequestTimeline(t, path)
	if doc.Status != operation.Canceled {
		t.Fatalf("run result = %s", doc.Status)
	}
	if record.Error == "" {
		t.Fatal("missing LLM error")
	}
}

func TestFinalSnapshotSurvivesCacheReleaseAndSessionsStayIsolated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := New(t.TempDir(), "main", "model", SessionOptions{})
	second := New(t.TempDir(), "main", "model", SessionOptions{})
	defer first.Finalize()
	defer second.Finalize()
	ctx, finish := Begin(first.Context(context.Background()), "first-work")
	_, finishSecond := Begin(second.Context(ctx), "second-work")
	finish(nil)
	first.Finalize()
	if _, err := operation.Read(first.SessionID); !errors.Is(err, operation.ErrNotFound) {
		t.Fatalf("finished Session retained cache: %v", err)
	}
	path, err := first.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, records := readRequestTimeline(t, path)
	if snapshot.Status != operation.Succeeded || len(snapshot.Operations) != 1 || snapshot.Operations[0].Name != "first-work" {
		t.Fatalf("lost final snapshot: %+v", snapshot)
	}
	if records[len(records)-1]["type"] != "session_end" {
		t.Fatal("final snapshot was not exported before Session close")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("timeline_snapshot")) {
		t.Fatal("review manifest contains a snapshot")
	}

	other, err := operation.Read(second.SessionID)
	if err != nil || len(other.Operations) != 1 || other.Operations[0].Name != "second-work" || other.Operations[0].ParentID != "" {
		t.Fatalf("Session contexts shared parentage or cache: %+v %v", other, err)
	}
	finishSecond(nil)
}

func (c waitingTimelineClient) SupportsTools() bool { return true }
func (c waitingTimelineClient) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return llm.CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return c.Generate(ctx, messages, tools, opts...) })
}

package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/go-stdx/timeline/store"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

type waitingTimelineClient struct{ ready chan struct{} }

func (c waitingTimelineClient) CompletionsWithCtx(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	ref, _ := timeline.StageFromContext(ctx)
	ctx, phase := telemetry.BeginTimelineStage(ctx, "await_response", timeline.WithParent(ref.StageID))
	close(c.ready)
	<-ctx.Done()
	telemetry.EndTimelineStage(ctx, phase, ctx.Err())
	return nil, ctx.Err()
}

func readRequestTimeline(t *testing.T, path string) (timeline.Snapshot, []map[string]any) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	transcript, err := ReadTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	return transcript.Timeline, transcript.Records
}

func TestTimelinePersistsBeforeRequestReturnsAndAfterCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	history := New(t.TempDir(), "test", "model", SessionOptions{})
	defer history.Finalize()
	scope := history.GetOrCreateScope(Scope{ID: "unit-1", Kind: "unit", Type: "file"})
	record := scope.AppendExecutionTaskRecord("execution-1", MainTask, []llm.Message{llm.NewTextMessage("user", "test")})
	path, err := history.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := waitingTimelineClient{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := record.Call(ctx, client, llm.ChatRequest{}); done <- err }()
	select {
	case <-client.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	doc, records := readRequestTimeline(t, path)
	deadline := time.Now().Add(5 * time.Second)
	for len(doc.Stages) != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		doc, records = readRequestTimeline(t, path)
	}
	if doc.Status != timeline.Unknown || len(doc.Stages) != 2 || doc.Stages[0].Status != timeline.Running {
		t.Fatalf("running facts not persisted: %+v", doc)
	}
	var request timeline.Stage
	for _, stage := range doc.Stages {
		if stage.Name == "llm.request" {
			request = stage
		}
	}
	executionID, _ := timeline.AttributeValue[string](request.Attributes, "execution_id")
	scopeID, _ := timeline.AttributeValue[string](request.Attributes, "scope_id")
	requestNo, _ := timeline.AttributeValue[int](request.Attributes, "request_no")
	if executionID != "execution-1" || scopeID != "unit-1" || requestNo != 1 {
		t.Fatalf("lost request identity: %+v", request)
	}
	for _, stage := range doc.Stages {
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
	if doc.Status != timeline.Unknown || doc.Stages[0].Status != timeline.Canceled {
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
	if doc.Status != timeline.Canceled {
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
	if _, err := timeline.Read(context.Background(), first.SessionID, false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("finished Session retained cache: %v", err)
	}
	path, err := first.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, records := readRequestTimeline(t, path)
	if snapshot.Status != timeline.Succeeded || len(snapshot.Stages) != 1 || snapshot.Stages[0].Name != "first-work" {
		t.Fatalf("lost final snapshot: %+v", snapshot)
	}
	if records[len(records)-1]["type"] != "session_end" {
		t.Fatal("final snapshot was not exported before Session close")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if !bytes.Contains(lines[len(lines)-2], []byte(`"type":"timeline_snapshot"`)) {
		t.Fatal("final snapshot must precede session_end")
	}
	other, err := timeline.Read(context.Background(), second.SessionID, false)
	if err != nil || len(other.Stages) != 1 || other.Stages[0].Name != "second-work" || other.Stages[0].ParentID != "" {
		t.Fatalf("Session contexts shared parentage or cache: %+v %v", other, err)
	}
	finishSecond(nil)
}

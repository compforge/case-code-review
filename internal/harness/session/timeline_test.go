package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/llm"
)

type waitingTimelineClient struct{ ready chan struct{} }

func (c waitingTimelineClient) CompletionsWithCtx(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	recorder, _ := timeline.FromContext(ctx)
	_, phase := timeline.BeginContext(ctx, recorder, "await_response")
	if err := recorder.Flush(context.Background()); err != nil {
		return nil, err
	}
	close(c.ready)
	<-ctx.Done()
	phase.End(ctx.Err())
	if err := recorder.Flush(context.Background()); err != nil {
		return nil, err
	}
	return nil, ctx.Err()
}

func readRequestTimeline(t *testing.T, path string) (timeline.Document, []map[string]any) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1024*1024)
	var doc timeline.Document
	var records []map[string]any
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
		if record["type"] != "timeline_update" {
			continue
		}
		var event struct {
			ID     string          `json:"timeline_id"`
			Update timeline.Update `json:"update"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		doc, _, err = timeline.MergeDocument(event.ID, doc, event.Update)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return doc, records
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
	if doc.Status != timeline.Running || len(doc.Stages) != 2 || doc.Stages[0].Status != timeline.Running {
		t.Fatalf("running facts not persisted: %+v", doc)
	}
	var request timeline.Stage
	for _, stage := range doc.Stages {
		if stage.Name == "llm.request" {
			request = stage.Stage
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
	if doc.Status != timeline.Running || doc.Stages[0].Status != timeline.Canceled {
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

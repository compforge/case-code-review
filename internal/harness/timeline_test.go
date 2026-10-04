package harness

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

type tracedTestClient struct{ llm.LLMClient }

func (c tracedTestClient) CompletionsWithCtx(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	ctx, end := session.Begin(ctx, "llm.provider")
	_, endHTTP := session.Begin(ctx, "http.request")
	response, err := c.LLMClient.CompletionsWithCtx(ctx, request)
	endHTTP(err)
	end(err)
	return response, err
}

func TestSessionTimelineJoinsConcurrentExecutionsAndRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	history := session.New(t.TempDir(), "main", "test", session.SessionOptions{})
	ctx := history.Context(context.Background())
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reviewCtx, finish := session.Begin(ctx, "review.unit", timeline.Field{Key: "unit_id", Value: fmt.Sprint(i)})
			client := tracedTestClient{&scriptedClient{responses: []*llm.ChatResponse{
				toolCallResponseID("same-call", "inspect", `{}`, nil),
				toolCallResponseID("same-done", "task_done", `{}`, nil),
			}}}
			result, err := runExecution(reviewCtx, ExecutionSpec{
				LLMClient: client, Session: history, Scope: session.Scope{ID: fmt.Sprint(i), Kind: "unit"},
				Messages: []agentgo.AgentMessage{msg.Text("user", "review")},
				ToolDefs: []llm.ToolDef{toolDef("inspect"), toolDef("task_done")}, MaxTurns: 2,
				ToolHandler: toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
					if request.Tool.Name() != "inspect" {
						return tool.TaskCheckpoint{}, false
					}
					return tool.Of("ok"), true
				}),
			})
			if err == nil && result.State != OutcomeCompleted {
				err = fmt.Errorf("outcome %s", result.State)
			}
			finish(err)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	history.Finalize()
	recorder, _ := timeline.FromContext(ctx)
	snapshot, err := recorder.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[timeline.StageID]timeline.Stage{}
	counts := map[string]int{}
	for _, stage := range snapshot.Stages {
		byID[stage.ID] = stage
		counts[stage.Name]++
		if stage.Status == timeline.Running {
			t.Errorf("unterminated stage: %+v", stage)
		}
	}
	if snapshot.ID != history.SessionID || snapshot.Status != timeline.Succeeded {
		t.Fatalf("run = %+v", snapshot)
	}
	if counts["execution"] != 2 || counts["model.attempt"] != 4 || counts["llm.request"] != 4 || counts["http.request"] != 4 {
		t.Fatalf("stage counts = %v", counts)
	}
	for _, stage := range snapshot.Stages {
		if stage.ParentID != snapshot.RootStageID {
			if _, ok := byID[stage.ParentID]; !ok {
				t.Errorf("orphan stage %+v", stage)
			}
		}
		if stage.Name == "llm.request" {
			parent := byID[stage.ParentID]
			if parent.Name != "model.attempt" || parent.StartedAt.After(stage.StartedAt) || parent.FinishedAt.Before(stage.FinishedAt) {
				t.Errorf("request escaped its physical model attempt: %+v / %+v", stage, parent)
			}
		}
	}
	// Assistant call IDs repeat across executions; namespacing prevents collisions.
	if counts["tool.execution"] != 4 || counts["tool.queue"] != 4 {
		t.Fatalf("tool stages collided: %v", counts)
	}
}

func TestExecutionTimelineUsesSourceTimeAndLeavesMissingEndIncomplete(t *testing.T) {
	recorder, _ := timeline.New("run")
	if err := recorder.Start(context.Background(), "review"); err != nil {
		t.Fatal(err)
	}
	ctx, root := timeline.BeginContext(timeline.NewContext(context.Background(), recorder), recorder, "execution", timeline.WithStageID("exec"))
	r := &executionTimeline{ctx: ctx, timeline: recorder, id: "exec", root: root, stages: map[timeline.StageID]timeline.StageHandle{}, parents: map[string]timeline.StageID{}}
	start := time.Now().Add(-time.Second)
	r.observe(agentgo.Event{Type: agentgo.EventTurnStart, TurnIndex: 1, Timestamp: start})
	execution := agentgo.Execution{ID: "model-1", Kind: agentgo.ExecutionKindModel, TurnIndex: 1, Attempt: 1}
	r.observe(agentgo.Event{Type: agentgo.EventModelExecStart, Execution: &execution, Timestamp: start})
	r.observe(agentgo.Event{Type: agentgo.EventModelExecEnd, Execution: &execution, Timestamp: start.Add(20 * time.Millisecond)})
	r.finish(ExecutionResult{State: OutcomeCompleted}, nil)
	snapshot, err := recorder.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range snapshot.Stages {
		switch stage.Name {
		case "model.attempt":
			if !stage.StartedAt.Equal(start) || stage.Duration(snapshot.CapturedAt) != 20*time.Millisecond {
				t.Fatalf("used observer clock: %+v", stage)
			}
		case "turn":
			if stage.Status != timeline.Running {
				t.Fatal("invented missing turn completion")
			}
		case "execution":
			if stage.Status != timeline.Failed {
				t.Fatal("missing event evidence looked complete")
			}
		}
	}
}

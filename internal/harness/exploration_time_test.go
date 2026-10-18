package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
)

type explorationClockClient struct {
	*scriptedClient
	delays    []time.Duration
	attempts  []llm.ChatRequest
	deadlines []time.Time
}

func (c *explorationClockClient) CompletionsWithCtx(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	c.attempts = append(c.attempts, request)
	deadline, _ := ctx.Deadline()
	c.deadlines = append(c.deadlines, deadline)
	delay := c.delays[0]
	c.delays = c.delays[1:]
	select {
	case <-time.After(delay):
		return c.scriptedClient.CompletionsWithCtx(ctx, request)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// +case:id=exploration_expiry_preserves_wrapup,desc=`a tool finishes after the exploration limit`,expect=`the next model request enters wrap-up and retains the caller deadline`
func TestExplorationExpiryDuringToolCallPreservesWrapUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &explorationClockClient{scriptedClient: &scriptedClient{responses: []*llm.ChatResponse{
			toolCallResponse("read_files", `{"reads":[{"file_path":"a.go"}]}`, nil),
			toolCallResponseID("result", "submit_result", `{}`, nil),
		}}, delays: []time.Duration{4 * time.Minute, 10 * time.Minute}}
		submitted := 0
		handler := toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
			if request.Tool.Name() == "read_files" {
				time.Sleep(2 * time.Minute)
				return tool.Of("evidence"), true
			}
			if request.Tool.Name() != "submit_result" {
				t.Errorf("investigation tool executed after expiry: %s", request.Tool.Name())
				return tool.TaskCheckpoint{}, true
			}
			submitted++
			return tool.CompleteWith("accepted"), true
		})
		began := time.Now()
		result, err := runExecution(context.Background(), ExecutionSpec{
			LLMClient: client, Model: "test", Messages: []agentgo.AgentMessage{msg.Text("user", "review")},
			ToolDefs: []llm.ToolDef{toolDef("read_files"), toolDef("submit_result")}, ToolHandler: handler,
			MaxTurns: 10, WrapUpAt: began.Add(5 * time.Minute), WrapUpPrompt: "exploration finished; submit mature claims", WrapUpAllowedTools: []string{"submit_result"}, NaturalCompletion: true,
		})
		if err != nil || result.State != OutcomeCompleted || submitted != 1 {
			t.Fatalf("result=%+v submissions=%d err=%v", result, submitted, err)
		}
		requests := client.Requests()
		if len(requests) != 2 || strings.Contains(requestText(requests[0]), "exploration finished") || !strings.Contains(requestText(requests[1]), "exploration finished") || strings.Contains(requestText(requests[1]), "Final completion turn") {
			t.Fatalf("unexpected wrap-up transition: %+v", requests)
		}
		assertStableToolRequests(t, requests)
		if time.Since(began) != 16*time.Minute {
			t.Fatalf("wrap-up did not outlive exploration: %s", time.Since(began))
		}
	})
}

func TestExplorationClockDoesNotDetachCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(time.Minute, cancel)
		defer timer.Stop()
		client := &explorationClockClient{scriptedClient: &scriptedClient{}, delays: []time.Duration{10 * time.Minute}}
		result, _ := runExecution(ctx, ExecutionSpec{LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, MaxTurns: 5, WrapUpAt: time.Now().Add(-time.Minute), WrapUpPrompt: "finish now", NaturalCompletion: true})
		if result.State != OutcomeAborted {
			t.Fatalf("caller cancellation lost during wrap-up: %+v", result)
		}
	})
}

func TestExplorationClockHasNoEarlyTimeReserve(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTurnController(ExecutionSpec{WrapUpAt: time.Now().Add(5 * time.Minute), WrapUpPrompt: "finish"})
		turn := agentgo.BeforeTurnContext{TurnIndex: 1}
		time.Sleep(4*time.Minute + 59*time.Second)
		messages, err := c.BeforeTurn(context.Background(), turn)
		if err != nil || len(messages) != 0 {
			t.Fatalf("early wrap-up: %v %v", messages, err)
		}
		time.Sleep(time.Second)
		messages, err = c.BeforeTurn(context.Background(), turn)
		if err != nil || len(messages) != 1 {
			t.Fatalf("missing boundary wrap-up: %v %v", messages, err)
		}
		messages, err = c.BeforeTurn(context.Background(), turn)
		if err != nil || len(messages) != 0 {
			t.Fatalf("repeated reminder: %v %v", messages, err)
		}
	})
}

func TestDynamicWrapUpBoundaryControlsTurnsAndInFlightTools(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		boundary := began.Add(50 * time.Second)
		c := newTurnController(ExecutionSpec{WrapUpPrompt: "finish", WrapUpDeadline: func() time.Time { return boundary }})
		turn := agentgo.BeforeTurnContext{TurnIndex: 1}
		messages, _ := c.BeforeTurn(ctx, turn)
		if len(messages) != 0 {
			t.Fatal("generic 90-second reserve overrode explicit budget")
		}
		boundary = began.Add(20 * time.Second)
		time.Sleep(21 * time.Second)
		c.checkTime(ctx)
		if !c.WrapUpIssued() {
			t.Fatal("adjusted deadline not checked before tool execution")
		}
		messages, _ = c.BeforeTurn(ctx, turn)
		if len(messages) != 1 {
			t.Fatal("in-flight expiry lost wrap-up reminder")
		}
	})
}

// +case:id=exploration_model_deadline,desc=`a model call starts at second 9 with exploration ending at second 10`,expect=`the failed request is retained and one wrap-up continuation completes under the original hard deadline`
func TestExplorationModelCallResumesWrapUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		client := &explorationClockClient{scriptedClient: &scriptedClient{responses: []*llm.ChatResponse{textResponse("review complete")}}, delays: []time.Duration{time.Minute, time.Second}}
		time.Sleep(9 * time.Second)
		var events []ExecutionEvent
		result, err := runExecution(ctx, ExecutionSpec{
			LLMClient: client,
			Messages:  []agentgo.AgentMessage{msg.Text("user", "review")},
			MaxTurns:  10, WrapUpAt: began.Add(10 * time.Second), WrapUpPrompt: "finish review now", NaturalCompletion: true,
			Events: EventSinkFunc(func(e ExecutionEvent) { events = append(events, e) }),
		})
		if err != nil || result.State != OutcomeCompleted || result.Turns != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if ctx.Err() != nil || time.Since(began) != 11*time.Second {
			t.Fatalf("wrong recovery timing: elapsed=%s parent=%v", time.Since(began), ctx.Err())
		}
		if len(client.attempts) != 2 || !client.deadlines[0].Equal(began.Add(10*time.Second)) || !client.deadlines[1].Equal(began.Add(12*time.Second)) {
			t.Fatalf("requests/deadlines=%d/%v", len(client.attempts), client.deadlines)
		}
		if strings.Contains(requestText(client.attempts[0]), "finish review now") || strings.Count(requestText(client.attempts[1]), "finish review now") != 1 {
			t.Fatal("wrap-up prompt was not committed exactly once after the failed call")
		}
		ends := 0
		for _, event := range events {
			if event.Type == EventExecutionError {
				t.Errorf("recoverable error escaped as terminal: %v", event.Err)
			}
			if event.Type == EventExecutionEnd {
				ends++
			}
		}
		if ends != 1 {
			t.Fatalf("terminal events=%d", ends)
		}
	})
}

func TestExplorationRecoveryPersistsOneExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	history := recording.New(t.TempDir(), "main", "test", recording.SessionOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &explorationClockClient{scriptedClient: &scriptedClient{responses: []*llm.ChatResponse{textResponse("complete")}}, delays: []time.Duration{time.Minute, 0}}
	result, err := runExecution(ctx, ExecutionSpec{
		LLMClient: client, Session: history, Scope: recording.Scope{ID: "unit-1", Kind: "unit"},
		Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, MaxTurns: 10,
		WrapUpAt: time.Now().Add(time.Second), WrapUpPrompt: "finish", NaturalCompletion: true,
	})
	if err != nil || result.State != OutcomeCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	history.Finalize()
	path, err := history.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	transcript, err := recording.ReadReview(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	facts := transcript.ExecutionFacts()
	if len(facts) != 1 || facts[0]["outcome"] != OutcomeCompleted {
		t.Fatalf("execution facts=%v", facts)
	}
	counts := map[string]int{}
	for _, record := range transcript.Records {
		counts[record["type"].(string)]++
		if record["artifact_kind"] == "execution_wrap_up_resumed" {
			counts["resumed"]++
		}
	}
	if counts["llm_request"] != 2 || counts["llm_error"] != 1 || counts["llm_response"] != 1 || counts["resumed"] != 1 {
		t.Fatalf("records=%v", counts)
	}
	stageCounts := map[string]int{}
	for _, stage := range transcript.Timeline.Stages {
		stageCounts[stage.Name]++
		if stage.Status == timeline.Running {
			t.Errorf("unfinished stage: %+v", stage)
		}
	}
	if stageCounts["turn"] != 2 || stageCounts["model.attempt"] != 2 {
		t.Fatalf("colliding recovery stages: %v", stageCounts)
	}
	stages := transcript.StageIndex()
	for _, record := range transcript.Records {
		if record["type"] != "context_projected" {
			continue
		}
		id, _ := record["stage_id"].(string)
		if stage, ok := stages[timeline.StageID(id)]; !ok || stage.Name != "model.attempt" {
			t.Fatalf("projection lost attempt identity: %v", record)
		}
	}
}

// +case:id=wrapup_recovery_preserves_results,desc=`one accepted result precedes an exploration request timeout`,expect=`continuation retains messages artifacts usage and spent turns without repeating the result tool`
func TestExplorationRecoveryPreservesCommittedState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		first := toolCallResponseID("accepted", "submit_result", `{}`, &llm.UsageInfo{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
		last := textResponse("review complete")
		last.Usage = &llm.UsageInfo{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}
		client := &explorationClockClient{scriptedClient: &scriptedClient{responses: []*llm.ChatResponse{first, last}}, delays: []time.Duration{time.Second, time.Minute, time.Second}}
		submitted := 0
		session := &LoopSession{}
		defer session.Close()
		initial := msg.NewFile("a.go", 1, 1, 1, "1|evidence")
		result, err := runExecution(ctx, ExecutionSpec{
			LoopSession: session, LLMClient: client, Messages: []agentgo.AgentMessage{initial},
			ToolDefs: []llm.ToolDef{toolDef("submit_result")},
			ToolHandler: toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
				submitted++
				return tool.Of("accepted hypothesis h-1"), true
			}),
			MaxTurns: 4, WrapUpAt: time.Now().Add(10 * time.Second), WrapUpPrompt: "finish review now", WrapUpAllowedTools: []string{"submit_result"}, NaturalCompletion: true,
		})
		if err != nil || result.State != OutcomeCompleted || result.Turns != 2 || result.ToolCalls != 1 || result.Usage.TotalTokens != 12 || submitted != 1 || len(session.state.Artifacts) != 1 {
			t.Fatalf("result=%+v submitted=%d artifacts=%d err=%v", result, submitted, len(session.state.Artifacts), err)
		}
		if len(client.attempts) != 3 || !strings.Contains(requestText(client.attempts[2]), "accepted hypothesis h-1") || !strings.Contains(requestText(client.attempts[2]), "1|evidence") {
			t.Fatal("accepted context was not retained")
		}
		assertStableToolRequests(t, client.attempts)
	})
}

func TestExplorationRecoveryRetainsCallerLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		hard, abort time.Duration
		want        string
		calls       int
	}{
		{name: "shorter hard deadline", hard: 5 * time.Second, want: OutcomeTimeout, calls: 1},
		{name: "equal hard deadline", hard: 10 * time.Second, want: OutcomeTimeout, calls: 1},
		{name: "cancel exploration", hard: 12 * time.Second, abort: 5 * time.Second, want: OutcomeAborted, calls: 1},
		{name: "wrap-up hard deadline", hard: 12 * time.Second, want: OutcomeTimeout, calls: 2},
		{name: "cancel wrap-up", hard: 12 * time.Second, abort: 11 * time.Second, want: OutcomeAborted, calls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), tc.hard)
				defer cancel()
				if tc.abort > 0 {
					time.AfterFunc(tc.abort, cancel)
				}
				client := &explorationClockClient{scriptedClient: &scriptedClient{}, delays: []time.Duration{time.Minute, time.Minute}}
				result, err := runExecution(ctx, ExecutionSpec{LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, MaxTurns: 10, WrapUpAt: time.Now().Add(10 * time.Second), WrapUpPrompt: "finish", NaturalCompletion: true})
				if err == nil || result.State != tc.want || len(client.attempts) != tc.calls {
					t.Fatalf("result=%+v calls=%d err=%v", result, len(client.attempts), err)
				}
			})
		})
	}
}

func TestExplorationMiddlewareDistinguishesRequestFailures(t *testing.T) {
	for _, kind := range []agentgo.ExecutionKind{agentgo.ExecutionKindModel, agentgo.ExecutionKindCompact} {
		for _, expired := range []bool{false, true} {
			t.Run(string(kind)+fmt.Sprint(expired), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
					defer cancel()
					e := &Execution{turns: newTurnController(ExecutionSpec{WrapUpAt: time.Now().Add(10 * time.Second), WrapUpPrompt: "finish"})}
					_, err := e.modelMiddleware()(ctx, agentgo.ModelExecution{Execution: agentgo.Execution{Kind: kind}}, func(callCtx context.Context, _ agentgo.ModelExecution) (agentgo.ModelResult, error) {
						if expired {
							<-callCtx.Done()
						}
						return agentgo.ModelResult{}, context.DeadlineExceeded
					})
					if errors.Is(err, errExplorationExpired) != expired || !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
						t.Fatalf("err=%v parent=%v", err, ctx.Err())
					}
				})
			})
		}
	}
}

func TestExplorationRecoveryFromSummaryTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		var messages []agentgo.AgentMessage
		for range 8 {
			messages = append(messages, msg.Text("user", strings.Repeat("review evidence and reasoning ", 30)))
		}
		client := &explorationClockClient{scriptedClient: &scriptedClient{responses: []*llm.ChatResponse{
			textResponse("<summary>preserve the confirmed review facts</summary>"), textResponse("review complete"),
		}}, delays: []time.Duration{time.Minute, time.Second, 0}}
		result, err := runExecution(ctx, ExecutionSpec{LLMClient: client, Messages: messages, MaxTurns: 10, MaxTokens: 400, ContextWindow: 400,
			WrapUpAt: time.Now().Add(10 * time.Second), WrapUpPrompt: "finish", NaturalCompletion: true})
		if err != nil || result.State != OutcomeCompleted || len(client.attempts) != 3 {
			t.Fatalf("result=%+v calls=%d err=%v", result, len(client.attempts), err)
		}
		if !strings.Contains(requestText(client.attempts[2]), "preserve the confirmed review facts") || !strings.Contains(requestText(client.attempts[2]), "finish") {
			t.Fatalf("recovered summary or wrap-up reminder missing: %s", requestText(client.attempts[2]))
		}
	})
}

func TestExplorationRecoveryClosesInvestigationTools(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		client := &explorationClockClient{scriptedClient: &scriptedClient{responses: []*llm.ChatResponse{
			toolCallResponse("read_files", `{}`, nil), textResponse("review complete"),
		}}, delays: []time.Duration{time.Minute, 0, 0}}
		reads := 0
		result, err := runExecution(ctx, ExecutionSpec{LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "review")},
			ToolDefs: []llm.ToolDef{toolDef("read_files"), toolDef("submit_result")},
			ToolHandler: toolHandlerFunc(func(context.Context, ToolRequest) (tool.TaskCheckpoint, bool) {
				reads++
				return tool.Of("evidence"), true
			}),
			MaxTurns: 10, WrapUpAt: time.Now().Add(10 * time.Second), WrapUpPrompt: "finish", WrapUpAllowedTools: []string{"submit_result"}, NaturalCompletion: true})
		if err != nil || result.State != OutcomeCompleted || reads != 0 || len(client.attempts) != 3 {
			t.Fatalf("result=%+v reads=%d calls=%d err=%v", result, reads, len(client.attempts), err)
		}
		if !strings.Contains(requestText(client.attempts[2]), "Final completion turn") {
			t.Fatal("missing final corrective turn")
		}
	})
}

func TestModelMiddlewareDeadline(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		parent, fixed, dynamic time.Duration
		wrapUp                 bool
		kind                   agentgo.ExecutionKind
		want                   time.Duration
	}{
		{name: "exploration", parent: 10 * time.Minute, fixed: 5 * time.Minute, want: 5 * time.Minute},
		{name: "shorter caller", parent: time.Minute, fixed: 5 * time.Minute, want: time.Minute},
		{name: "dynamic boundary", parent: 10 * time.Minute, fixed: 5 * time.Minute, dynamic: 3 * time.Minute, want: 3 * time.Minute},
		{name: "wrap-up", parent: 10 * time.Minute, fixed: 5 * time.Minute, wrapUp: true, want: 10 * time.Minute},
		{name: "no exploration limit", parent: 10 * time.Minute, want: 10 * time.Minute},
		{name: "unlimited"},
		{name: "summarization", parent: 10 * time.Minute, fixed: 5 * time.Minute, kind: agentgo.ExecutionKindCompact, want: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			began := time.Now()
			ctx := context.Background()
			if tc.parent > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, began.Add(tc.parent))
				defer cancel()
			}
			spec := ExecutionSpec{WrapUpPrompt: "finish"}
			if tc.fixed > 0 {
				spec.WrapUpAt = began.Add(tc.fixed)
			}
			if tc.dynamic > 0 {
				spec.WrapUpDeadline = func() time.Time { return began.Add(tc.dynamic) }
			}
			e := &Execution{turns: newTurnController(spec)}
			e.turns.wrapUpIssued = tc.wrapUp
			kind := tc.kind
			if kind == "" {
				kind = agentgo.ExecutionKindModel
			}
			_, err := e.modelMiddleware()(ctx, agentgo.ModelExecution{Execution: agentgo.Execution{Kind: kind}}, func(callCtx context.Context, _ agentgo.ModelExecution) (agentgo.ModelResult, error) {
				deadline, limited := callCtx.Deadline()
				if limited != (tc.want > 0) || limited && !deadline.Equal(began.Add(tc.want)) {
					t.Fatalf("deadline=%s limited=%v want=%s", deadline, limited, tc.want)
				}
				return agentgo.ModelResult{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if ctx.Err() != nil {
				t.Fatalf("model cleanup canceled parent: %v", ctx.Err())
			}
		})
	}
}

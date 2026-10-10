package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

type explorationClockClient struct {
	*scriptedClient
	delays []time.Duration
}

func (c *explorationClockClient) CompletionsWithCtx(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
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

// +case:id=exploration_model_deadline,desc=`an exploration call starts one second before its deadline`,expect=`the request stops at that deadline while its parent remains live; no automatic wrap-up recovery is claimed`
func TestExplorationModelCallUsesRemainingDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		client := &explorationClockClient{scriptedClient: &scriptedClient{}, delays: []time.Duration{time.Minute}}
		time.Sleep(5*time.Minute - time.Second)
		result, err := runExecution(ctx, ExecutionSpec{
			LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "review")},
			MaxTurns: 10, WrapUpAt: began.Add(5 * time.Minute), WrapUpPrompt: "finish", NaturalCompletion: true,
		})
		if !errors.Is(err, context.DeadlineExceeded) || result.State != OutcomeLLMError {
			t.Fatalf("request timeout was not propagated: result=%+v err=%v", result, err)
		}
		if ctx.Err() != nil || time.Since(began) != 5*time.Minute {
			t.Fatalf("wrong cancellation boundary: elapsed=%s parent=%v", time.Since(began), ctx.Err())
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

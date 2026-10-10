package runner

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type timedReviewClient func(context.Context, llm.ChatRequest) (*llm.ChatResponse, error)

func (f timedReviewClient) CompletionsWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return f(ctx, req)
}

func waitReview(ctx context.Context, delay time.Duration) error {
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func reviewText(text string) *llm.ChatResponse {
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: &text}, FinishReason: "stop"}}}
}
func reviewCall(id, name string, arguments any) *llm.ChatResponse {
	data, _ := json.Marshal(arguments)
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: string(data)}}}}, FinishReason: "tool_calls"}}}
}
func suspicion(id string) *llm.ChatResponse {
	return reviewCall(id, "submit_hypothesis", map[string]any{
		"path": "a.go", "existing_code": "_ = 0", "content": id,
		"trigger": id, "impact": id, "change_attribution": "changed assignment",
		"evidence": []string{"a.go:4"}, "uncertainty": "verify caller", "category": "bug", "severity": "high",
	})
}
func confirmed() *llm.ChatResponse {
	return reviewCall("assessment", "submit_assessment", map[string]any{
		"support": "supported", "attribution": "caused", "value": "actionable", "novelty": "new",
		"reason": "Unit diff proves the changed assignment", "evidence": []string{"a.go:4"},
	})
}
func timeoutRunner(t *testing.T, client llm.LLMClient) (*Runner, *session.SessionHistory) {
	t.Helper()
	repo := t.TempDir()
	history := session.New(repo, "main", "test", session.SessionOptions{})
	tpl := template.Template{
		MainTask:             template.LlmConversation{Messages: []template.ChatMessage{{Role: "system", Content: "R1"}, {Role: "user", Content: "{{diff}}"}}},
		HypothesisReviewTask: &template.LlmConversation{Messages: []template.ChatMessage{{Role: "system", Content: "R2"}, {Role: "user", Content: "{{hypothesis}}"}}},
		MaxTokens:            100000, MaxToolRequestTimes: 30,
	}
	a := New(Args{RepoDir: repo, Session: history, LLMClient: client, MaxConcurrency: 1, ConcurrentTaskTimeout: 10,
		MainToolDefs: []llm.ToolDef{unitreview.HypothesisToolDef()}, Template: tpl})
	a.changes = []change.Change{goDiff("a.go", 1)}
	return a, history
}
func timeoutTranscript(t *testing.T, history *session.SessionHistory) []map[string]any {
	t.Helper()
	history.Flush()
	path, err := history.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

// +case:id=unit_budget_borrows_discovery_time,desc=`R1 ends after three minutes and R2 needs four more`,expect=`R2 completes past the nominal three-minute allocation before the shared Unit deadline; debrief follows Trial`
func TestUnitTimeoutLetsReview2UseUnusedDiscoveryTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var r1Calls atomic.Int32
		client := timedReviewClient(func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if req.Messages[0].ExtractText() == "R2" {
				if err := waitReview(ctx, 4*time.Minute); err != nil {
					return nil, err
				}
				return confirmed(), nil
			}
			if r1Calls.Add(1) == 1 {
				if err := waitReview(ctx, 3*time.Minute); err != nil {
					return nil, err
				}
				return suspicion("first"), nil
			}
			return reviewText("done"), nil
		})
		a, history := timeoutRunner(t, client)
		defer history.Finalize()
		began := time.Now()
		findings, err := a.dispatchUnits(history.Context(context.Background()))
		if err != nil || len(findings) != 1 {
			t.Fatalf("findings=%v err=%v", findings, err)
		}
		if elapsed := time.Since(began); elapsed != 7*time.Minute {
			t.Fatalf("elapsed=%v", elapsed)
		}
		var trialSeq, debriefSeq float64
		for _, record := range timeoutTranscript(t, history) {
			if record["artifact_kind"] == "trial_decision" {
				trialSeq = record["seq"].(float64)
			}
			if record["type"] == "debrief" {
				debriefSeq = record["seq"].(float64)
				if record["outcome"] != "completed" {
					t.Fatalf("debrief=%v", record)
				}
			}
		}
		if trialSeq == 0 || debriefSeq <= trialSeq {
			t.Fatalf("Trial=%v debrief=%v", trialSeq, debriefSeq)
		}
	})
}

func TestExplorationDeadlineRetainsAssessmentAndMarksRemainingReviewIncomplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var r1Calls, r2Calls atomic.Int32
		client := timedReviewClient(func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if req.Messages[0].ExtractText() == "R2" {
				delay := 2 * time.Minute
				if r2Calls.Add(1) > 1 {
					delay = 20 * time.Minute
				}
				if err := waitReview(ctx, delay); err != nil {
					return nil, err
				}
				return confirmed(), nil
			}
			switch r1Calls.Add(1) {
			case 1:
				if err := waitReview(ctx, time.Minute); err != nil {
					return nil, err
				}
				return suspicion("first"), nil
			case 2:
				return suspicion("second"), nil
			default:
				return reviewText("done"), nil
			}
		})
		a, history := timeoutRunner(t, client)
		defer history.Finalize()
		began := time.Now()
		findings, err := a.dispatchUnits(history.Context(context.Background()))
		if err != nil || len(findings) != 1 {
			t.Fatalf("accepted Finding lost: %v %v", findings, err)
		}
		// R1 finished at one minute. R2 explores for 85% of the remaining
		// interval ending at 9m58s, so its request expires at 8m37.3s.
		if elapsed := time.Since(began); elapsed != 8*time.Minute+37300*time.Millisecond {
			t.Fatalf("wrong exploration deadline: %v", elapsed)
		}
		assessments, incomplete := 0, false
		for _, record := range timeoutTranscript(t, history) {
			if record["artifact_kind"] == "review_assessment" {
				assessments++
			}
			if record["type"] == "debrief" {
				incomplete = record["outcome"] == harness.OutcomeLLMError
			}
		}
		if !incomplete || assessments != 1 {
			t.Fatalf("incomplete=%v assessments=%d", incomplete, assessments)
		}
	})
}

func TestUnitTimeoutIncludesPlanAndBoundsWrapUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var reminders atomic.Int32
		client := timedReviewClient(func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			n := calls.Add(1)
			if n == 1 {
				if err := waitReview(ctx, 6*time.Minute); err != nil {
					return nil, err
				}
				return reviewText("plan"), nil
			}
			for _, message := range req.Messages {
				if strings.Contains(message.ExtractText(), "BUDGET NEARLY EXHAUSTED") {
					reminders.Add(1)
				}
			}
			if err := waitReview(ctx, 20*time.Minute); err != nil {
				return nil, err
			}
			return reviewText("done"), nil
		})
		a, history := timeoutRunner(t, client)
		defer history.Finalize()
		a.args.Template.PlanTask = &template.LlmConversation{Messages: []template.ChatMessage{{Role: "user", Content: "plan"}}}
		began := time.Now()
		_, err := a.dispatchUnits(history.Context(context.Background()))
		if err == nil || calls.Load() != 2 || reminders.Load() != 1 {
			t.Fatalf("calls=%d wrapups=%d err=%v", calls.Load(), reminders.Load(), err)
		}
		if elapsed := time.Since(began); elapsed < 6*time.Minute || elapsed >= 7*time.Minute {
			t.Fatalf("R1 deadline not enforced: %v", elapsed)
		}
	})
}

func TestReview1SlotReleasedWhileReview2StillRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var r1Calls atomic.Int32
		r2Started := make(chan struct{})
		secondUnit := make(chan struct{})
		client := timedReviewClient(func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if req.Messages[0].ExtractText() == "R2" {
				close(r2Started)
				<-secondUnit
				return confirmed(), nil
			}
			switch r1Calls.Add(1) {
			case 1:
				return suspicion("first"), nil
			case 2:
				<-r2Started
				return reviewText("done"), nil
			default:
				close(secondUnit)
				return reviewText("done"), nil
			}
		})
		a, history := timeoutRunner(t, client)
		defer history.Finalize()
		a.changes = append(a.changes, goDiff("b.go", 1))
		findings, err := a.dispatchUnits(history.Context(context.Background()))
		if err != nil || len(findings) != 1 {
			t.Fatalf("%v %v", findings, err)
		}
	})
}

func TestAsyncHypothesisResolutionStaysInsideUnitLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var r1Calls atomic.Int32
		client := timedReviewClient(func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if req.Messages[0].ExtractText() == "R2" {
				if err := waitReview(ctx, 4*time.Minute); err != nil {
					return nil, err
				}
				return confirmed(), nil
			}
			if r1Calls.Add(1) == 1 {
				return suspicion("async"), nil
			}
			return reviewText("done"), nil
		})
		a, history := timeoutRunner(t, client)
		defer history.Finalize()
		pool := harness.NewWorkerPool(1)
		occupied := make(chan struct{})
		pool.Submit(func() error { close(occupied); time.Sleep(time.Minute); return nil })
		<-occupied
		a.args.WorkerPool = pool
		a.hypothesisHook.WorkerPool = pool
		began := time.Now()
		findings, err := a.dispatchUnits(history.Context(context.Background()))
		if err != nil || len(findings) != 1 {
			t.Fatalf("late candidate lost: %v %v", findings, err)
		}
		if elapsed := time.Since(began); elapsed != 5*time.Minute {
			t.Fatalf("elapsed=%v", elapsed)
		}
		for _, record := range timeoutTranscript(t, history) {
			if record["type"] == "debrief" && record["outcome"] != "completed" {
				t.Fatalf("debrief=%v", record)
			}
		}
	})
}

func TestWaitingUnitStartsItsOwnClockAfterDiscoverySlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := timedReviewClient(func(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			deadline, ok := ctx.Deadline()
			// Each newly admitted Unit receives its own 5m55.81s exploration
			// interval (70% of 9m58s, then 85% exploration).
			if !ok || time.Until(deadline) != 355810*time.Millisecond {
				t.Errorf("Unit did not receive a fresh exploration deadline: %s", time.Until(deadline))
			}
			if calls.Add(1) == 1 {
				if err := waitReview(ctx, 5*time.Minute); err != nil {
					return nil, err
				}
			}
			return reviewText("done"), nil
		})
		a, history := timeoutRunner(t, client)
		defer history.Finalize()
		a.changes = append(a.changes, goDiff("b.go", 1))
		if _, err := a.dispatchUnits(history.Context(context.Background())); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 2 {
			t.Fatalf("calls=%d", calls.Load())
		}
	})
}

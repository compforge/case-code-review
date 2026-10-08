package runner

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type explorationPlanClient struct {
	t        *testing.T
	requests []llm.ChatRequest
}

func (c *explorationPlanClient) CompletionsWithCtx(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	if _, ok := ctx.Deadline(); ok {
		c.t.Error("exploration deadline must not cancel model calls or wrap-up")
	}
	c.requests = append(c.requests, request)
	time.Sleep(6 * time.Minute)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content := "No material leads remain."
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: &content}, FinishReason: "stop"}}, Usage: &llm.UsageInfo{PromptTokens: 10, CompletionTokens: 5}}, nil
}

func TestExplorationTimeIncludesPlanAndLeavesWrapUpAlive(t *testing.T) {
	repo := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		history := session.New(repo, "main", "test", session.SessionOptions{})
		defer history.Finalize()
		client := &explorationPlanClient{t: t}
		conversation := template.LlmConversation{Messages: []template.ChatMessage{{Role: "user", Content: "review {{diff}}"}}}
		a := New(Args{RepoDir: repo, Session: history, LLMClient: client, MaxConcurrency: 1, ConcurrentTaskTimeout: 5, Template: template.Template{MainTask: conversation, PlanTask: &conversation, MaxTokens: 10000, MaxToolRequestTimes: 5}})
		a.changes = []change.Change{goDiff("p.go", 1)}
		if _, err := a.dispatchUnits(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(client.requests) != 2 {
			t.Fatalf("requests=%d, want plan and wrap-up", len(client.requests))
		}
		var text strings.Builder
		for _, m := range client.requests[1].Messages {
			text.WriteString(m.ExtractText())
		}
		if !strings.Contains(text.String(), "BUDGET NEARLY EXHAUSTED") {
			t.Fatalf("plan time not charged to exploration: %s", text.String())
		}
		if history.LLMFailures() != 0 {
			t.Fatal("exploration expiry recorded as a model failure")
		}
	})
}

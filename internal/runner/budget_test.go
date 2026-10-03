package runner

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type budgetClient struct{ calls int }

func (c *budgetClient) CompletionsWithCtx(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	c.calls++
	content := "No further issues."
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: &content}, FinishReason: "stop"}}, Usage: &llm.UsageInfo{PromptTokens: 100}}, nil
}

func TestReviewBudgetSharesPlanAndExecutionAndSkipsQueuedUnits(t *testing.T) {
	for _, plan := range []bool{false, true} {
		name := "main"
		if plan {
			name = "plan"
		}
		t.Run(name, func(t *testing.T) {
			client := &budgetClient{}
			repo := t.TempDir()
			history := session.New(repo, "main", "test", session.SessionOptions{})

			conversation := template.LlmConversation{Messages: []template.ChatMessage{{Role: "user", Content: "review {{diff}}"}}}
			tpl := template.Template{MainTask: conversation, MaxTokens: 10000, MaxToolRequestTimes: 5}
			if plan {
				tpl.PlanTask = &conversation
			}
			a := New(Args{RepoDir: repo, Template: tpl, LLMClient: client, Session: history, MaxConcurrency: 1, MaxTokensBudget: 100})
			a.changes = []change.Change{goDiff("p.go", 3)}
			if _, err := a.dispatchUnits(t.Context()); err != nil {
				t.Fatal(err)
			}
			if client.calls != 1 {
				t.Fatalf("model calls = %d, want 1", client.calls)
			}
			history.Finalize()
			path, err := history.TranscriptPath()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			transcript := string(data)
			if got := strings.Count(transcript, `"outcome":"skipped_policy"`); got != 2 {
				t.Fatalf("skipped units = %d; transcript=%s", got, transcript)
			}
			if plan && !strings.Contains(transcript, `"outcome":"truncated"`) {
				t.Fatalf("plan exhausted budget without truncating main: %s", transcript)
			}
			if got := strings.Count(transcript, `"artifact_kind":"token_budget"`); got != 1 {
				t.Fatalf("budget artifacts = %d", got)
			}
			if history.LLMFailures() != 0 {
				t.Fatalf("local budget counted as provider failure")
			}
		})
	}
}

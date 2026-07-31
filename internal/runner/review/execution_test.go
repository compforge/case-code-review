package review

import (
	"context"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestAssessmentToolRunsThroughHarnessWithoutRegistryProvider(t *testing.T) {
	hypothesis := Hypothesis{Path: "a.go", Content: "issue", ExistingCode: "x"}
	hypothesis.ID = IDFor(hypothesis)
	client := &assessmentScriptedClient{responses: []*llm.ChatResponse{
		reviewToolResponse("call-1", SubmitAssessments.Name(), `{"assessments":[{
			"hypothesis_id":"`+hypothesis.ID+`",
			"support":"supported",
			"attribution":"caused",
			"value":"actionable",
			"novelty":"new",
			"reason":"the checked caller reaches this path",
			"evidence":["caller.go:12"]
		}]}`),
		reviewToolResponse("call-2", "task_done", `{}`),
	}}
	collector := NewAssessmentCollector()
	result, err := harness.Execute(context.Background(), harness.ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review the case")},
		ToolDefs: []llm.ToolDef{
			AssessmentToolDef(),
			{
				Type: "function",
				Function: llm.FunctionDef{
					Name: "task_done",
					Parameters: map[string]any{
						"type": "object", "properties": map[string]any{},
					},
				},
			},
		},
		ToolHandler: &AssessmentHook{Collector: collector},
		Session:     &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		Scope:       session.Scope{ID: "hypothesis_review:change_set", Kind: "run"},
		MaxTurns:    2,
		MaxTokens:   1_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != harness.OutcomeCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	assessments := collector.Assessments()
	if len(assessments) != 1 || assessments[0].HypothesisID != hypothesis.ID {
		t.Fatalf("unexpected assessments: %+v", assessments)
	}
}

type assessmentScriptedClient struct {
	responses []*llm.ChatResponse
}

func (c *assessmentScriptedClient) CompletionsWithCtx(
	_ context.Context,
	_ llm.ChatRequest,
) (*llm.ChatResponse, error) {
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func reviewToolResponse(id, name, arguments string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Choices: []llm.Choice{{
			Message: llm.ResponseMessage{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: id, Type: "function",
					Function: llm.FunctionCall{Name: name, Arguments: arguments},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
}

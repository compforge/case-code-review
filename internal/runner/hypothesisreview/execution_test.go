package hypothesisreview

import (
	"context"
	"errors"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
)

func TestAssessmentToolRunsThroughHarnessWithoutRegistryProvider(t *testing.T) {
	hypothesis := unitreview.Hypothesis{Path: "a.go", Content: "issue", ExistingCode: "x"}
	hypothesis.ID = unitreview.IDFor(hypothesis)
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
	execution, err := harness.NewExecution(harness.ExecutionSpec{
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
		Scope:       session.Scope{ID: "hypothesis_review:l-1", Kind: "lane"},
		MaxTurns:    2,
		MaxTokens:   1_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := execution.Run(context.Background())
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

func TestReviewRequiresCurrentHypothesisAssessment(t *testing.T) {
	hypothesis := completeHypothesis("h-1", "a.go")
	client := &assessmentScriptedClient{responses: []*llm.ChatResponse{
		reviewToolResponse("call-1", "task_done", `{}`),
		reviewAssessmentResponse("call-2", hypothesis.ID),
		reviewToolResponse("call-3", "task_done", `{}`),
	}}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}} {{change_set}} {{clues}} {{prior_assessments}}"},
		}},
		LLMClient: client,
		ToolDefs:  []llm.ToolDef{{Type: "function", Function: llm.FunctionDef{Name: "task_done"}}},
		Session:   &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		MaxTurns:  3,
		MaxTokens: 2_000,
	}, ReviewInput{LaneID: "l-1", Hypothesis: hypothesis}, nil)
	if len(result.Assessments) != 1 || result.Assessments[0].HypothesisID != hypothesis.ID {
		t.Fatalf("assessments = %+v, want current hypothesis", result.Assessments)
	}
	if len(client.responses) != 0 {
		t.Fatalf("Review stopped before completion guard was satisfied")
	}
}

func TestReviewReturnsAcceptedAssessmentsAfterLLMFailure(t *testing.T) {
	hypothesis := completeHypothesis("h-1", "a.go")
	client := &failingAssessmentClient{first: reviewAssessmentResponse("call-1", hypothesis.ID)}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"}, {Role: "user", Content: "{{hypothesis}}"},
		}},
		LLMClient: client,
		ToolDefs:  []llm.ToolDef{{Type: "function", Function: llm.FunctionDef{Name: "task_done"}}},
		Session:   &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		MaxTurns:  2,
		MaxTokens: 2_000,
	}, ReviewInput{LaneID: "l-1", Hypothesis: hypothesis}, nil)
	if len(result.Assessments) != 1 || result.Assessments[0].HypothesisID != hypothesis.ID {
		t.Fatalf("accepted partial assessment was lost: %+v", result.Assessments)
	}
}

func completeHypothesis(id, path string) unitreview.Hypothesis {
	return unitreview.Hypothesis{
		ID: id, Path: path, Content: "issue", ExistingCode: "x", Trigger: "call",
		Impact: "failure", ChangeAttribution: "changed", Evidence: []string{path + ":1"},
	}
}

func reviewAssessmentResponse(callID, hypothesisID string) *llm.ChatResponse {
	return reviewToolResponse(callID, SubmitAssessments.Name(), `{"assessments":[{
		"hypothesis_id":"`+hypothesisID+`","support":"insufficient","attribution":"unknown",
		"value":"unknown","novelty":"new","reason":"missing decisive evidence","evidence":[]
	}]}`)
}

type assessmentScriptedClient struct {
	responses []*llm.ChatResponse
}

type failingAssessmentClient struct {
	first *llm.ChatResponse
	calls int
}

func (c *failingAssessmentClient) CompletionsWithCtx(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	c.calls++
	if c.calls == 1 {
		return c.first, nil
	}
	return nil, errors.New("provider unavailable")
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

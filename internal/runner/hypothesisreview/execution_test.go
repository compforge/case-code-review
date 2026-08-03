package hypothesisreview

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestAssessmentToolRunsThroughHarnessWithoutRegistryProvider(t *testing.T) {
	hypothesis := unitreview.Hypothesis{Path: "a.go", Content: "issue", ExistingCode: "x"}
	hypothesis.ID = unitreview.IDFor(hypothesis)
	client := &assessmentScriptedClient{responses: []*llm.ChatResponse{
		reviewToolResponse("call-1", SubmitAssessment.Name(), `{
			"support":"supported",
			"attribution":"caused",
			"value":"actionable",
			"novelty":"new",
			"reason":"the checked caller reaches this path",
			"evidence":["caller.go:12"]
		}`),
	}}
	collector := NewAssessmentCollector(hypothesis.ID)
	execution, err := harness.NewExecution(harness.ExecutionSpec{
		LLMClient:      client,
		Messages:       []msg.Msg{msg.Text("user", "review the case")},
		ToolDefs:       []llm.ToolDef{AssessmentToolDef()},
		ToolHandler:    &AssessmentHook{Collector: collector},
		CompletionTool: SubmitAssessment.Name(),
		Session:        &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		Scope:          session.Scope{ID: "hypothesis_review:l-1", Kind: "lane"},
		MaxTurns:       2,
		MaxTokens:      1_000,
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

func TestReviewBindsAssessmentToCurrentHypothesis(t *testing.T) {
	hypothesis := completeHypothesis("h-1", "a.go")
	client := &assessmentScriptedClient{responses: []*llm.ChatResponse{
		reviewAssessmentResponse("call-1"),
	}}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}} {{change_set}} {{clues}} {{prior_assessments}}"},
		}},
		LLMClient: client,
		Session:   &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		MaxTurns:  1,
		MaxTokens: 2_000,
	}, ReviewInput{LaneID: "l-1", Hypothesis: hypothesis}, nil)
	if len(result.Assessments) != 1 || result.Assessments[0].HypothesisID != hypothesis.ID {
		t.Fatalf("assessments = %+v, want current hypothesis", result.Assessments)
	}
	if len(client.responses) != 0 {
		t.Fatalf("Review did not stop after accepting the bound assessment")
	}
}

func TestReviewCanAssessFromRetainedUnitSnapshotsWithoutReadingAgain(t *testing.T) {
	hypothesis := completeHypothesis("h-1", "a.go")
	reviewUnit := unit.UnitOf(unit.Fragment{Path: "a.go", Diff: "+fixed"})
	reviewUnit.AddFileSnapshot(unit.FileSnapshot{
		Kind: unit.CurrentSnapshot, Path: "a.go", Start: 1, End: 1, Total: 1,
		Content: "File: a.go (Total lines: 1)\n1|fixed",
	})
	client := &assessmentScriptedClient{responses: []*llm.ChatResponse{
		reviewToolResponse("call-1", SubmitAssessment.Name(), `{
			"support":"supported","attribution":"caused",
			"value":"actionable","novelty":"new","reason":"Unit evidence proves it",
			"evidence":["a.go:1"]
		}`),
	}}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}} {{change_set}} {{clues}} {{evidence_paths}}"},
		}},
		LLMClient: client,
		Session:   &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		MaxTurns:  1,
		MaxTokens: 2_000,
	}, ReviewInput{LaneID: "l-1", Unit: reviewUnit, Hypothesis: hypothesis}, nil)

	if len(result.Assessments) != 1 || len(result.Assessments[0].EvidenceReceipts) == 0 {
		t.Fatalf("assessment did not inherit Unit evidence: %+v", result.Assessments)
	}
	foundSnapshot := false
	if len(client.requests) == 1 {
		for _, message := range client.requests[0].Messages {
			foundSnapshot = foundSnapshot || strings.Contains(message.ExtractText(), "fixed")
		}
	}
	if !foundSnapshot {
		t.Fatalf("Unit snapshots were not supplied to Review 2: %+v", client.requests)
	}
}

func TestReviewReadFilesReportsRetainedUnitCoverage(t *testing.T) {
	hypothesis := completeHypothesis("h-1", "a.go")
	reviewUnit := unit.UnitOf(unit.Fragment{Path: "a.go", Diff: "+fixed"})
	reviewUnit.AddFileSnapshot(unit.FileSnapshot{
		Kind: unit.CurrentSnapshot, Path: "a.go", Start: 1, End: 1, Total: 1,
		Content: "File: a.go (Total lines: 1)\n1|fixed",
	})
	provider := &countingReviewReadProvider{}
	registry := tool.NewRegistry()
	registry.Register(provider)
	client := &assessmentScriptedClient{responses: []*llm.ChatResponse{
		reviewToolResponse("read-1", tool.FileRead.Name(), `{"reads":[{"file_path":"a.go"}]}`),
		reviewAssessmentResponse("submit-1"),
	}}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}} {{change_set}} {{clues}}"},
		}},
		LLMClient: client,
		ToolDefs: []llm.ToolDef{{
			Type: "function", Function: llm.FunctionDef{Name: tool.FileRead.Name()},
		}},
		Tools: registry, Session: &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		MaxTurns: 2, MaxTokens: 2_000, FileDedup: true,
	}, ReviewInput{LaneID: "l-1", Unit: reviewUnit, Hypothesis: hypothesis}, nil)

	if len(result.Assessments) != 1 || provider.calls != 0 {
		t.Fatalf("assessment=%+v provider calls=%d, want retained coverage without provider read", result.Assessments, provider.calls)
	}
	covered := false
	if len(client.requests) >= 2 {
		for _, message := range client.requests[1].Messages {
			covered = covered || strings.Contains(message.ExtractText(), "retained Unit context in this Lane")
		}
	}
	if !covered {
		t.Fatalf("covered read did not explain retained Unit/Lane context: %+v", client.requests)
	}
}

func TestReviewTimeoutPersistsSystemInsufficientAssessment(t *testing.T) {
	hypothesis := completeHypothesis("h-1", "a.go")
	var submissions []AssessmentSubmission
	var warnings []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	result := Review(ctx, Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}}"},
		}},
		LLMClient: &blockingAssessmentClient{},
		Session:   &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
		MaxTurns:  2,
		MaxTokens: 2_000,
		OnAssessment: func(submission AssessmentSubmission) {
			submissions = append(submissions, submission)
		},
		RecordWarning: func(warningType, _ string, _ string) {
			warnings = append(warnings, warningType)
		},
	}, ReviewInput{LaneID: "l-1", Hypothesis: hypothesis}, nil)

	if result.Execution.State != harness.OutcomeTimeout {
		t.Fatalf("execution = %+v, want timeout", result.Execution)
	}
	if len(result.Assessments) != 1 {
		t.Fatalf("assessments = %+v, want one system fallback", result.Assessments)
	}
	assessment := result.Assessments[0]
	if assessment.HypothesisID != hypothesis.ID || assessment.Support != Insufficient ||
		assessment.Attribution != AttributionUnknown || assessment.Value != ValueUnknown ||
		assessment.Novelty != Novel || assessment.ReviewerAlias != "system" {
		t.Fatalf("fallback assessment = %+v", assessment)
	}
	if len(submissions) != 1 || submissions[0].Assessment.HypothesisID != hypothesis.ID {
		t.Fatalf("persisted submissions = %+v", submissions)
	}
	for _, warning := range warnings {
		if warning == "hypothesis_unassessed" {
			t.Fatalf("timeout fallback was still reported unassessed: %v", warnings)
		}
	}
}

func TestRenderReviewPromptDoesNotRepeatRetainedLaneHistory(t *testing.T) {
	input := ReviewInput{
		PriorAssessments: []Assessment{{HypothesisID: "h-prior", Reason: "prior decision"}},
		PriorEvidence:    []EvidenceReceipt{{ToolCallID: "receipt-prior", Ref: "prior.go"}},
	}
	retained := renderReviewPrompt("{{prior_assessments}}", Config{}, input, false, true)
	if strings.Contains(retained, "h-prior") || strings.Contains(retained, "receipt-prior") {
		t.Fatalf("retained Lane history was repeated: %q", retained)
	}
	if !strings.Contains(retained, "retained conversation") {
		t.Fatalf("retained Lane guidance missing: %q", retained)
	}

	fallback := renderReviewPrompt("{{prior_assessments}}", Config{}, input, false, false)
	if !strings.Contains(fallback, "h-prior") {
		t.Fatalf("prior assessment missing without continuation: %q", fallback)
	}
	if strings.Contains(fallback, "receipt-prior") {
		t.Fatalf("Runner-only evidence receipt leaked into prompt: %q", fallback)
	}
}

func completeHypothesis(id, path string) unitreview.Hypothesis {
	return unitreview.Hypothesis{
		ID: id, Path: path, Content: "issue", ExistingCode: "x", Trigger: "call",
		Impact: "failure", ChangeAttribution: "changed", Evidence: []string{path + ":1"},
	}
}

func reviewAssessmentResponse(callID string) *llm.ChatResponse {
	return reviewToolResponse(callID, SubmitAssessment.Name(), `{
		"support":"insufficient","attribution":"unknown",
		"value":"unknown","novelty":"new","reason":"missing decisive evidence","evidence":[]
	}`)
}

type assessmentScriptedClient struct {
	responses []*llm.ChatResponse
	requests  []llm.ChatRequest
}

type blockingAssessmentClient struct{}

func (*blockingAssessmentClient) CompletionsWithCtx(
	ctx context.Context,
	_ llm.ChatRequest,
) (*llm.ChatResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *assessmentScriptedClient) CompletionsWithCtx(
	_ context.Context,
	request llm.ChatRequest,
) (*llm.ChatResponse, error) {
	c.requests = append(c.requests, request)
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

type countingReviewReadProvider struct{ calls int }

func (p *countingReviewReadProvider) Tool() tool.Tool { return tool.FileRead }
func (p *countingReviewReadProvider) Execute(context.Context, map[string]any) (string, error) {
	p.calls++
	return "unexpected provider read", nil
}

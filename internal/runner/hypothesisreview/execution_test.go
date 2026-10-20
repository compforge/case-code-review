package hypothesisreview

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestAssessmentToolRunsThroughHarnessWithoutRegistryProvider(t *testing.T) {
	hypothesis := unitreview.Hypothesis{Path: "a.go", Content: "issue", ExistingCode: "x"}
	hypothesis.ID = unitreview.IDFor(hypothesis)
	client := &assessmentScriptedClient{responses: []*agentgo.LLMResponse{
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
		ChatModel:      client,
		Messages:       []agentgo.AgentMessage{msg.Text("user", "review the case")},
		ToolDefs:       []llm.ToolDef{AssessmentToolDef()},
		ToolHandler:    &AssessmentHook{Collector: collector},
		CompletionTool: SubmitAssessment.Name(),
		Session:        &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
		Scope:          recording.Scope{ID: "hypothesis_review:l-1", Kind: "lane"},
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
	client := &assessmentScriptedClient{responses: []*agentgo.LLMResponse{
		reviewAssessmentResponse("call-1"),
	}}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}} {{change_set}} {{clues}} {{prior_assessments}}"},
		}},
		ChatModel: client,
		Session:   &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
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
	client := &assessmentScriptedClient{responses: []*agentgo.LLMResponse{
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
		ChatModel: client,
		Session:   &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
		MaxTurns:  1,
		MaxTokens: 2_000,
	}, ReviewInput{LaneID: "l-1", Unit: reviewUnit, Hypothesis: hypothesis}, nil)

	if len(result.Assessments) != 1 || len(result.Assessments[0].EvidenceReceipts) == 0 {
		t.Fatalf("assessment did not inherit Unit evidence: %+v", result.Assessments)
	}
	foundSnapshot := false
	if len(client.requests) == 1 {
		for _, message := range client.requests[0].Messages {
			foundSnapshot = foundSnapshot || strings.Contains(message.TextContent(), "fixed")
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
	client := &assessmentScriptedClient{responses: []*agentgo.LLMResponse{
		reviewToolResponse("read-1", tool.FileRead.Name(), `{"reads":[{"file_path":"a.go"}]}`),
		reviewAssessmentResponse("submit-1"),
	}}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}} {{change_set}} {{clues}}"},
		}},
		ChatModel: client,
		ToolDefs: []llm.ToolDef{{
			Type: "function", Function: llm.FunctionDef{Name: tool.FileRead.Name()},
		}},
		Tools: registry, Session: &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
		MaxTurns: 2, MaxTokens: 2_000, FileDedup: true,
	}, ReviewInput{LaneID: "l-1", Unit: reviewUnit, Hypothesis: hypothesis}, nil)

	if len(result.Assessments) != 1 || provider.calls != 0 {
		t.Fatalf("assessment=%+v provider calls=%d, want retained coverage without provider read", result.Assessments, provider.calls)
	}
	covered := false
	if len(client.requests) >= 2 {
		for _, message := range client.requests[1].Messages {
			covered = covered || strings.Contains(message.TextContent(), "retained Unit context in this Lane")
		}
	}
	if !covered {
		t.Fatalf("covered read did not explain retained Unit/Lane context: %+v", client.requests)
	}
}

func TestReviewExternalBoundaryForcesInsufficientAssessment(t *testing.T) {
	hypothesis := completeHypothesis("h-external", "adapter.go")
	hypothesis.Trigger = "external provider emits state x"
	client := &assessmentScriptedClient{responses: []*agentgo.LLMResponse{
		reviewToolResponse("external-1", WebSearch.Name(), `{"query":"provider state x contract"}`),
		reviewToolResponse("submit-supported", SubmitAssessment.Name(), `{
			"support":"supported","attribution":"caused","value":"actionable",
			"novelty":"new","reason":"the external state triggers the issue",
			"evidence":["adapter.go:10"]
		}`),
		reviewAssessmentResponse("submit-insufficient"),
	}}
	result := Review(context.Background(), Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "verify"},
			{Role: "user", Content: "{{hypothesis}}"},
		}},
		ChatModel: client,
		Session:   &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
		MaxTurns:  3,
		MaxTokens: 2_000,
	}, ReviewInput{LaneID: "l-1", Hypothesis: hypothesis}, nil)

	if len(result.Assessments) != 1 || result.Assessments[0].Support != Insufficient {
		t.Fatalf("assessments = %+v, want one insufficient decision", result.Assessments)
	}
	if !hasReceipt(result.Assessments[0].EvidenceReceipts, ExternalEvidenceUnverifiedReceipt, hypothesis.ID) {
		t.Fatalf("external boundary receipt missing: %+v", result.Assessments[0].EvidenceReceipts)
	}
	if len(client.responses) != 0 {
		t.Fatalf("Review did not continue through the rejected supported submission")
	}
}

func TestReviewTimeoutRemainsUnassessed(t *testing.T) {
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
		ChatModel: &blockingAssessmentClient{},
		Session:   &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
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

	if len(result.Assessments) != 0 || len(submissions) != 0 {
		t.Fatalf("timeout fabricated judgment: %+v %+v", result.Assessments, submissions)
	}
	if slices.Contains(warnings, "hypothesis_unassessed") {
		t.Fatalf("reviewer duplicated pipeline-owned unassessed warning: %v", warnings)
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

func reviewAssessmentResponse(callID string) *agentgo.LLMResponse {
	return reviewToolResponse(callID, SubmitAssessment.Name(), `{
		"support":"insufficient","attribution":"unknown",
		"value":"unknown","novelty":"new","reason":"missing decisive evidence","evidence":[]
	}`)
}

type assessmentScriptedClient struct {
	responses []*agentgo.LLMResponse
	requests  []agentgo.LLMRequest
}

type blockingAssessmentClient struct{}

func (c *blockingAssessmentClient) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *assessmentScriptedClient) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	config := agentgo.ResolveCallConfig(opts)
	request := agentgo.LLMRequest{Messages: messages,
		Tools: tools, Config: &config}

	c.requests = append(c.requests, request)
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func reviewToolResponse(id, name, arguments string) *agentgo.LLMResponse {
	return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: append([]agentgo.ContentBlock{}, agentgo.ToolCallBlock(agentgo.ToolCall{ID: id,
		Name: name,
		Args: json.RawMessage(arguments)})), StopReason: agentgo.StopReasonToolUse}}
}

type countingReviewReadProvider struct{ calls int }

func (p *countingReviewReadProvider) Tool() tool.Tool { return tool.FileRead }
func (p *countingReviewReadProvider) Execute(context.Context, map[string]any) (string, error) {
	p.calls++
	return "unexpected provider read", nil
}

func hasReceipt(receipts []EvidenceReceipt, kind, ref string) bool {
	for _, receipt := range receipts {
		if receipt.Kind == kind && receipt.Ref == ref {
			return true
		}
	}
	return false
}

// +case:id=lane_hypothesis_isolation,desc=`two hypotheses append to one LoopSession`,expect=`the second sees the first conversation but gets a fresh Assessment collector, completion state and turn budget`
func TestReviewAppendsHypothesisWithFreshExecutionState(t *testing.T) {
	session := &harness.LoopSession{}
	defer session.Close()
	client := &assessmentScriptedClient{responses: []*agentgo.LLMResponse{
		reviewAssessmentResponse("call-1"), reviewAssessmentResponse("call-1"),
	}}
	config := Config{
		Task: template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "stable verification rules"},
			{Role: "user", Content: "{{hypothesis}} {{prior_assessments}}"},
		}},
		ChatModel: client, MaxTurns: 1, MaxTokens: 10000,
	}
	for _, id := range []string{"hyp-first", "hyp-second"} {
		result := Review(t.Context(), config, ReviewInput{LaneID: "lane", Hypothesis: completeHypothesis(id, "a.go")}, session)
		if result.Execution.State != harness.OutcomeCompleted || result.Execution.Turns != 1 || len(result.Assessments) != 1 || result.Assessments[0].HypothesisID != id {
			t.Fatalf("%s result=%+v assessments=%+v", id, result.Execution, result.Assessments)
		}
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	var content strings.Builder
	for _, message := range client.requests[1].Messages {
		content.WriteString(message.TextContent())
	}
	text := content.String()
	if strings.Count(text, "stable verification rules") != 1 || !strings.Contains(text, "hyp-first") || !strings.Contains(text, "hyp-second") || !strings.Contains(text, `"accepted":"hyp-first"`) {
		t.Fatalf("second request did not append to the first conversation: %s", text)
	}
}

func (c *blockingAssessmentClient) SupportsTools() bool { return true }
func (c *blockingAssessmentClient) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return llm.CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return c.Generate(ctx, messages, tools, opts...) })
}

func (c *assessmentScriptedClient) SupportsTools() bool { return true }
func (c *assessmentScriptedClient) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return llm.CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return c.Generate(ctx, messages, tools, opts...) })
}

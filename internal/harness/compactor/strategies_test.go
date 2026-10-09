package compactor

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

func TestHistoryStrategiesDoNotRetainTheirOwnTail(t *testing.T) {
	tests := []struct {
		name  string
		stage agentcontext.Compactor
		input []agentgo.AgentMessage
	}{
		{"tool", &ToolResultCompactor{}, round("last-history-result")},
		{"text", NewLightTrimCompactor(LightTrimConfig{}), []agentgo.AgentMessage{agentgo.UserMsg(strings.Repeat("历史证据", 2000))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := agentgo.ToMessages(tt.input)
			out, err := tt.stage.Compact(t.Context(), agentgo.TransformContext{Messages: tt.input}, 0)
			if err != nil {
				t.Fatal(err)
			}
			last := len(out) - 1
			if agentcontext.EstimateTokens(out[last]) >= agentcontext.EstimateTokens(tt.input[last]) {
				t.Fatal("last historical message was protected again")
			}
			if !reflect.DeepEqual(out[last].Raw(), tt.input[last].Raw()) || !reflect.DeepEqual(original, agentgo.ToMessages(tt.input)) {
				t.Fatal("projection lost raw evidence or mutated input")
			}
			if !utf8.ValidString(out[last].TextContent()) {
				t.Fatal("projection split a rune")
			}
			if err := validateTools(out); err != nil {
				t.Fatal(err)
			}
			again, err := tt.stage.Compact(t.Context(), agentgo.TransformContext{Messages: out}, 0)
			if err != nil || !reflect.DeepEqual(agentgo.ToMessages(out), agentgo.ToMessages(again)) {
				t.Fatalf("repeated projection changed: %v", err)
			}
		})
	}
}

type captureSummaryModel struct {
	summaryModel
	requests   [][]agentgo.Message
	executions []agentgo.Execution
	response   string
}

func (m *captureSummaryModel) Generate(ctx context.Context, messages []agentgo.Message, _ []agentgo.ToolSpec, _ ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	m.requests = append(m.requests, messages)
	execution, _ := agentgo.ExecutionFromContext(ctx)
	m.executions = append(m.executions, execution)
	return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.TextBlock(m.response)}}}, nil
}

func TestHistorySummaryRetainsEvidenceAndUsesIncrementalCheckpoint(t *testing.T) {
	model := &captureSummaryModel{response: "<analysis>private reasoning</analysis><summary>confirmed checkpoint</summary>"}
	c := NewSummaryCompactor(SummaryConfig{Model: model})
	ctx := agentgo.ContextWithExecution(t.Context(), agentgo.Execution{ID: "compact-1", TurnIndex: 3})
	raw := agentgo.UserMsg("original evidence marker " + strings.Repeat("source ", 200))
	projected := newProjectedMessage(raw, agentgo.UserMsg("reference only"))
	out, err := c.Compact(ctx, agentgo.TransformContext{Messages: []agentgo.AgentMessage{projected}}, .5)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := out[0].(agentcontext.ContextSummary)
	if len(out) != 1 || checkpoint.Kept != 0 || checkpoint.Summary != "confirmed checkpoint" || !reflect.DeepEqual(checkpoint.RawMessages, []agentgo.AgentMessage{raw}) {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	if !strings.Contains(model.requests[0][1].TextContent(), "original evidence marker") {
		t.Fatal("summary consumed the cleared projection instead of source evidence")
	}
	newFact := agentgo.UserMsg("new confirmed fact")
	out, err = c.Compact(ctx, agentgo.TransformContext{Messages: append(out, newFact)}, .5)
	if err != nil {
		t.Fatal(err)
	}
	prompt := model.requests[1][1].TextContent()
	if strings.Contains(prompt, "original evidence marker") || strings.Count(prompt, "confirmed checkpoint") != 1 || !strings.Contains(prompt, "new confirmed fact") {
		t.Fatalf("incremental prompt replayed or duplicated old evidence: %s", prompt)
	}
	if len(out[0].(agentcontext.ContextSummary).RawMessages) != 2 {
		t.Fatal("incremental summary lost provenance")
	}
	for _, execution := range model.executions {
		if execution.ParentID != "compact-1" || execution.TurnIndex != 3 || execution.Attempt != 1 {
			t.Fatalf("summary escaped parent execution: %+v", execution)
		}
	}
	if model.executions[0].ID == model.executions[1].ID {
		t.Fatal("distinct summary calls share an execution ID")
	}
}

func TestHistorySummaryRejectsEmptyResponse(t *testing.T) {
	c := NewSummaryCompactor(SummaryConfig{Model: &captureSummaryModel{}})
	out, err := c.Compact(t.Context(), agentgo.TransformContext{Messages: []agentgo.AgentMessage{agentgo.UserMsg("evidence")}}, .5)
	if err == nil || out != nil {
		t.Fatalf("empty response was accepted: %v", err)
	}
}

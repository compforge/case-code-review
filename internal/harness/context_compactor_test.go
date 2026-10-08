package harness

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/compactor"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestContextBudgetFailureStopsBeforeModel(t *testing.T) {
	manager := newContextManager(ExecutionSpec{ContextWindow: 100, FileEvictEnabled: true}, &chatModel{client: &scriptedClient{}})
	input := []agentgo.AgentMessage{msg.FixedText("user", strings.Repeat("required task constraints ", 200))}
	projection, err := manager.Compact(t.Context(), agentgo.TransformContext{Messages: input}, agentgo.CompactReasonThreshold)
	if !errors.Is(err, compactor.ErrBudget) || projection.Changed {
		t.Fatalf("budget failure = %v, committed=%v", err, projection.Changed)
	}
}

func TestContextZoneReportUsesTimeline(t *testing.T) {
	tm, err := timeline.New("zone-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := timeline.NewContext(t.Context(), tm)
	manager := newContextManager(ExecutionSpec{ContextWindow: 400, FileEvictEnabled: true}, &chatModel{client: &scriptedClient{}})
	_, err = manager.Compact(ctx, agentgo.TransformContext{Messages: []agentgo.AgentMessage{msg.FixedText("user", "review the file"), msg.NewFile("example.go", 1, 200, 200, strings.Repeat("1|source line\n", 200))}}, agentgo.CompactReasonThreshold)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tm.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"context.zones", "fixed", "active", "history", "tokens_target", "tokens_after", "strategies", "target_met"} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("missing %s in timeline", key)
		}
	}
}

// TestContextCompactionReplay is an opt-in deterministic replay of a captured
// request with its latest raw tool result restored. The fixture stays in ignored
// eval/data: it is a local transform comparison, not a reconstructed full run.
func TestContextCompactionReplay(t *testing.T) {
	path := os.Getenv("CCR_CONTEXT_REPLAY")
	if path == "" {
		t.Skip("set CCR_CONTEXT_REPLAY to a local captured-input fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Messages []llm.Message     `json:"messages"`
		Result   msg.LLMToolResult `json:"result"`
		Ratio    float64           `json:"ratio"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	input := msg.Wrap(fixture.Messages)
	latest := -1
	for i, message := range input {
		if wire, ok := message.(agentgo.Message); ok && i == 1 && wire.Role == agentgo.RoleUser {
			input[i] = msg.Instruction{Message: wire}
		}
		if fixture.Messages[i].ToolCallID == fixture.Result.ToolCallID {
			input[i], latest = msg.FromLLM(fixture.Result), i
		}
	}
	if latest < 0 {
		t.Fatal("raw tool result not found in captured request")
	}
	input, _ = normalizeContextMessages(input)
	before := agentcontext.EstimateTotal(input)
	legacy, err := agentcontext.NewMessageCompactor().Compact(t.Context(), input, fixture.Ratio)
	if err != nil {
		t.Fatal(err)
	}
	policy := newContextCompactor(ExecutionSpec{FileEvictEnabled: true}, &chatModel{client: &scriptedClient{}}, before, before/5)
	view, err := policy.Compact(t.Context(), input, fixture.Ratio)
	if err != nil {
		t.Fatal(err)
	}
	if len(view) != len(input) {
		t.Fatal("unexpected summary in deterministic message-compaction replay")
	}
	rawChars := len([]rune(input[latest].TextContent()))
	legacyChars := len([]rune(legacy[latest].TextContent()))
	newChars := len([]rune(view[latest].TextContent()))
	if legacyChars >= rawChars/2 {
		t.Fatal("fixture did not reproduce aggressive fresh-result trimming")
	}
	if newChars != rawChars {
		t.Fatal("fresh evidence was trimmed by zone policy")
	}
	if agentcontext.EstimateTotal(view) > int(float64(before)*fixture.Ratio) {
		t.Fatal("replay exceeded target budget")
	}
	t.Logf("estimated_tokens before=%d legacy=%d zone=%d; fresh_result_chars raw=%d legacy=%d zone=%d", before, agentcontext.EstimateTotal(legacy), agentcontext.EstimateTotal(view), rawChars, legacyChars, newChars)
}

func TestExecutionReportsContextBudgetAsTruncated(t *testing.T) {
	client := &scriptedClient{}
	result, err := runExecution(t.Context(), ExecutionSpec{
		LLMClient: client, ContextWindow: 100, MaxTurns: 2,
		Messages: []agentgo.AgentMessage{msg.FixedText("user", strings.Repeat("required constraints ", 200))},
		ToolDefs: []llm.ToolDef{toolDef("task_done")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeTruncated || !strings.Contains(result.Reason, "context compaction") || len(client.Requests()) != 0 {
		t.Fatalf("state=%s reason=%s model_calls=%d", result.State, result.Reason, len(client.Requests()))
	}
}

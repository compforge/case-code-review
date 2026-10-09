package compactor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
)

type compactFunc func(context.Context, []agentgo.AgentMessage, float64) ([]agentgo.AgentMessage, error)

func (f compactFunc) Compact(ctx context.Context, input agentgo.TransformContext, expect float64) ([]agentgo.AgentMessage, error) {
	messages := input.Messages
	return f(ctx, messages, expect)
}

func file(name string) *msg.File {
	return msg.NewFile(name, 1, 100, 100, strings.Repeat("1|meaningful source evidence\n", 100))
}

func round(ids ...string) []agentgo.AgentMessage {
	call := agentgo.Message{Role: agentgo.RoleAssistant}
	var results []agentgo.AgentMessage
	for _, id := range ids {
		call.Content = append(call.Content, agentgo.ToolCallBlock(agentgo.ToolCall{ID: id, Name: "read_files", Args: []byte(`{"file_path":"example.go"}`)}))
		result := agentgo.ToolResultMsg(id, []byte(strings.Repeat("new evidence ", 300)), false)
		results = append(results, result)
	}
	return append([]agentgo.AgentMessage{call}, results...)
}

func TestZoneCompactorProtectsFreshParallelResultsAcrossAllStages(t *testing.T) {
	input := []agentgo.AgentMessage{agentgo.SystemMsg("review instructions"), msg.FixedText("user", "review target and constraints"), file("old.go")}
	fresh := round("read-a", "read-b")
	input = append(input, fresh...)
	beforeWire := agentgo.ToMessages(input)
	var calls int
	stage := compactFunc(func(_ context.Context, history []agentgo.AgentMessage, expect float64) ([]agentgo.AgentMessage, error) {
		calls++
		if len(history) != 1 || history[0] != input[2] {
			t.Fatal("protected messages reached history compactor")
		}
		if expect <= 0 || expect >= 1 {
			t.Fatalf("history ratio = %f", expect)
		}
		if calls == 1 {
			return history, nil
		}
		next, _ := history[0].Compact(0)
		return []agentgo.AgentMessage{next}, nil
	})
	c := ZoneCompactor{KeepRecentTokens: 1, Stages: []Stage{{"first", stage}, {"fallback", stage}}}
	target := agentcontext.EstimateTotal(input) - agentcontext.EstimateTokens(input[2])/2
	view, err := c.Compact(t.Context(), agentgo.TransformContext{Messages: input}, float64(target)/float64(agentcontext.EstimateTotal(input)))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("stages called = %d", calls)
	}
	if !reflect.DeepEqual(agentgo.ToMessages(view[3:]), agentgo.ToMessages(fresh)) {
		t.Fatal("fresh tool round changed")
	}
	if view[2].TextContent() == input[2].TextContent() || view[2].Raw().TextContent() != input[2].TextContent() {
		t.Fatal("old file compaction lost raw evidence")
	}
	if !reflect.DeepEqual(agentgo.ToMessages(input), beforeWire) {
		t.Fatal("input mutated")
	}
}

func TestPartitionProtectsWholeRoundsAndKeepsAnchorsInPlace(t *testing.T) {
	first, latest := round("old"), round("new-a", "new-b")
	anchor := msg.FixedText("user", "next hypothesis")
	input := []agentgo.AgentMessage{agentgo.SystemMsg("instructions"), file("preload.go")}
	input = append(input, first...)
	input = append(input, anchor, file("next.go"))
	input = append(input, latest...)
	input = append(input, agentgo.UserMsg("finish now"))
	parts := partition(input, 1)
	if !reflect.DeepEqual(agentgo.ToMessages(flatten(parts)), agentgo.ToMessages(input)) {
		t.Fatal("partition reordered messages")
	}
	if got := parts[len(parts)-1]; got.zone != active || len(got.messages) != len(latest)+1 {
		t.Fatalf("active segment = %+v", got)
	}
	if len(parts) != 5 || parts[2].zone != fixed {
		t.Fatalf("fixed anchor lost: %+v", parts)
	}
	if start := activeStart(input, agentcontext.EstimateTotal(input)); start != 2 {
		t.Fatalf("expanded active starts at %d", start)
	}
	if start := activeStart(input[:2], 1000); start != 2 {
		t.Fatal("initial sources were protected as a tool round")
	}
}

func TestZoneCompactorHistoricalPriorityAndAge(t *testing.T) {
	high, old, newer := file("target.go").ConfigurePriority(20), file("old.go"), file("newer.go")
	input := []agentgo.AgentMessage{msg.FixedText("user", "task"), high, old, newer}
	c := ZoneCompactor{Stages: []Stage{{"message", &MessageCompactor{}}}}
	view, err := c.Compact(t.Context(), agentgo.TransformContext{Messages: input}, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	if view[1].TextContent() != high.TextContent() || view[3].TextContent() != newer.TextContent() {
		t.Fatal("compressed a newer or higher-priority message first")
	}
	if view[2].TextContent() == old.TextContent() {
		t.Fatal("old low-priority evidence was not compressed")
	}
}

func TestZoneCompactorNoOpAndFailureAreTransactional(t *testing.T) {
	failure := errors.New("summary unavailable")
	for _, tc := range []struct {
		name  string
		ratio float64
		stage compactFunc
		want  error
	}{
		{"fits", 1, func(context.Context, []agentgo.AgentMessage, float64) ([]agentgo.AgentMessage, error) {
			t.Fatal("no-op invoked history")
			return nil, nil
		}, nil},
		{"summary failure", .5, func(context.Context, []agentgo.AgentMessage, float64) ([]agentgo.AgentMessage, error) {
			return nil, failure
		}, failure},
		{"insufficient reduction", .5, func(_ context.Context, m []agentgo.AgentMessage, _ float64) ([]agentgo.AgentMessage, error) {
			return m, nil
		}, ErrBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []agentgo.AgentMessage{agentgo.SystemMsg("instruction"), file("source.go")}
			original := agentgo.ToMessages(input)
			c := ZoneCompactor{Stages: []Stage{{"summary", tc.stage}}}
			out, err := c.Compact(t.Context(), agentgo.TransformContext{Messages: input}, tc.ratio)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v", err)
			}
			if err != nil && out != nil {
				t.Fatal("failed rewrite returned a commit candidate")
			}
			if !reflect.DeepEqual(agentgo.ToMessages(input), original) {
				t.Fatal("failed rewrite mutated input")
			}
		})
	}
}

func TestZoneCompactorOversizedActiveDoesNotEraseFreshEvidence(t *testing.T) {
	input := append([]agentgo.AgentMessage{file("old.go")}, round("huge")...)
	called := false
	c := ZoneCompactor{LimitTokens: 10, Stages: []Stage{{"history", compactFunc(func(context.Context, []agentgo.AgentMessage, float64) ([]agentgo.AgentMessage, error) {
		called = true
		return nil, nil
	})}}}
	for _, ratio := range []float64{0, .01} {
		out, err := c.Compact(t.Context(), agentgo.TransformContext{Messages: input}, ratio)
		if !errors.Is(err, ErrBudget) || out != nil || called {
			t.Fatalf("oversized active result=%v error=%v called=%v", out, err, called)
		}
	}
}

type summaryModel struct {
	calls int
	fail  bool
}

func (m *summaryModel) Generate(_ context.Context, messages []agentgo.Message, _ []agentgo.ToolSpec, _ ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	m.calls++
	if m.fail {
		return nil, errors.New("summary failed")
	}
	return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.TextBlock("Confirmed evidence: older source checked; continue the current investigation.")}}}, nil
}
func (*summaryModel) GenerateStream(context.Context, []agentgo.Message, []agentgo.ToolSpec, ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return nil, errors.New("unused")
}
func (*summaryModel) SupportsTools() bool { return false }

func TestZoneCompactorRepeatedSummaryPreservesActiveAndRaw(t *testing.T) {
	model := &summaryModel{}
	c := ZoneCompactor{KeepRecentTokens: 1, Stages: []Stage{{"summary", NewSummaryCompactor(SummaryConfig{Model: model})}}}
	input := []agentgo.AgentMessage{msg.FixedText("user", "the review task"), file("old.go")}
	input = append(input, round("first")...)
	for _, next := range []string{"second", "third"} {
		beforeActive := agentgo.ToMessages(input[len(input)-2:])
		view, err := c.Compact(t.Context(), agentgo.TransformContext{Messages: input}, .8)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(agentgo.ToMessages(view[len(view)-2:]), beforeActive) {
			t.Fatal("summary changed fresh evidence")
		}
		summary, ok := view[1].(agentcontext.ContextSummary)
		if !ok || len(summary.RawMessages) == 0 {
			t.Fatal("history summary lost raw evidence")
		}
		input = append(view, round(next)...)
	}
	if model.calls != 2 {
		t.Fatalf("summary calls = %d", model.calls)
	}
}

func TestZoneCompactorRejectsOrphanedToolResults(t *testing.T) {
	c := ZoneCompactor{}
	_, err := c.Compact(t.Context(), agentgo.TransformContext{Messages: []agentgo.AgentMessage{agentgo.ToolResultMsg("missing", []byte("evidence"), false)}}, .5)
	if err == nil {
		t.Fatal("accepted orphan tool result")
	}
}

func TestPartitionMovesSupersededTaskIntoHistory(t *testing.T) {
	old := msg.FixedText("user", "completed hypothesis")
	current := msg.FixedText("user", "current hypothesis")
	input := []agentgo.AgentMessage{agentgo.SystemMsg("rules"), old, file("old.go"), current, file("current.go")}
	input = append(input, round("fresh")...)
	parts := partition(input, 1)
	if parts[1].zone != history || !reflect.DeepEqual(parts[1].messages[0], old) {
		t.Fatal("completed task stayed pinned")
	}
	if parts[2].zone != fixed || !reflect.DeepEqual(parts[2].messages[0], current) {
		t.Fatal("current task was not pinned")
	}
}

func TestPrimaryDiffProtectionFollowsTaskLifetime(t *testing.T) {
	oldDiff := msg.NewDiff([]string{"old.go"}, "old changes").ConfigureReviewSource(nil)
	newDiff := msg.NewDiff([]string{"new.go"}, "new changes").ConfigureReviewSource(nil)
	input := []agentgo.AgentMessage{msg.FixedText("user", "old task"), oldDiff, msg.FixedText("user", "new task"), newDiff, file("context.go")}
	parts := partition(input, 1)
	for _, part := range parts {
		for _, m := range part.messages {
			if m == oldDiff && part.zone != history {
				t.Fatal("completed diff remained protected")
			}
			if m == newDiff && part.zone != fixed {
				t.Fatal("primary diff entered history")
			}
		}
	}
}

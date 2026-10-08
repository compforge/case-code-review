package msg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/compforge/agentgo/codec"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func withSourceManager(t *testing.T, messages []agentgo.AgentMessage, check func(agentgo.ArtifactManager)) {
	t.Helper()
	stop := errors.New("initialization-only test")
	agent := agentgo.NewAgent(agentgo.WithBeforeRun(func(_ context.Context, run agentgo.BeforeRunContext) error {
		if err := RegisterArtifacts(run.Artifacts, messages); err != nil {
			return err
		}
		check(run.Artifacts)
		return stop
	}))
	if err := agent.Prompt(t.Context(), "test"); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestSourceArtifactsPreserveIdentityAndCodec(t *testing.T) {
	a := sourceFile("a.go", 1, 3)
	same := *a
	same.Content = "navigation label\n" + same.Content
	base := *a
	base.Snapshot = SnapshotBaseline
	base.Ref = "revision"
	changed := *a
	changed.Content = strings.ReplaceAll(changed.Content, "line 1", "changed")
	values := SourceArtifacts([]agentgo.AgentMessage{a, &same, &base, &changed})
	if len(values) != 3 {
		t.Fatalf("identity conflated or duplicated source: %d", len(values))
	}
	c, err := agentgo.NewCodec(codec.Type[SourceArtifact]("ccr.source.v1"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := c.Marshal(agentgo.AgentState{Artifacts: values})
	if err != nil {
		t.Fatal(err)
	}
	var decoded agentgo.AgentState
	if err := c.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, decoded.Artifacts) {
		t.Fatal("codec changed source types/identity/content")
	}
	clipped := sourceFile("clip.go", 1, 1)
	clipped.Content += "[Output truncated: line shortened]\n"
	if len(SourceArtifacts([]agentgo.AgentMessage{clipped})) != 0 {
		t.Fatal("clipped line became exact source")
	}
}

func TestRegisteredSourceProjectionUsesCurrentCoverage(t *testing.T) {
	a, b := sourceFile("a.go", 1, 20), sourceFile("a.go", 10, 30)
	input := []agentgo.AgentMessage{a, b}
	withSourceManager(t, input, func(manager agentgo.ArtifactManager) {
		project := func(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
			return TransformSource(agentgo.TransformContext{Messages: messages, Artifacts: manager})
		}
		view := project(input)
		standalone := TransformSource(agentgo.TransformContext{Messages: input})
		for i := range view {
			if view[i].TextContent() != standalone[i].TextContent() {
				t.Fatal("inventory changed existing projection policy")
			}
		}
		if !strings.Contains(view[1].TextContent(), "Lines 10-20 already shown") {
			t.Fatal("overlap not deduplicated")
		}
		reduced, _ := a.Compact(0)
		rebuilt := project([]agentgo.AgentMessage{reduced, view[1]})
		if !strings.Contains(rebuilt[1].TextContent(), "10|line 10\n") {
			t.Fatal("retained inventory claimed invisible coverage")
		}
		if view[1].Raw().TextContent() != b.TextContent() {
			t.Fatal("raw changed")
		}
		for _, value := range manager.ListArtifacts() {
			manager.DeleteArtifact(value.ID())
		}
		missing := project(input)
		if missing[1].TextContent() != b.TextContent() {
			t.Fatal("missing material hid evidence")
		}
	})
}

func TestSourceRegistrationHandlesSearchAndConcurrentReads(t *testing.T) {
	file := sourceFile("a.go", 1, 3)
	search := FromLLM(LLMToolResult{Tool: CodeSearchToolName, ToolCallID: "search", Arguments: map[string]any{"searches": []any{map[string]any{"query": "line"}}}, Content: tool.MergeCodeSearchResults([]string{"File: a.go\nMatch lines: 1\n1|line 1\nContext:\nLINE_RANGE: 1-3\n1|line 1\n2|line 2\n3|line 3"})})
	withSourceManager(t, nil, func(manager agentgo.ArtifactManager) {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if err := RegisterArtifacts(manager, []agentgo.AgentMessage{file, search}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		expected := SourceArtifacts([]agentgo.AgentMessage{file, search})
		if !reflect.DeepEqual(manager.ListArtifacts(), expected) {
			t.Fatal("parallel registration lost or duplicated material")
		}
		view := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{search, file}, Artifacts: manager})
		if strings.Contains(view[1].TextContent(), "2|line 2\n") {
			t.Fatal("search/read evidence was not shared")
		}
	})
}

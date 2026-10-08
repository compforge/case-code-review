package runner

import (
	"context"
	"errors"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/unit"
	"reflect"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/compforge/agentgo/codec"
)

func TestClueMessageProjectionRetainsProvenanceAndRebuildsCoverage(t *testing.T) {
	text := strings.Repeat("The caller must preserve cancellation and return the original failure. ", 20)
	first := newDocClueMessage("caller.go::Run", "", "caller docstring", text)
	second := newDocClueMessage("other.go::Run", "", "used type docstring", text)
	before := newDocClueMessage("caller.go::Run", "base", "before change", text)
	changed := newDocClueMessage("caller.go::Run", "", "changed docstring", text+"Different guarantee.")
	input := []agentgo.AgentMessage{first, second, before, changed}
	withClueManager(t, input, func(manager agentgo.ArtifactManager) {
		if len(manager.ListArtifacts()) != 2 {
			t.Fatalf("document identity: %d", len(manager.ListArtifacts()))
		}
		project := func(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
			return msg.TransformMaterials(agentgo.TransformContext{Messages: messages, Artifacts: manager})
		}
		view := project(input)
		if strings.Contains(view[1].TextContent(), text) || !strings.Contains(view[1].TextContent(), "other.go::Run") || !strings.Contains(view[1].TextContent(), "used type docstring") {
			t.Fatal("dedup lost provenance or repeated the body")
		}
		if strings.Contains(view[2].TextContent(), text) || !strings.Contains(view[2].TextContent(), "before base") || !strings.Contains(view[3].TextContent(), "Different guarantee") {
			t.Fatal("conflated snapshots or changed documents")
		}
		again := project(view)
		for i := range view {
			if view[i].TextContent() != again[i].TextContent() {
				t.Fatal("projection is not idempotent")
			}
		}
		if view[1].Raw().TextContent() != second.TextContent() {
			t.Fatal("raw doc changed")
		}
		compacted, ratio := first.Compact(0)
		if ratio >= 1 {
			t.Fatal("document cannot be compacted independently")
		}
		restored := project([]agentgo.AgentMessage{compacted, view[1]})
		if !strings.Contains(restored[1].TextContent(), text) {
			t.Fatal("compaction left a dangling document reference")
		}
		removed := project(view[1:2])
		if !strings.Contains(removed[0].TextContent(), text) {
			t.Fatal("removing first occurrence left a dangling reference")
		}
		manager.DeleteArtifact(first.MaterialArtifact().ID())
		missing := project(input)
		if !strings.Contains(missing[1].TextContent(), text) {
			t.Fatal("unregistered document lost content")
		}
	})
}

func TestClueMessageCodecAndShortProjection(t *testing.T) {
	original := ClueArtifact{ClueKind: unit.ClueDoc, Text: "brief"}
	c, err := agentgo.NewCodec(codec.Type[ClueArtifact]("ccr.clue.v1"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.Marshal(agentgo.AgentState{Artifacts: []agentgo.Artifact{original}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded agentgo.AgentState
	if err := c.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Artifacts[0], original) || decoded.Artifacts[0].ID() != original.ID() {
		t.Fatal("codec changed document identity")
	}
	a, b := newDocClueMessage("a", "", "", "ok"), newDocClueMessage("b", "", "", "ok")
	view := msg.TransformMaterials(agentgo.TransformContext{Messages: []agentgo.AgentMessage{a, b}})
	if len(view[1].TextContent()) > len(b.TextContent()) {
		t.Fatal("reference expanded a short document")
	}
}

func newDocClueMessage(ref, snapshot, label, text string) *ClueMessage {
	m := NewClueMessage(unit.Clue{Kind: unit.ClueDoc, Ref: ref, Snapshot: snapshot, Text: text})
	m.label = label
	return m
}

func withClueManager(t *testing.T, messages []agentgo.AgentMessage, check func(agentgo.ArtifactManager)) {
	t.Helper()
	stop := errors.New("initialization-only test")
	agent := agentgo.NewAgent(agentgo.WithBeforeRun(func(_ context.Context, run agentgo.BeforeRunContext) error {
		if err := msg.RegisterArtifacts(run.Artifacts, messages); err != nil {
			return err
		}
		check(run.Artifacts)
		return stop
	}))
	if err := agent.Prompt(t.Context(), "test"); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestClueMessageRetainsKindAndDomainPolicy(t *testing.T) {
	body := strings.Repeat("Preserve this evidence and its constraints. ", 20)
	var input []agentgo.AgentMessage
	for _, kind := range []unit.ClueKind{unit.ClueDoc, unit.ClueSpec, unit.ClueRule, unit.ClueHistory, unit.ClueProject, unit.ClueLink} {
		clue := unit.Clue{Kind: kind, Relation: unit.RelCallee, Ref: "a.go::Run", Snapshot: "base", Text: body}
		message := NewClueMessage(clue)
		if message.Kind != kind || message.Clue != clue {
			t.Fatal("clue facts lost")
		}
		raw, ok := message.Raw().(*ClueMessage)
		if !ok || raw.Clue != clue {
			t.Fatal("raw lost clue type or facts")
		}
		compacted, _ := message.Compact(0)
		if kind != unit.ClueDoc && compacted.TextContent() != message.TextContent() {
			t.Fatal("doc compression applied to another clue kind")
		}
		input = append(input, message)
	}
	withClueManager(t, input, func(manager agentgo.ArtifactManager) {
		if len(manager.ListArtifacts()) != len(input) {
			t.Fatal("artifact identity conflated different clue kinds")
		}
		view := msg.TransformMaterials(agentgo.TransformContext{Messages: input, Artifacts: manager})
		for _, m := range view {
			if !strings.Contains(m.TextContent(), body) {
				t.Fatal("different clue kinds were deduplicated")
			}
		}
	})
}

package hypothesisreview

import (
	"slices"
	"time"

	"github.com/compforge/agentgo"

	"github.com/compforge/go-stdx/slicesx"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// ReviewInput is the Unit state needed to assess one Hypothesis. It is an API
// input, not a review-domain entity: the Lane owns grouping, retained context,
// prior evidence, and execution order.
type ReviewInput struct {
	LaneID     string
	Unit       unit.Unit
	Hypothesis unitreview.Hypothesis

	// ContextDelta distinguishes a Lane-provided incremental projection from a
	// direct caller, which projects the Unit's complete current state.
	ContextDelta  bool
	Fragments     []unit.Fragment
	FileSnapshots []unit.FileSnapshot
	RelatedDiffs  []unit.DiffSnapshot
	SearchResults []unit.SearchResult

	PriorEvidence    []EvidenceReceipt
	PriorAssessments []Assessment
}

func (i ReviewInput) turnFragments() []unit.Fragment {
	if i.ContextDelta {
		return i.Fragments
	}
	return i.Unit.Fragments
}

func (i ReviewInput) turnSnapshots() unit.ReviewSnapshot {
	if i.ContextDelta {
		return unit.ReviewSnapshot{
			FileSnapshots: i.FileSnapshots,
			RelatedDiffs:  i.RelatedDiffs,
			SearchResults: i.SearchResults,
		}
	}
	return i.Unit.Review()
}

func (i ReviewInput) Paths() []string {
	paths := append([]string(nil), i.Unit.Paths()...)
	snapshot := i.Unit.Review()
	for _, file := range snapshot.FileSnapshots {
		paths = append(paths, file.Path)
	}
	for _, diff := range snapshot.RelatedDiffs {
		paths = append(paths, diff.Paths...)
	}
	for _, result := range snapshot.SearchResults {
		paths = append(paths, result.Paths...)
	}
	if i.Hypothesis.Path != "" {
		paths = append(paths, i.Hypothesis.Path)
	}
	paths = slicesx.Uniq(paths)
	slices.Sort(paths)
	return paths
}

const (
	prioritySearch      = 10
	priorityFile        = 20
	priorityRelatedDiff = 30
	priorityTargetDiff  = 40
	priorityHypothesis  = 50
)

// reviewContextMessages projects immutable Unit state into independently
// compactable AgentMessages. Compaction changes only the execution view; the
// full snapshots remain on the Unit for later Review and Trial stages.
func reviewContextMessages(input ReviewInput) []agentgo.AgentMessage {
	snapshot := input.turnSnapshots()
	out := make([]agentgo.AgentMessage, 0, len(input.turnFragments())+len(snapshot.FileSnapshots)+len(snapshot.RelatedDiffs)+len(snapshot.SearchResults))
	for _, fragment := range input.turnFragments() {
		diff := unit.DiffSnapshot{
			Paths:   []string{fragment.Path},
			Content: "==== FILE: " + fragment.Path + " ====\n" + fragment.Diff,
		}
		diff.ID = unit.DiffSnapshotIDFor(diff)
		out = append(out, msg.NewDiff(diff.Paths, diff.Content).
			ConfigurePresentation("UNIT TARGET DIFF", priorityTargetDiff))
	}
	for _, file := range snapshot.FileSnapshots {
		kind := msg.SnapshotCurrent
		if file.Kind == unit.BaselineSnapshot {
			kind = msg.SnapshotBaseline
		} else if file.Kind == unit.DependencySnapshot {
			kind = msg.SnapshotDependency
		}
		out = append(out, (&msg.File{
			Path: file.Path, Start: file.Start, End: file.End, Total: file.Total,
			Content: file.Content, Snapshot: kind, Ref: file.Ref,
			Label: "retained Unit context in this Lane",
		}).ConfigurePriority(priorityFile))
	}
	for _, diff := range snapshot.RelatedDiffs {
		out = append(out, msg.NewDiff(diff.Paths, diff.Content).
			ConfigurePresentation("RELATED DIFF RETAINED BY UNIT REVIEW", priorityRelatedDiff))
	}
	for _, result := range snapshot.SearchResults {
		out = append(out, (&msg.SearchResult{
			Tool: msg.CodeSearchToolName, Query: result.Query, Paths: result.Paths, Content: result.Content,
		}).ConfigurePresentation("SEARCH RESULT RETAINED BY UNIT REVIEW", prioritySearch))
	}
	return out
}

func UnitReceipts(reviewUnit unit.Unit) []EvidenceReceipt {
	var out []EvidenceReceipt
	for _, fragment := range reviewUnit.Fragments {
		diff := unit.DiffSnapshot{Paths: []string{fragment.Path}, Content: fragment.Diff}
		out = append(out, EvidenceReceipt{
			ToolCallID: "unit:" + unit.DiffSnapshotIDFor(diff), Kind: "diff", Ref: fragment.Path,
		})
	}
	snapshot := reviewUnit.Review()
	for _, file := range snapshot.FileSnapshots {
		kind := "source"
		if file.Kind == unit.BaselineSnapshot {
			kind = "base"
		} else if file.Kind == unit.DependencySnapshot {
			kind = "dependency"
		}
		if file.Path != "" {
			ref := file.Path
			if file.Kind == unit.DependencySnapshot {
				ref = file.Ref
			}
			out = append(out, EvidenceReceipt{ToolCallID: "unit:" + file.ID, Kind: kind, Ref: ref})
		}
	}
	for _, diff := range snapshot.RelatedDiffs {
		for _, path := range slicesx.Uniq(diff.Paths) {
			if path != "" {
				out = append(out, EvidenceReceipt{ToolCallID: "unit:" + diff.ID, Kind: "diff", Ref: path})
			}
		}
	}
	for _, result := range snapshot.SearchResults {
		kind := "search"
		if result.Kind == unit.FileDiscovery {
			kind = "discovery"
		}
		if result.Query != "" {
			out = append(out, EvidenceReceipt{ToolCallID: "unit:" + result.ID, Kind: kind, Ref: result.Query})
		}
	}
	return out
}

// hypothesisMessage keeps Review 2's input typed until Harness projects it.
// Compaction may shorten supporting context, but never the claim being judged.
type hypothesisMessage struct {
	full      string
	condensed string
	compacted bool
	timestamp time.Time
}

func newHypothesisMessage(full, condensed string) hypothesisMessage {
	return hypothesisMessage{full: full, condensed: condensed, timestamp: time.Now()}
}

func (m hypothesisMessage) ToLLM() llm.Message {
	content := m.full
	if m.compacted && m.condensed != "" {
		content = m.condensed
	}
	return llm.NewTextMessage("user", content)
}

func (m hypothesisMessage) GetRole() agentgo.Role     { return agentgo.RoleUser }
func (m hypothesisMessage) GetTimestamp() time.Time   { return m.timestamp }
func (m hypothesisMessage) Raw() agentgo.AgentMessage { m.compacted = false; return m }
func (m hypothesisMessage) TextContent() string {
	wire := m.ToLLM()
	return wire.ExtractText()
}
func (m hypothesisMessage) ThinkingContent() string { return "" }
func (m hypothesisMessage) HasToolCalls() bool      { return false }
func (m hypothesisMessage) ToMessage() (agentgo.Message, bool) {
	return agentgo.Message{
		Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock(m.TextContent())},
		Timestamp: m.timestamp,
	}, true
}

func (m hypothesisMessage) Compact(expect float64) (agentgo.AgentMessage, float64) {
	rawTokens := llm.CountTokens(m.full)
	if rawTokens <= 0 {
		return m, 1
	}
	currentWire := m.ToLLM()
	currentRatio := float64(llm.CountTokens(currentWire.ExtractText())) / float64(rawTokens)
	if currentRatio <= expect || m.compacted || m.condensed == "" {
		return m, currentRatio
	}
	m.compacted = true
	compactedWire := m.ToLLM()
	return m, float64(llm.CountTokens(compactedWire.ExtractText())) / float64(rawTokens)
}

func (m hypothesisMessage) Priority() int { return priorityHypothesis }

// FixedContext preserves the claim and task constraints while allowing the
// message-owned representation to omit supporting context already held separately.
func (m hypothesisMessage) FixedContext() agentgo.AgentMessage {
	next, _ := m.Compact(0)
	return next
}

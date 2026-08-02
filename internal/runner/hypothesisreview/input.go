package hypothesisreview

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/go-stdx/slicesx"
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
func reviewContextMessages(input ReviewInput) []msg.Msg {
	snapshot := input.turnSnapshots()
	out := make([]msg.Msg, 0, len(input.turnFragments())+len(snapshot.FileSnapshots)+len(snapshot.RelatedDiffs)+len(snapshot.SearchResults))
	for _, fragment := range input.turnFragments() {
		diff := unit.DiffSnapshot{
			Paths:   []string{fragment.Path},
			Content: "==== FILE: " + fragment.Path + " ====\n" + fragment.Diff,
		}
		diff.ID = unit.DiffSnapshotIDFor(diff)
		out = append(out, diffSnapshotMessage{snapshot: diff, target: true})
	}
	for _, file := range snapshot.FileSnapshots {
		kind := msg.SnapshotCurrent
		if file.Kind == unit.BaselineSnapshot {
			kind = msg.SnapshotBaseline
		}
		out = append(out, (&msg.File{
			Path: file.Path, Start: file.Start, End: file.End, Total: file.Total,
			Content: file.Content, Snapshot: kind, Ref: file.Ref,
			Label: "retained Unit context in this Lane",
		}).ConfigurePriority(priorityFile))
	}
	for _, diff := range snapshot.RelatedDiffs {
		out = append(out, diffSnapshotMessage{snapshot: diff})
	}
	for _, result := range snapshot.SearchResults {
		out = append(out, searchResultMessage{result: result})
	}
	return out
}

type diffSnapshotMessage struct {
	snapshot unit.DiffSnapshot
	target   bool
}

func (m diffSnapshotMessage) ToLLM(level msg.CompactionLevel) llm.Message {
	content := m.snapshot.Content
	switch level {
	case msg.CompactionCondensed:
		var anchors []string
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, "==== FILE: ") || strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "@@ ") {
				anchors = append(anchors, line)
			}
		}
		if len(anchors) > 0 {
			content = "Diff anchors retained after compaction:\n" + strings.Join(anchors, "\n")
		}
	case msg.CompactionReference:
		paths := append([]string(nil), m.snapshot.Paths...)
		sort.Strings(paths)
		content = fmt.Sprintf("Diff for [%s] compacted to a reference; call read_diffs for exact hunks.", strings.Join(paths, ", "))
	}
	label := "RELATED DIFF RETAINED BY UNIT REVIEW"
	if m.target {
		label = "UNIT TARGET DIFF"
	}
	return llm.NewTextMessage("user", label+":\n"+content)
}

func (m diffSnapshotMessage) MaxCompaction() msg.CompactionLevel { return msg.CompactionReference }
func (m diffSnapshotMessage) Priority() int {
	if m.target {
		return priorityTargetDiff
	}
	return priorityRelatedDiff
}

type searchResultMessage struct{ result unit.SearchResult }

func (m searchResultMessage) ToLLM(level msg.CompactionLevel) llm.Message {
	content := m.result.Content
	switch level {
	case msg.CompactionCondensed:
		if len(m.result.Paths) > 0 {
			content = fmt.Sprintf("Search %q matched:\n- %s", m.result.Query, strings.Join(m.result.Paths, "\n- "))
		}
	case msg.CompactionReference:
		content = fmt.Sprintf("Search result for %q compacted to a reference; rerun the search for exact hits.", m.result.Query)
	}
	return llm.NewTextMessage("user", "SEARCH RESULT RETAINED BY UNIT REVIEW:\n"+content)
}

func (m searchResultMessage) MaxCompaction() msg.CompactionLevel { return msg.CompactionReference }
func (m searchResultMessage) Priority() int                      { return prioritySearch }

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
		}
		if file.Path != "" {
			out = append(out, EvidenceReceipt{ToolCallID: "unit:" + file.ID, Kind: kind, Ref: file.Path})
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
}

func newHypothesisMessage(full, condensed string) hypothesisMessage {
	return hypothesisMessage{full: full, condensed: condensed}
}

func (m hypothesisMessage) ToLLM(level msg.CompactionLevel) llm.Message {
	content := m.full
	if level >= msg.CompactionCondensed && m.condensed != "" {
		content = m.condensed
	}
	return llm.NewTextMessage("user", content)
}

func (m hypothesisMessage) MaxCompaction() msg.CompactionLevel {
	return msg.CompactionCondensed
}

func (m hypothesisMessage) Priority() int { return priorityHypothesis }

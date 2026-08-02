package hypothesisreview

import (
	"slices"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"github.com/qiankunli/go-stdx/slicesx"
)

// ReviewInput is the material needed to assess one Hypothesis. It is an API
// input, not a review-domain entity: the Lane owns grouping, retained context,
// prior evidence, and execution order.
type ReviewInput struct {
	LaneID           string
	Changes          []change.Change
	Hypothesis       unitreview.Hypothesis
	Clues            []unit.Clue
	EvidencePaths    []string
	PriorEvidence    []EvidenceReceipt
	PriorAssessments []Assessment
}

func (i ReviewInput) Paths() []string {
	paths := append([]string(nil), i.EvidencePaths...)
	if i.Hypothesis.Path != "" {
		paths = append(paths, i.Hypothesis.Path)
	}
	paths = slicesx.Uniq(paths)
	slices.Sort(paths)
	return paths
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

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

// Dossier is the immutable evidence packet transferred from one divergent
// Unit Review to a convergent Hypothesis Review. Lane assignment, rather than
// Dossier mutation, groups related work for context reuse.
type Dossier struct {
	ID               string
	LaneID           string
	Changes          []change.Change
	Hypotheses       []unitreview.Hypothesis
	Clues            []unit.Clue
	EvidencePaths    []string
	PriorEvidence    []EvidenceReceipt
	PriorDossierIDs  []string
	PriorAssessments []Assessment
}

func (c Dossier) Paths() []string {
	paths := append([]string(nil), c.EvidencePaths...)
	for _, hypothesis := range c.Hypotheses {
		if hypothesis.Path != "" {
			paths = append(paths, hypothesis.Path)
		}
	}
	paths = slicesx.Uniq(paths)
	slices.Sort(paths)
	return paths
}

// dossierMessage keeps Review 2's initial evidence packet typed until Harness
// projects it. Even under compaction every hypothesis remains complete because
// an omitted claim could never satisfy the Dossier completion contract.
type dossierMessage struct {
	full      string
	condensed string
}

func newDossierMessage(full, condensed string) dossierMessage {
	return dossierMessage{full: full, condensed: condensed}
}

func (m dossierMessage) ToLLM(level msg.CompactionLevel) llm.Message {
	content := m.full
	if level >= msg.CompactionCondensed && m.condensed != "" {
		content = m.condensed
	}
	return llm.NewTextMessage("user", content)
}

func (m dossierMessage) MaxCompaction() msg.CompactionLevel {
	return msg.CompactionCondensed
}

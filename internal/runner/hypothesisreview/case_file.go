package hypothesisreview

import (
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"github.com/qiankunli/go-stdx/slicesx"
)

// CaseFile is the material packet transferred from divergent Unit Reviews to
// one convergent Hypothesis Review. The first implementation deliberately puts
// the whole ChangeSet in one CaseFile; future partitioning must follow
// behavioral/evidence relationships rather than comment-anchor files.
type CaseFile struct {
	ID         string
	Changes    []change.Change
	Hypotheses []unitreview.Hypothesis
	Clues      []unit.Clue
}

func (c CaseFile) Paths() []string {
	paths := make([]string, 0, len(c.Hypotheses))
	for _, hypothesis := range c.Hypotheses {
		if hypothesis.Path != "" {
			paths = append(paths, hypothesis.Path)
		}
	}
	return slicesx.Uniq(paths)
}

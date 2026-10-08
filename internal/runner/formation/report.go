package formation

import (
	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/language"
)

type GroupingMerge struct {
	Namespace language.NamespaceRef `json:"namespace"`
	Targets   []string              `json:"targets"`
	Paths     []cg.Path             `json:"paths"`
}

type GroupingStep struct {
	Strategy         string          `json:"strategy"`
	InputUnits       int             `json:"input_units"`
	OutputUnits      int             `json:"output_units"`
	Merges           []GroupingMerge `json:"merges,omitempty"`
	BudgetBlocked    int             `json:"budget_blocked,omitempty"`
	MissingNamespace int             `json:"missing_namespace,omitempty"`
}

type GroupingReport struct {
	InitialUnits  int            `json:"initial_units"`
	MaxUnits      int            `json:"max_units"`
	FinalUnits    int            `json:"final_units"`
	LimitExceeded bool           `json:"limit_exceeded"`
	Steps         []GroupingStep `json:"steps"`
}

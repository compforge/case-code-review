package template

import "fmt"

// ReviewTimeBudget allocates a Unit's elapsed time, not cumulative worker time.
// Zero fractions select defaults so existing templates retain a bounded policy.
type ReviewTimeBudget struct {
	Review1Fraction     float64 `json:"review1_fraction,omitempty"`
	ExplorationFraction float64 `json:"exploration_fraction,omitempty"`
}

func (b ReviewTimeBudget) WithDefaults() ReviewTimeBudget {
	if b.Review1Fraction == 0 {
		b.Review1Fraction = .7
	}
	if b.ExplorationFraction == 0 {
		b.ExplorationFraction = .85
	}
	return b
}

func (b ReviewTimeBudget) Validate() error {
	b = b.WithDefaults()
	if !(b.Review1Fraction > 0 && b.Review1Fraction < 1) {
		return fmt.Errorf("REVIEW_TIME_BUDGET.review1_fraction must be between 0 and 1")
	}
	if !(b.ExplorationFraction > 0 && b.ExplorationFraction < 1) {
		return fmt.Errorf("REVIEW_TIME_BUDGET.exploration_fraction must be between 0 and 1")
	}
	return nil
}

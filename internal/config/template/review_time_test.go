package template

import "testing"

func TestReviewTimeBudgetDefaultsAndValidation(t *testing.T) {
	defaults := (ReviewTimeBudget{}).WithDefaults()
	if defaults.Review1Fraction != .7 || defaults.ExplorationFraction != .85 {
		t.Fatalf("defaults=%+v", defaults)
	}
	for _, value := range []ReviewTimeBudget{{Review1Fraction: -1}, {Review1Fraction: 1}, {ExplorationFraction: 1.1}} {
		if value.Validate() == nil {
			t.Fatalf("invalid policy accepted: %+v", value)
		}
	}
	if err := (ReviewTimeBudget{Review1Fraction: .6, ExplorationFraction: .8}).Validate(); err != nil {
		t.Fatal(err)
	}
}

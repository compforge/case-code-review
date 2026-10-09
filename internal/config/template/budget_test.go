package template

import "testing"

func TestIndependentCompletionBudgets(t *testing.T) {
	review, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	scan, err := LoadScanDefault()
	if err != nil {
		t.Fatal(err)
	}
	if review.MaxTokens != 200000 || review.CompletionTokenLimit() != 16384 {
		t.Fatalf("review budgets: %+v", review)
	}
	if scan.MaxTokens != 58888 || scan.CompletionTokenLimit() != 16384 {
		t.Fatalf("scan budgets: %+v", scan)
	}
	review.MaxTokens = 400000
	scan.MaxTokens = 200000
	if review.CompletionTokenLimit() != 16384 || scan.CompletionTokenLimit() != 16384 {
		t.Fatal("context override expanded output budget")
	}
	if (Template{MaxTokens: 4096}).CompletionTokenLimit() != 4096 || (ScanTemplate{MaxTokens: 4096}).CompletionTokenLimit() != 4096 {
		t.Fatal("legacy completion budget was not preserved")
	}
	review.MaxCompletionTokens = -1
	scan.MaxCompletionTokens = -1
	if review.Validate() == nil || scan.Validate() == nil {
		t.Fatal("negative completion budget accepted")
	}
}

package viewer

import "testing"

func TestToolTargetSummarizesBatchSearchQueries(t *testing.T) {
	got := toolTarget(`{"searches":[{"search_text":"Alpha"},{"search_text":"Beta"}]}`)
	if got != "Alpha, Beta" {
		t.Fatalf("toolTarget = %q, want Alpha, Beta", got)
	}
}

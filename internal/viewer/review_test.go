package viewer

import "testing"

func TestToolTargetSummarizesBatchSearchQueries(t *testing.T) {
	got := toolTarget(`{"searches":[{"query":"Alpha"},{"query":"Beta"}]}`)
	if got != "Alpha, Beta" {
		t.Fatalf("toolTarget = %q, want Alpha, Beta", got)
	}
}

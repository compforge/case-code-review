package viewer

import "testing"

func TestToolTargetSummarizesBatchSearchQueries(t *testing.T) {
	got := toolTarget(`{"searches":[{"query":"Alpha","syntax":"literal"},{"query":"Beta","syntax":"literal"}]}`)
	if got != "Alpha, Beta" {
		t.Fatalf("toolTarget = %q, want Alpha, Beta", got)
	}
}

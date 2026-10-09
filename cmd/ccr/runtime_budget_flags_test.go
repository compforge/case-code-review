package main

import "testing"

func TestReviewRuntimeBudgetFlags(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--max-files", "100000", "--max-snapshot-bytes", "268435456", "--max-completion-tokens", "8192"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.maxFiles != 100000 || opts.maxSnapshotBytes != 268435456 || opts.maxCompletionTokens != 8192 {
		t.Fatalf("options=%+v", opts)
	}
	for _, flag := range []string{"--max-files", "--max-snapshot-bytes", "--max-completion-tokens"} {
		if _, err := parseReviewFlags([]string{flag, "-1"}); err == nil {
			t.Fatalf("%s accepted negative budget", flag)
		}
	}
	scan, err := parseScanFlags([]string{"--max-completion-tokens", "8192"})
	if err != nil || scan.maxCompletionTokens != 8192 {
		t.Fatalf("scan budget=%d error=%v", scan.maxCompletionTokens, err)
	}
	if _, err := parseScanFlags([]string{"--max-completion-tokens", "-1"}); err == nil {
		t.Fatal("scan accepted negative output cap")
	}
}

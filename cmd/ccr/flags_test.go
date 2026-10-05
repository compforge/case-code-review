package main

import (
	"strings"
	"testing"
)

func TestParseReviewFlagsModelOverride(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--model", "claude-opus-4-6", "--biz-id", "github:org/repo#148"})
	if err != nil {
		t.Fatalf("parseReviewFlags: %v", err)
	}

	if opts.model != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", opts.model, "claude-opus-4-6")
	}
	if opts.outputFormat != "text" {
		t.Errorf("outputFormat = %q, want %q", opts.outputFormat, "text")
	}
	if opts.audience != "human" {
		t.Errorf("audience = %q, want %q", opts.audience, "human")
	}
	if opts.bizID != "github:org/repo#148" {
		t.Errorf("bizID = %q", opts.bizID)
	}
}

func TestParseReviewFlagsJSONL(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--format", "jsonl"})
	if err != nil {
		t.Fatalf("parseReviewFlags: %v", err)
	}
	if opts.outputFormat != "jsonl" {
		t.Fatalf("outputFormat = %q, want jsonl", opts.outputFormat)
	}
}

func TestParseReviewFlagsRejectsUnknownFormat(t *testing.T) {
	_, err := parseReviewFlags([]string{"--format", "xml"})
	if err == nil || !strings.Contains(err.Error(), "invalid --format") {
		t.Fatalf("error = %v, want invalid --format", err)
	}
}

func TestParseReviewFlagsRejectsJSONLPreview(t *testing.T) {
	_, err := parseReviewFlags([]string{"--format", "jsonl", "--preview"})
	if err == nil || !strings.Contains(err.Error(), "only supported for an executing review") {
		t.Fatalf("error = %v", err)
	}
}

func TestReviewTokenBudgetFlag(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--max-tokens-budget", "12345"})
	if err != nil || opts.maxTokensBudget != 12345 {
		t.Fatalf("budget=%d err=%v", opts.maxTokensBudget, err)
	}
	if _, err := parseReviewFlags([]string{"--max-tokens-budget", "-1"}); err == nil {
		t.Fatal("negative budget accepted")
	}
}

func TestReviewMaxUnitsFlag(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--max-units", "12"})
	if err != nil || opts.maxUnits != 12 {
		t.Fatalf("maxUnits=%d err=%v", opts.maxUnits, err)
	}
	if _, err := parseReviewFlags([]string{"--max-units", "-1"}); err == nil {
		t.Fatal("negative max units accepted")
	}
}

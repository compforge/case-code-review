package runner

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
)

func TestStripEmptyPlanBlock(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "english template wrapper is removed",
			input: "header\n### Review Plan (Optional)\n{{plan_guidance}}\n\ntail",
			want:  "header\ntail",
		},
		{
			name:  "english template wrapper without trailing blank line is removed",
			input: "header\n### Review Plan (Optional)\n{{plan_guidance}}\ntail",
			want:  "header\ntail",
		},
		{
			name:  "chinese template wrapper is removed",
			input: "header\n### 审查计划\n{{plan_guidance}}\n\ntail",
			want:  "header\ntail",
		},
		{
			name:  "chinese optional wrapper is removed",
			input: "header\n### 审查计划（可选）\n{{plan_guidance}}\n\ntail",
			want:  "header\ntail",
		},
		{
			name:  "no wrapper present is a no-op",
			input: "no plan block here\njust text",
			want:  "no plan block here\njust text",
		},
		{
			name:  "multiple wrappers all removed",
			input: "### Review Plan (Optional)\n{{plan_guidance}}\n\nmiddle\n### 审查计划\n{{plan_guidance}}\n\nend",
			want:  "middle\nend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripEmptyPlanBlock(tt.input)
			if got != tt.want {
				t.Errorf("stripEmptyPlanBlock() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "{{plan_guidance}}") {
				t.Errorf("stripEmptyPlanBlock() left literal {{plan_guidance}} in output: %q", got)
			}
		})
	}
}

func TestStripEmptyPlanBlock_IntegrationWithReplaceAll(t *testing.T) {
	template := "header\n### Review Plan (Optional)\n{{plan_guidance}}\n\ntail"

	stripped := stripEmptyPlanBlock(template)
	final := strings.ReplaceAll(stripped, "{{plan_guidance}}", "")

	want := "header\ntail"
	if final != want {
		t.Errorf("stripEmptyPlanBlock integration:\n  got:  %q\n  want: %q", final, want)
	}
	if strings.Contains(final, "{{plan_guidance}}") {
		t.Errorf("literal {{plan_guidance}} leaked: %q", final)
	}
	if strings.Contains(final, "Review Plan") {
		t.Errorf("dangling Review Plan header retained: %q", final)
	}
}

func TestReviewModeString(t *testing.T) {
	tests := []struct {
		from, to, commit string
		want             string
	}{
		{"", "", "abc123", session.ReviewModeCommit},
		{"main", "feature", "", session.ReviewModeRange},
		{"", "", "", session.ReviewModeWorkspace},
		{"main", "feature", "abc123", session.ReviewModeCommit},
	}

	for _, tt := range tests {
		got := reviewModeString(tt.from, tt.to, tt.commit)
		if got != tt.want {
			t.Errorf("reviewModeString(%q, %q, %q) = %q, want %q", tt.from, tt.to, tt.commit, got, tt.want)
		}
	}
}

func TestTagFingerprints(t *testing.T) {
	a := []finding.Finding{
		{Path: "a.go", StartLine: 3, Content: "nil deref"},
		{Path: "a.go", StartLine: 99, Content: "nil deref"}, // same finding, relocated
		{Path: "b.go", StartLine: 3, Content: "nil deref"},  // same words, different file
	}
	tagFingerprints(a)
	if len(a[0].Fingerprint) != 12 {
		t.Fatalf("fingerprint length = %d, want 12", len(a[0].Fingerprint))
	}
	// Identity is path+content: line drift must not change it, path must.
	if a[0].Fingerprint != a[1].Fingerprint {
		t.Errorf("line shift changed fingerprint: %s vs %s", a[0].Fingerprint, a[1].Fingerprint)
	}
	if a[0].Fingerprint == a[2].Fingerprint {
		t.Errorf("different path must change fingerprint")
	}
}

package viewer

import (
	"bytes"
	"strings"
	"testing"
)

func TestWorkspaceTemplatesRender(t *testing.T) {
	vs := &ViewSession{
		Summary:         SessionSummary{SessionID: "session-1", CWD: "/repo", BizID: "github:org/repo#148", HasDiffStats: true, DiffFileCount: 4, FileCount: 2},
		CompletionLabel: "Partial — not a clean verdict",
	}

	tests := []struct {
		name string
		data any
	}{
		{"reviews.html", reviewListData{Reviews: []reviewListEntry{{SessionSummary: vs.Summary, EncodedRepo: "repo", RepoName: "repo"}}}},
		{"review.html", sessionPageData{EncodedRepo: "repo", RepoName: "repo", Session: vs}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := parseTemplate(tt.name)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := tmpl.Execute(&out, tt.data); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "ZgotmplZ") {
				t.Fatal("template emitted an unsafe CSS or URL placeholder")
			}
			if tt.name == "reviews.html" && !strings.Contains(out.String(), "github:org/repo#148") {
				t.Fatal("journal list does not show the biz id")
			}
			if tt.name == "review.html" {
				if !strings.Contains(out.String(), "github:org/repo#148") || !strings.Contains(out.String(), "Business decisions") {
					t.Fatal("journal page lost review identity")
				}
				if strings.Contains(out.String(), "Review results and statistics") || strings.Contains(out.String(), "Review Overview") {
					t.Fatal("removed review summary remains")
				}
			}

		})
	}
}

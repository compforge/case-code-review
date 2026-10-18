package viewer

import (
	"bytes"
	"strings"
	"testing"
)

func TestTimelineTemplatesRender(t *testing.T) {
	vs := &ViewSession{
		Summary:         SessionSummary{SessionID: "session-1", CWD: "/repo", BizID: "github:org/repo#148", HasDiffStats: true, DiffFileCount: 4, FileCount: 2},
		CompletionLabel: "Partial — not a clean verdict",
	}

	tests := []struct {
		name string
		data any
	}{
		{"timelines.html", timelineListData{Timelines: []timelineListEntry{{SessionSummary: vs.Summary, EncodedRepo: "repo", RepoName: "repo"}}}},
		{"session.html", sessionPageData{EncodedRepo: "repo", RepoName: "repo", Session: vs}},
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
			if tt.name == "timelines.html" && !strings.Contains(out.String(), "github:org/repo#148") {
				t.Fatal("timeline list does not show the biz id")
			}
			if tt.name == "session.html" {
				if !strings.Contains(out.String(), "github:org/repo#148") || !strings.Contains(out.String(), "Review Timeline") {
					t.Fatal("timeline page lost review identity")
				}
				if strings.Contains(out.String(), "Review results and statistics") || strings.Contains(out.String(), "Review Overview") {
					t.Fatal("removed review summary remains")
				}
			}

		})
	}
}

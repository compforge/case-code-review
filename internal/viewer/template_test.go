package viewer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/recording"
)

func TestTimelineTemplatesRender(t *testing.T) {
	vs := &ViewSession{
		Cost: recording.CostReport{Total: recording.Cost{UnknownUsage: 1}, Stages: []recording.StageCost{{Stage: timeline.Stage{ID: "request", Name: "llm.request", Status: timeline.Running}, DurationMS: 1000, SelfMS: 1000, Cost: recording.Cost{Pending: 1, UnknownUsage: 1}}}},
		Summary: SessionSummary{
			SessionID: "session-1", CWD: "/repo", BizID: "github:org/repo#148",
			HasDiffStats: true, DiffFileCount: 4, FileCount: 2,
		},
		Diagnostics: SessionDiagnostics{
			Status: "warning", StatusLabel: "Partial", Summary: "one hypothesis is unassessed",
			DiffFiles: 4, ReviewFiles: 2, Review1Units: 1, Review1Executions: 1, Review1Done: 1,
			Hypotheses: 2, Assessments: 1, Unassessed: 1,
			Alerts: []DiagnosticAlert{{Level: "warning", Title: "1 Hypothesis lacks Assessment", Detail: "inspect Review 2"}},
		},
		Compaction: CompactionSummary{
			Count: 1, Summarized: 1,
		},
		SystemPrompts: []SystemPrompt{
			{TaskTypes: []TaskType{MainTask}, Text: "investigate"},
		},
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

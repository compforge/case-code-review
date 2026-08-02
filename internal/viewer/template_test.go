package viewer

import (
	"bytes"
	"strings"
	"testing"
)

func TestSessionAndReviewTemplatesRender(t *testing.T) {
	turn := &TaskCard{
		TaskType:         MainTask,
		RequestNo:        1,
		TurnNo:           1,
		PromptDelta:      120,
		MessageDelta:     2,
		PromptTokens:     120,
		CompletionTokens: 12,
		Request: []DisplayMessage{
			{Role: "system", Text: "investigate"},
			{Role: "user", Text: "review"},
		},
		ResponseContent: "I will inspect the file.",
		Reasoning:       "The changed path needs context.",
		StopReason:      "tool_calls",
	}
	review := &ReviewScope{
		ID:          "internal/viewer/store.go",
		Kind:        "unit",
		Scope:       "file",
		EncodedRepo: "repo",
		SessionID:   "session-1",
		Stage:       Review1Stage,
		Paths:       []string{"internal/viewer/store.go"},
		FilePath:    "internal/viewer/store.go",
		Tasks:       map[TaskType][]*TaskCard{MainTask: {turn}},
		Calls:       []*TaskCard{turn},
		Executions: []*ReviewExecution{{
			ID: "execution-1", TaskType: MainTask, Calls: []*TaskCard{turn}, Turns: []*TaskCard{turn},
			Conversation: []ConversationNode{
				{ID: "execution-1-1", Kind: "prompt", Label: "Prompt Snapshot · Turn 1", Preview: "2 messages", Messages: turn.Request, TurnNo: 1, PromptTokens: 120},
				{ID: "execution-1-2", Kind: "assistant", Label: "Assistant · Turn 1", Preview: "inspect", Text: "I will inspect the file.", Reasoning: "The changed path needs context.", TurnNo: 1},
			},
			Metrics: ReviewMetrics{LLMCalls: 1, TurnCount: 1, PromptTokens: 120},
			Status:  "completed", Outcome: "completed", DurationMs: 100,
		}},
		Metrics: ReviewMetrics{
			LLMCalls:        1,
			TurnCount:       1,
			PromptTokens:    120,
			MaxPromptTokens: 120,
		},
		HasSourcePreloads: true,
		HasContextPaths:   true,
		FileReads:         FileReadMetrics{Calls: 4, Requests: 4, UniqueFiles: 3, CoveredRequests: 1, SamePathRepeats: 1, PreloadedFiles: 1, UnitKnownFiles: 2, CallGraphFiles: 1},
		Status:            "completed",
		ExecutionDone:     1,
		Artifacts:         []ReviewArtifact{{Kind: "review_hypothesis", HypothesisID: "h-1", Data: `{"id":"h-1"}`}},
	}
	vs := &ViewSession{
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
		Reviews: []*ReviewScope{review},
		SystemPrompts: []SystemPrompt{
			{TaskTypes: []TaskType{MainTask}, Text: "investigate"},
		},
	}

	tests := []struct {
		name string
		data any
	}{
		{"repos.html", map[string]any{"Repos": []RepoInfo{{EncodedPath: "repo", LatestBizID: "github:org/repo#148", SessionCount: 1}}}},
		{"session.html", sessionPageData{EncodedRepo: "repo", RepoName: "repo", Session: vs}},
		{"review.html", reviewPageData{EncodedRepo: "repo", RepoName: "repo", Session: vs, Review: review}},
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
			if tt.name == "repos.html" && !strings.Contains(out.String(), "github:org/repo#148") {
				t.Fatal("repository template does not show the latest biz id")
			}
			if tt.name == "session.html" && !strings.Contains(out.String(), `href="/r/repo/session-1/review?scope=internal%2Fviewer%2Fstore.go"`) {
				t.Fatal("session review link does not preserve the repo and session path")
			}
			if tt.name == "session.html" && !strings.Contains(out.String(), "github:org/repo#148") {
				t.Fatal("session template does not show biz id")
			}
			if tt.name == "session.html" && (!strings.Contains(out.String(), "Diff Files") || !strings.Contains(out.String(), "Review Files") || !strings.Contains(out.String(), "Review 1")) {
				t.Fatal("session template does not show the diff-to-Review-1 funnel")
			}
			if tt.name == "session.html" && (!strings.Contains(out.String(), "Review Pipeline") || !strings.Contains(out.String(), "Review health") || !strings.Contains(out.String(), "unassessed")) {
				t.Fatal("session template does not surface pipeline health")
			}
			if tt.name == "session.html" && !strings.Contains(out.String(), "Review 2 Lanes") {
				t.Fatal("session template does not list Review 2 by Lane")
			}
			if tt.name == "review.html" && (!strings.Contains(out.String(), "Already covered") || !strings.Contains(out.String(), "Same-path repeats")) {
				t.Fatal("review page does not separate covered and repeated file reads")
			}
			if tt.name == "review.html" && (!strings.Contains(out.String(), "Executions") || !strings.Contains(out.String(), `data-conversation-node="execution-1-1"`) || !strings.Contains(out.String(), "Prompt Snapshot") || !strings.Contains(out.String(), "Reasoning")) {
				t.Fatal("review page does not render execution prompt snapshots")
			}
			if tt.name == "review.html" && strings.Contains(out.String(), `data-review-view="turns"`) {
				t.Fatal("review page still renders the removed Turns view")
			}
		})
	}
}

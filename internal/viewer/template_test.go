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
	}
	review := &ReviewRun{
		ID:       "internal/viewer/store.go",
		Kind:     "unit",
		Scope:    "file",
		Stage:    Review1Stage,
		Paths:    []string{"internal/viewer/store.go"},
		FilePath: "internal/viewer/store.go",
		Tasks:    map[TaskType][]*TaskCard{MainTask: {turn}},
		Calls:    []*TaskCard{turn},
		Turns:    []*TaskCard{turn},
		Metrics: ReviewMetrics{
			LLMCalls:        1,
			TurnCount:       1,
			PromptTokens:    120,
			MaxPromptTokens: 120,
		},
	}
	vs := &ViewSession{
		Summary: SessionSummary{SessionID: "session-1", CWD: "/repo"},
		Reviews: []*ReviewRun{review},
		SystemPrompts: []SystemPrompt{
			{TaskTypes: []TaskType{MainTask}, Text: "investigate"},
		},
	}

	tests := []struct {
		name string
		data any
	}{
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
		})
	}
}

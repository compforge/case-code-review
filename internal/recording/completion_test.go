package recording

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// +case=`Partial, skipped and never-started Units remain incomplete independently of findings and warning names`
func TestCompletionUsesPipelineOutcomes(t *testing.T) {
	tests := []struct {
		name, outcome, want string
		unassessed          []string
		ended               bool
		failure             string
	}{
		{name: "complete", outcome: "completed", ended: true, want: "completed"},
		{name: "timeout", outcome: "timeout", ended: true, want: "partial"},
		{name: "skipped", outcome: "skipped_policy", ended: true, want: "partial"},
		{name: "never started", ended: true, want: "partial"},
		{name: "unassessed", outcome: "completed", unassessed: []string{"h1"}, ended: true, want: "partial"},
		{name: "not finalized", outcome: "completed", want: "running"},
		{name: "fatal", outcome: "completed", ended: true, failure: "load failed", want: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := &Review{Scopes: map[string]*ReviewScope{}, operationError: tt.failure}
			if tt.ended {
				review.EndTime = time.Now()
			}
			scope := Scope{ID: "u1", Kind: "unit", Paths: []string{"a.go"}}
			review.GetOrCreateScope(scope)
			if tt.outcome != "" {
				review.CloseScope(scope, Debrief{Outcome: tt.outcome, Unassessed: tt.unassessed})
			}
			// A Lane or auxiliary call does not define the Unit's completion.
			review.GetOrCreateScope(Scope{ID: "lane", Kind: "lane"})
			got := review.Completion()
			if got.Status != tt.want || len(got.Units) != 1 {
				t.Fatalf("completion = %+v", got)
			}
			got.Units[0].Paths[0] = "mutated"
			if review.Completion().Units[0].Paths[0] != "a.go" {
				t.Fatal("result aliases live scope")
			}
		})
	}
}

func TestCompletionPersistsWithoutReadingSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	review := New(t.TempDir(), "main", "test", SessionOptions{})
	review.CloseScope(Scope{ID: "u1", Kind: "unit"}, Debrief{Outcome: "timeout", Unassessed: []string{"h1"}})
	review.Finalize()
	path, err := review.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transcript.Completion(), review.Completion()) {
		t.Fatalf("completion drift: %+v", transcript.Completion())
	}
	if transcript.Completion().Status != "partial" {
		t.Fatal("partial review became clean")
	}
	// Finalization seals the same result even if called twice.
	review.Finalize(errors.New("late failure"))
	if review.Completion().Status != "partial" {
		t.Fatal("duplicate finalization changed outcome")
	}
}

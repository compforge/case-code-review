package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/runner"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
)

func TestSanitizeTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "hello world", "hello world"},
		{"preserves tab", "col1\tcol2", "col1\tcol2"},
		{"preserves newline", "line1\nline2", "line1\nline2"},
		{"strips ESC", "before\x1b[2Jafter", "before[2Jafter"},
		{"strips OSC 52", "\x1b]52;c;dGVzdA==\x07", "]52;c;dGVzdA=="},
		{"strips BEL alone", "beep\x07done", "beepdone"},
		{"strips null byte", "a\x00b", "ab"},
		{"strips DEL", "a\x7fb", "ab"},
		{"strips carriage return", "fake\rreal", "fakereal"},
		{"empty string", "", ""},
		{"only control chars", "\x1b\x07\x00\x7f", ""},
		{"unicode preserved", "代码审查 レビュー 🔍", "代码审查 レビュー 🔍"},
		{"mixed safe and unsafe", "path\x1b[0m/file.go", "path[0m/file.go"},
		{"strips C1 CSI (U+009B)", "beforeafter", "beforeafter"},
		{"strips C1 OSC (U+009D)", "beforeafter", "beforeafter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTerminal(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeTerminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestJSONLEmitterWritesOrderedEvents(t *testing.T) {
	var output bytes.Buffer
	emitter := newJSONLEmitter(&output)
	emitter.start(&recording.Review{SessionID: "session-1", RepoDir: "/repo"})
	emitter.finding(finding.Finding{Path: "a.go", Content: "bug", Fingerprint: "abc123"})
	emitter.finish(jsonOutput{
		Status: "completed_with_warnings", Version: Version,
		Comments:  []finding.Finding{},
		ToolCalls: &jsonToolCalls{ByTool: map[string]int64{}},
		Warnings:  []runner.Warning{{Type: "unit_incomplete", File: "a.go", Message: "partial"}},
	})
	if err := emitter.Error(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("event lines = %d, want 4\n%s", len(lines), output.String())
	}
	wantTypes := []string{"run_started", "finding", "warning", "run_finished"}
	for i, line := range lines {
		var event jsonlEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if event.Type != wantTypes[i] || event.Sequence != int64(i+1) {
			t.Errorf("event %d = type %q sequence %d", i, event.Type, event.Sequence)
		}
	}
}

func TestOutputJSONFatal_EmitsParseableFailedJSON(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	emitErr := outputJSONFatal(
		errors.New("all 3 unit review(s) failed"),
		[]runner.Warning{{Type: "unit_error", File: "a.go", Message: "context deadline exceeded"}},
		nil,
	)
	w.Close()
	os.Stdout = old
	if emitErr != nil {
		t.Fatal(emitErr)
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var out jsonOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, raw)
	}
	if out.Status != "failed" {
		t.Errorf("status = %q, want failed", out.Status)
	}
	if out.Message != "all 3 unit review(s) failed" {
		t.Errorf("message = %q", out.Message)
	}
	if len(out.Warnings) != 1 || out.Warnings[0].Type != "unit_error" {
		t.Errorf("warnings not carried through: %+v", out.Warnings)
	}
	if out.Comments == nil {
		t.Error("comments must be [] not null")
	}
}

func TestAttachSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sh := &recording.Review{SessionID: "session-1", RepoDir: "/repo"}
	out := jsonOutput{}

	attachSession(&out, sh)

	if out.SessionID != "session-1" {
		t.Fatalf("session_id = %q", out.SessionID)
	}
	if !strings.HasSuffix(out.SessionPath, "session-1.jsonl") {
		t.Fatalf("session_path = %q", out.SessionPath)
	}
}

// +case=`Warnings cannot turn partial coverage into clean or make a completed review incomplete`
func TestOutputUsesBusinessCompletion(t *testing.T) {
	for _, tt := range []struct {
		name, outcome, want string
		findings            bool
		warnings            []runner.Warning
	}{
		{name: "partial without warning", outcome: "timeout", want: "completed_with_errors"},
		{name: "partial with finding", outcome: "timeout", findings: true, want: "completed_with_errors"},
		{name: "clean", outcome: "completed", want: "success"},
		{name: "diagnostic only", outcome: "completed", warnings: []runner.Warning{{Type: "subtask_error", Message: "diagnostic"}}, want: "completed_with_warnings"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			review := &recording.Review{Scopes: map[string]*recording.ReviewScope{}, EndTime: time.Now()}
			review.CloseScope(recording.Scope{ID: "unit", Kind: "unit"}, recording.Debrief{Outcome: tt.outcome})
			var comments []finding.Finding
			if tt.findings {
				comments = []finding.Finding{{Path: "a.go", Content: "accepted finding"}}
			}
			got := buildJSONWithWarnings(comments, tt.warnings, 1, 0, 0, 0, 0, 0, 0, "", nil, nil, review)
			if got.Status != tt.want || len(got.Comments) != len(comments) {
				t.Fatalf("result = %+v", got)
			}
			if tt.outcome != "completed" && (strings.Contains(got.Message, "Looks good") || got.Completion.Status != "partial") {
				t.Fatalf("false clean: %+v", got)
			}
			if tt.outcome == "completed" && !tt.findings && !strings.Contains(got.Message, "Looks good") {
				t.Fatalf("complete result lost: %+v", got)
			}
			old := os.Stdout
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = writer
			outputTextWithWarnings(comments, tt.warnings, review.Completion())
			os.Stdout = old
			writer.Close()
			raw, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if tt.outcome != "completed" && (!strings.Contains(string(raw), "incomplete") || strings.Contains(string(raw), "Looks good")) {
				t.Fatalf("false-clean text: %s", raw)
			}
		})
	}
}

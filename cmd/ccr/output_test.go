package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner"
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
	sh := &session.SessionHistory{SessionID: "session-1", RepoDir: "/repo"}
	out := jsonOutput{}

	attachSession(&out, sh)

	if out.SessionID != "session-1" {
		t.Fatalf("session_id = %q", out.SessionID)
	}
	if !strings.HasSuffix(out.SessionPath, "session-1.jsonl") {
		t.Fatalf("session_path = %q", out.SessionPath)
	}
}

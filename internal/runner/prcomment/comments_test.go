package prcomment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPreservesAllPRCommentFacts(t *testing.T) {
	comments, err := Load("testdata/comments.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 4 {
		t.Fatalf("comments=%d", len(comments))
	}
	text := comments.Render()
	for _, raw := range comments {
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, row["body"].(string)) || !strings.Contains(text, row["id"].(string)) {
			t.Fatalf("missing comment: %s", raw)
		}
	}
	for _, want := range []string{`"reply_to_id":"conversation:reply"`, `"outdated":true`, `"resolved_by":"alice"`, "not proof of acceptance or a fix", "untrusted discussion evidence"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestLoadRejectsNonCommentArrays(t *testing.T) {
	for _, data := range []string{`{"a.go":[{"msg":"old finding"}]}`, `["not a comment"]`, `[null]`, `{broken`} {
		path := filepath.Join(t.TempDir(), "comments.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	for _, data := range []string{"", "   ", "[]", "null"} {
		path := filepath.Join(t.TempDir(), "comments.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		comments, err := Load(path)
		if err != nil || comments.Render() != "" {
			t.Fatalf("empty input: %v %v", comments, err)
		}
	}
	if comments, err := Load(""); err != nil || len(comments) != 0 {
		t.Fatalf("no input: %v %v", comments, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
}

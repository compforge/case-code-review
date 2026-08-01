package llmloop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// fileReadStub returns a canned file_read-shaped result for every call, so the
// loop produces promotable File messages.
type fileReadStub struct{ body string }

func (fileReadStub) Tool() tool.Tool { return tool.FileRead }
func (s fileReadStub) Execute(context.Context, map[string]any) (string, error) {
	return s.body, nil
}

func TestRunPerFile_FileDedupStubsCoveredRead(t *testing.T) {
	result := fmt.Sprintf("File: pkg/a.go (Total lines: 3)\nIS_TRUNCATED: false\nLINE_RANGE: 1-3\n%s",
		"1|package a\n2|\n3|func F() {}\n")

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResp("file_read", `{"file_path":"pkg/a.go"}`), // round 1: read
		toolCallResp("file_read", `{"file_path":"pkg/a.go"}`), // round 2: same read again
		toolCallResp("task_done", `{}`),
	}}
	reg := tool.NewRegistry()
	reg.Register(fileReadStub{body: result})
	reg.Freeze()
	r := NewRunner(Deps{
		LLMClient:        client,
		Template:         template.Template{MaxToolRequestTimes: 10, MaxTokens: 10000},
		Tools:            reg,
		Session:          session.New(".", "test", "m", session.SessionOptions{}),
		FileDedupEnabled: true,
	})

	if _, err := r.RunPerFile(context.Background(), msg.Wrap([]llm.Message{llm.NewTextMessage("user", "review")}), scope()); err != nil {
		t.Fatalf("RunPerFile: %v", err)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected 3 rounds, got %d", len(client.requests))
	}

	// Round 3's request is built after the duplicate read landed: the FIRST
	// copy must be stubbed, the second kept in full, pairing ids intact.
	var full, stubs int
	for _, m := range client.requests[2].Messages {
		if m.Role != "tool" {
			continue
		}
		text := m.ExtractText()
		switch {
		case strings.Contains(text, "superseded"):
			stubs++
			if m.ToolCallID == "" {
				t.Fatalf("stub lost its tool_call pairing: %+v", m)
			}
		case strings.Contains(text, "func F() {}"):
			full++
		}
	}
	if stubs != 1 || full != 1 {
		t.Fatalf("want 1 stub + 1 full copy, got stubs=%d full=%d", stubs, full)
	}
}

func TestRunPerFile_FileDedupGateOff(t *testing.T) {
	result := fmt.Sprintf("File: pkg/a.go (Total lines: 1)\nIS_TRUNCATED: false\nLINE_RANGE: 1-1\n%s", "1|x\n")
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResp("file_read", `{"file_path":"pkg/a.go"}`),
		toolCallResp("file_read", `{"file_path":"pkg/a.go"}`),
		toolCallResp("task_done", `{}`),
	}}
	reg := tool.NewRegistry()
	reg.Register(fileReadStub{body: result})
	reg.Freeze()
	r := NewRunner(Deps{
		LLMClient: client,
		Template:  template.Template{MaxToolRequestTimes: 10, MaxTokens: 10000},
		Tools:     reg,
		Session:   session.New(".", "test", "m", session.SessionOptions{}),
	})
	if _, err := r.RunPerFile(context.Background(), msg.Wrap([]llm.Message{llm.NewTextMessage("user", "review")}), scope()); err != nil {
		t.Fatalf("RunPerFile: %v", err)
	}
	for _, m := range client.requests[2].Messages {
		if strings.Contains(m.ExtractText(), "superseded") {
			t.Fatal("gate off must not stub anything")
		}
	}
}

func TestEvictFiles(t *testing.T) {
	mk := func(path string, lines int) *msg.File {
		body := strings.Repeat("1|some code line here\n", lines)
		result := fmt.Sprintf("File: %s (Total lines: %d)\nIS_TRUNCATED: false\nLINE_RANGE: 1-%d\n%s", path, lines, lines, body)
		f := &msg.File{}
		if !f.FromLLM(msg.LLMToolResult{Tool: "file_read", ToolCallID: "c", Content: result}) {
			t.Fatalf("promotion failed for %s", path)
		}
		return f
	}
	oldest := mk("a.go", 60)
	middle := mk("b.go", 60)
	newest := mk("c.go", 60)
	msgs := []msg.Msg{msg.Text("user", "task"), oldest, middle, newest}

	// A limit reachable by shedding exactly the two oldest files (stubs still
	// cost tokens — measure one on a sacrificial twin instead of guessing).
	sacrifice := mk("a.go", 60)
	sacrifice.Stub(msg.StubEvicted)
	stubTokens := countMsgTokens([]msg.Msg{sacrifice})
	limit := countMsgTokens([]msg.Msg{msgs[0], newest}) + 2*stubTokens + 5
	if n := evictReclaimable(msgs, limit); n != 2 {
		t.Fatalf("evicted = %d, want 2", n)
	}
	if !oldest.Stubbed() || !middle.Stubbed() || newest.Stubbed() {
		t.Fatal("must evict oldest-first and keep the newest read")
	}
	if got := countMsgTokens(msgs); got > limit {
		t.Fatalf("still over limit after eviction: %d > %d", got, limit)
	}

	// Under-limit conversations are untouched.
	if n := evictReclaimable(msgs, 1<<20); n != 0 {
		t.Fatal("must not evict under the limit")
	}
}

// Regression: the async-compression snapshot must not share mutable *File
// state with the live conversation — under -race, a shallow snapshot trips
// when the main loop stubs a File while the background job lowers it.
func TestAsyncCompressionSnapshotRace(t *testing.T) {
	result := fmt.Sprintf("File: pkg/a.go (Total lines: 1)\nIS_TRUNCATED: false\nLINE_RANGE: 1-1\n%s", "1|x\n")
	f := &msg.File{}
	if !f.FromLLM(msg.LLMToolResult{Tool: "file_read", ToolCallID: "c1", Content: result}) {
		t.Fatal("promotion failed")
	}
	client := &scriptedClient{responses: []*llm.ChatResponse{{
		Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: strPtr("summary")}}},
	}}}
	r := NewRunner(Deps{
		LLMClient: client,
		Template: template.Template{MaxTokens: 10000, MemoryCompressionTask: template.LlmConversation{
			Messages: []template.ChatMessage{{Role: "user", Content: "compress: {{context}}"}},
		}},
		Session: session.New(".", "test", "m", session.SessionOptions{}),
	})

	messages := []msg.Msg{msg.Text("system", "s"), msg.Text("user", "task"), f}
	r.triggerAsyncCompression(context.Background(), messages, scope())
	f.Stub(msg.StubEvicted) // main loop keeps mutating while the job snapshots

	r.compressionMu.Lock()
	job := r.pendingJob
	r.compressionMu.Unlock()
	if job != nil {
		<-job.done
	}
}

func strPtr(s string) *string { return &s }

// Board eviction: a Board digest is reclaimable, shed before LLM compression.
func TestEvictReclaimable_IncludesBoard(t *testing.T) {
	bd := msg.NewBoard("peer notes: " + strings.Repeat("x", 400))
	msgs := []msg.Msg{msg.Text("user", "task"), bd}
	full := countMsgTokens(msgs)
	if n := evictReclaimable(msgs, full-1); n != 1 {
		t.Fatalf("board digest must be evictable, got %d", n)
	}
	if !bd.Reclaimed() {
		t.Fatal("board not reclaimed")
	}
	lw := bd.Lower()
	if !strings.Contains(lw.ExtractText(), "elided") {
		t.Fatalf("reclaimed board must render a pointer: %q", lw.ExtractText())
	}
}

type stubBoard struct {
	digest string
	pulls  int
	posts  []board.Bulletin
}

func (s *stubBoard) Register(string, board.Interest) {}
func (s *stubBoard) Publish(b board.Bulletin)        { s.posts = append(s.posts, b) }
func (s *stubBoard) Pull(string) (string, int) {
	s.pulls++
	if s.pulls == 1 && s.digest != "" {
		return s.digest, 1
	}
	return "", 0
}

func TestRunPerFile_BoardPullInjectsAndAutoPublishes(t *testing.T) {
	result := fmt.Sprintf("File: pkg/a.go (Total lines: 1)\nIS_TRUNCATED: false\nLINE_RANGE: 1-1\n%s", "1|x\n")
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResp("file_read", `{"file_path":"pkg/a.go"}`),
		toolCallResp("task_done", `{}`),
	}}
	reg := tool.NewRegistry()
	reg.Register(fileReadStub{body: result})
	reg.Freeze()
	sb := &stubBoard{digest: "peer confirmed: read shared.go"}
	r := NewRunner(Deps{
		LLMClient: client,
		Template:  template.Template{MaxToolRequestTimes: 10, MaxTokens: 10000},
		Tools:     reg,
		Session:   session.New(".", "test", "m", session.SessionOptions{}),
		Board:     sb,
	})
	outcome, err := r.RunPerFile(context.Background(), msg.Wrap([]llm.Message{llm.NewTextMessage("user", "review")}), scope())
	if err != nil {
		t.Fatalf("RunPerFile: %v", err)
	}
	// Turn 1 pulled the peer digest — it must appear in that request.
	if !strings.Contains(requestText(client.requests[0]), "peer confirmed: read shared.go") {
		t.Fatalf("board digest not injected into turn 1")
	}
	if outcome.BoardPulled != 1 {
		t.Fatalf("BoardPulled = %d, want 1", outcome.BoardPulled)
	}
	// The file_read was auto-published as a fact keyed by path.
	if outcome.BoardPosted != 1 || len(sb.posts) != 1 || sb.posts[0].Paths[0] != "pkg/a.go" {
		t.Fatalf("auto-publish off: posted=%d posts=%+v", outcome.BoardPosted, sb.posts)
	}
	if sb.posts[0].Level != board.LevelConfirmed {
		t.Fatalf("read fact must be confirmed-level: %v", sb.posts[0].Level)
	}
}

func requestText(req llm.ChatRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if s, ok := m.Content.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

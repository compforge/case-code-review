package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecutionBoundsModelOutputWithoutSuppressingReread(t *testing.T) {
	provider := &fileReadProvider{body: "File: pkg/a.go (Total lines: 1)\nIS_TRUNCATED: false\nLINE_RANGE: 1-1\n1|" + strings.Repeat("a", 100_000)}
	registry := tool.NewRegistry()
	registry.Register(provider)
	registry.Freeze()
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("read-1", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("read-2", "read_files", `{"reads":[{"file_path":"pkg/a.go","start_line":1,"end_line":1}]}`, nil),
		toolCallResponseID("done", "task_done", `{}`, nil),
	}}
	history := &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)}
	scope := session.Scope{ID: "bounded", Kind: "unit", Paths: []string{"pkg/a.go"}}
	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "review this unit")},
		ToolDefs: []llm.ToolDef{toolDef("read_files"), toolDef("task_done")}, Tools: registry,
		Session: history, Scope: scope, MaxTurns: 3, ContextWindow: 100_000, FileDedupEnabled: true,
	})
	if err != nil || result.State != OutcomeCompleted || provider.calls != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, provider.calls, err)
	}
	seen := 0
	for _, request := range client.Requests()[1:] {
		for _, message := range request.Messages {
			if message.Role != "tool" {
				continue
			}
			content := message.ExtractText()
			if len(content) > tool.MaxResultBytes || !strings.Contains(content, "Output truncated") {
				t.Fatalf("unbounded/unmarked tool output: %d bytes", len(content))
			}
			seen++
		}
	}
	if seen != 3 {
		t.Fatalf("tool observations = %d, want 3", seen)
	}
	records := history.Scopes[scope.ID].TaskRecords[session.MainTask]
	if records[0].ToolResults[0].Result != tool.EncodeFileReadResults([]string{provider.body}) {
		t.Fatal("session lost original provider output")
	}
}

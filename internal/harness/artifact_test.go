package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecutionArtifactRegistrationAndContinuation(t *testing.T) {
	initial := msg.NewFile("a.go", 1, 1, 2, "File: a.go (Total lines: 2)\nIS_TRUNCATED: true\nLINE_RANGE: 1-1\n1|first\n")
	source := "File: a.go (Total lines: 2)\nIS_TRUNCATED: false\nLINE_RANGE: 1-2\n1|first\n2|second\n"
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("read", "read_files", `{"reads":[{"file_path":"a.go","start_line":1,"end_line":2}]}`, nil),
		toolCallResponse("task_done", `{}`, nil),
	}}
	result, err := runExecution(t.Context(), ExecutionSpec{
		LLMClient: client, Messages: []agentgo.AgentMessage{initial},
		ToolDefs: []llm.ToolDef{toolDef("read_files"), toolDef("task_done")},
		ToolHandler: toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
			if request.Tool.Name() == "read_files" {
				return tool.Of(tool.EncodeFileReadResults([]string{source})), true
			}
			return tool.TaskCheckpoint{}, false
		}), MaxTurns: 2, FileDedupEnabled: true,
	})
	if err != nil || result.State != OutcomeCompleted {
		t.Fatalf("run=%+v err=%v", result, err)
	}
	if len(result.artifacts) != 2 {
		t.Fatalf("initial and tool sources not registered: %v", result.artifacts)
	}
	requests := client.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	text := requestText(requests[1])
	if strings.Count(text, "1|first") != 1 || strings.Count(text, "2|second") != 1 {
		t.Fatalf("source projection: %s", text)
	}
	found := false
	for _, m := range requests[1].Messages {
		if m.Role == "tool" && m.ToolCallID == "read" {
			found = true
		}
	}
	if !found {
		t.Fatal("tool pairing lost")
	}
	rawFound := false
	for _, m := range result.context {
		if strings.Contains(m.Raw().TextContent(), "1|first\n2|second") {
			rawFound = true
		}
	}
	if !rawFound {
		t.Fatal("raw tool source changed")
	}

	// Continuation may retain only a summary. Materials survive independently,
	// but cannot by themselves make any source line count as visible evidence.
	result.context = []agentgo.AgentMessage{msg.Text("user", "summary")}
	continuationClient := &scriptedClient{responses: []*llm.ChatResponse{toolCallResponse("task_done", `{}`, nil)}}
	continued, err := runExecution(t.Context(), ExecutionSpec{LLMClient: continuationClient, ContinueFrom: &result, Messages: []agentgo.AgentMessage{initial}, ToolDefs: []llm.ToolDef{toolDef("task_done")}, MaxTurns: 1, FileDedupEnabled: true})
	if err != nil || continued.State != OutcomeCompleted || len(continued.artifacts) != 2 {
		t.Fatalf("continue=%+v err=%v", continued, err)
	}
	if strings.Count(requestText(continuationClient.Requests()[0]), "1|first") != 1 {
		t.Fatal("retained material hid fresh source")
	}
	independent, err := runExecution(t.Context(), ExecutionSpec{LLMClient: &scriptedClient{responses: []*llm.ChatResponse{toolCallResponse("task_done", `{}`, nil)}}, Messages: []agentgo.AgentMessage{msg.Text("user", "unrelated")}, ToolDefs: []llm.ToolDef{toolDef("task_done")}, MaxTurns: 1})
	if err != nil || len(independent.artifacts) != 0 {
		t.Fatal("independent execution inherited materials")
	}
}

func TestExecutionProjectsCallerMaterialsOnEveryRequest(t *testing.T) {
	body := strings.Repeat("Preserve cancellation and report the original error. ", 30)
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("inspect", `{}`, nil), toolCallResponse("task_done", `{}`, nil),
	}}
	result, err := runExecution(t.Context(), ExecutionSpec{
		LLMClient: client,
		Messages: []agentgo.AgentMessage{
			msg.FixedText("user", "Review the change using the supplied evidence."),
			newMaterialTestMessage("caller.go::Run", "", "caller", body),
			newMaterialTestMessage("callee.go::Call", "", "callee", body),
		},
		ToolDefs: []llm.ToolDef{toolDef("inspect"), toolDef("task_done")},
		ToolHandler: toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
			if request.Tool.Name() == "inspect" {
				return tool.Of("inspection completed"), true
			}
			return tool.TaskCheckpoint{}, false
		}),
		MaxTurns: 2,
	})
	if err != nil || result.State != OutcomeCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.artifacts) != 1 || result.artifacts[0].Kind() != "document" {
		t.Fatal("document body was not registered once")
	}
	for _, request := range client.Requests() {
		text := requestText(request)
		if strings.Count(text, body) != 1 || !strings.Contains(text, "caller.go::Run") || !strings.Contains(text, "callee.go::Call") {
			t.Fatal("request lost body coverage or provenance")
		}
	}
	if len(client.Requests()) != 2 {
		t.Fatal("second turn missing")
	}
	// Request projection does not rewrite the committed evidence.
	rawCount := 0
	for _, message := range result.context {
		if strings.Contains(message.Raw().TextContent(), body) {
			rawCount++
		}
	}
	if rawCount != 2 {
		t.Fatal("projection changed raw document evidence")
	}
}

// An external message implementation exercises Harness's material capability
// without importing the review domain. The integration test above retains its
// registration, per-turn coverage and committed Raw assertions.
type testMaterialArtifact string

func (a testMaterialArtifact) ID() string { return "test:" + string(a) }
func (testMaterialArtifact) Kind() string { return "document" }

type testMaterialMessage struct {
	agentgo.Message
	ref, body, duplicateOf string
}

func newMaterialTestMessage(ref, snapshot, label, body string) *testMaterialMessage {
	return &testMaterialMessage{Message: agentgo.Message{Role: agentgo.RoleUser}, ref: ref, body: body}
}
func (m *testMaterialMessage) TextContent() string {
	if m.duplicateOf != "" {
		return m.ref + " same as " + m.duplicateOf
	}
	return m.ref + "\n" + m.body
}
func (m *testMaterialMessage) ToMessage() (agentgo.Message, bool) {
	return agentgo.Message{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock(m.TextContent())}}, true
}
func (m *testMaterialMessage) Raw() agentgo.AgentMessage                       { return m.WithMaterialReference("") }
func (m *testMaterialMessage) Compact(float64) (agentgo.AgentMessage, float64) { return m, 1 }
func (m *testMaterialMessage) MaterialArtifact() agentgo.Artifact {
	return testMaterialArtifact(m.body)
}
func (m *testMaterialMessage) MaterialReference() string { return m.ref }
func (m *testMaterialMessage) MaterialVisible() bool     { return m.duplicateOf == "" }
func (m *testMaterialMessage) WithMaterialReference(ref string) msg.MaterialMessage {
	copy := *m
	copy.duplicateOf = ref
	return &copy
}

func TestPreviewProjectionMatchesLiveRequest(t *testing.T) {
	initial := []agentgo.AgentMessage{
		msg.FixedText("user", "review"),
		msg.NewFile("a.go", 1, 1, 1, "File: a.go (Total lines: 1)\n1|same source\n"),
		msg.NewFile("a.go", 1, 1, 1, "File: a.go (Total lines: 1)\n1|same source\n"),
	}
	expected := ProjectContext(initial, msg.Artifacts(initial), true)
	repeated := ProjectContext(expected, msg.Artifacts(initial), true)
	if len(expected) != len(repeated) {
		t.Fatal("projection accumulated derived inventory")
	}
	for i, m := range expected {
		if m.TextContent() != repeated[i].TextContent() {
			t.Fatal("non-idempotent projection")
		}
	}
	client := &scriptedClient{responses: []*llm.ChatResponse{toolCallResponse("task_done", `{}`, nil)}}
	result, err := runExecution(t.Context(), ExecutionSpec{LLMClient: client, Messages: initial, ToolDefs: []llm.ToolDef{toolDef("task_done")}, MaxTurns: 1, FileDedupEnabled: true})
	if err != nil || result.State != OutcomeCompleted {
		t.Fatal(result.State, err)
	}
	request := client.Requests()[0]
	if len(request.Messages) != len(expected) {
		t.Fatalf("live=%d preview=%d", len(request.Messages), len(expected))
	}
	for i, m := range expected {
		if request.Messages[i].ExtractText() != m.TextContent() {
			t.Fatalf("message %d differs", i)
		}
	}
}

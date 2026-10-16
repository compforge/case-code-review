package msg

import (
	"reflect"
	"testing"

	"github.com/compforge/agentgo"
)

func TestSessionCodecPreservesDomainMessages(t *testing.T) {
	file := NewFile("a.go", 1, 1, 1, "1|package a")
	file.toolCallID = "read-1"
	file.Outline = "package a"
	file.representation = fileOutline
	search := &SearchResult{messageMeta: newMessageMeta(), Tool: "find_files", Query: "*.go", Paths: []string{"a.go"}, Content: "a.go", representation: searchReference}
	messages := []agentgo.AgentMessage{
		FixedText("user", "review"), file,
		&FileBatch{messageMeta: newMessageMeta(), items: []fileBatchItem{{file: file}, {raw: "not found"}}, tool: "read_files", toolCallID: "read-1", representation: fileBatchOutline},
		search, &SearchBatch{messageMeta: newMessageMeta(), items: []searchBatchItem{{result: search}, {raw: "no matches"}}, sharedSource: "unit", toolCallID: "search-1", representation: searchBatchReference},
		NewDiff([]string{"a.go"}, "+package a"),
		NewFileContext([]FileContextEntry{{Path: "a.go", View: ViewOutline, Reason: "caller", Content: "package a"}}), NewBoard("confirmed caller"),
		ToolReceipt{messageMeta: newMessageMeta(), Tool: "task_done", Content: "accepted", ToolCallID: "done"},
		&ToolView{AgentMessage: file, content: "bounded"},
	}
	codec, err := Codec()
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		t.Run(reflect.TypeOf(message).String(), func(t *testing.T) {
			raw, err := codec.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			var restored agentgo.AgentMessage
			if err = codec.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.TextContent() != message.TextContent() || restored.Raw().TextContent() != message.Raw().TextContent() || !restored.GetTimestamp().Equal(message.GetTimestamp()) || !reflect.DeepEqual(contextItems(restored), contextItems(message)) {
				t.Fatalf("lost message semantics: %s", raw)
			}
		})
	}
}

func contextItems(m agentgo.AgentMessage) []agentgo.ContextItem {
	if described, ok := m.(interface{ ContextItems() []agentgo.ContextItem }); ok {
		return described.ContextItems()
	}
	return nil
}

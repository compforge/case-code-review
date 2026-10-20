package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// writeModelStream turns a complete test response into provider SSE events.
// Transport tests still assert the actual request, cache markers and usage.
func writeModelStream(t *testing.T, w http.ResponseWriter, raw string) {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	if response["type"] == "message" {
		content, _ := response["content"].([]any)
		start := map[string]any{}
		for key, value := range response {
			start[key] = value
		}
		start["content"] = []any{}
		start["stop_reason"] = nil
		emit(map[string]any{"type": "message_start", "message": start})
		for i, value := range content {
			block := value.(map[string]any)
			emit(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			emit(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": block["text"]}})
			emit(map[string]any{"type": "content_block_stop", "index": i})
		}
		emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": response["stop_reason"]}, "usage": response["usage"]})
		emit(map[string]any{"type": "message_stop"})
	} else {
		for i, value := range response["choices"].([]any) {
			choice := value.(map[string]any)
			choice["index"] = i
			choice["delta"] = choice["message"]
			delete(choice, "message")
		}
		emit(response)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

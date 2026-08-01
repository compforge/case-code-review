package runner

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/template"
)

func TestReviewCompressionPromptsAdaptTemplateContext(t *testing.T) {
	system, instruction := reviewCompressionPrompts(Args{
		Template: template.Template{
			MemoryCompressionTask: template.LlmConversation{
				Messages: []template.ChatMessage{
					{Role: "system", Content: "preserve confirmed findings"},
					{Role: "user", Content: "{{context}}"},
				},
			},
		},
	})
	if system != "preserve confirmed findings" {
		t.Fatalf("system prompt = %q", system)
	}
	if strings.Contains(instruction, "{{context}}") ||
		!strings.Contains(instruction, "conversation supplied above") {
		t.Fatalf("instruction was not adapted for agentcore: %q", instruction)
	}
}

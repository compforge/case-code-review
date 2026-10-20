package finding

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// ReLocateComment calls the LLM to regenerate a precise existing_code snippet
// when text-based matching fails, then retries ResolveComment with the new snippet.
// Returns (success, response, requestMessages) so the caller can record session
// history and track token usage. Response and messages are nil on early exits.
func ReLocateComment(ctx context.Context, cm *Finding, d *change.Change, client agentgo.ChatModel, task *template.LlmConversation, maxTokens int) (bool, *agentgo.LLMResponse, []agentgo.Message) {
	if task == nil || len(task.Messages) == 0 {
		return false, nil, nil
	}

	if task.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(task.Timeout)*time.Second)
		defer cancel()
	}

	messages := make([]agentgo.Message, 0, len(task.Messages))
	for _, m := range task.Messages {
		content := m.Content
		content = strings.ReplaceAll(content, "{diff}", d.Diff)
		content = strings.ReplaceAll(content, "{existing_code}", cm.ExistingCode)
		content = strings.ReplaceAll(content, "{suggestion_content}", cm.Content)
		messages = append(messages, agentgo.Message{Role: agentgo.Role(m.Role), Content: []agentgo.ContentBlock{agentgo.TextBlock(content)}})
	}

	resp, err := llm.Call(ctx, client, agentgo.LLMRequest{Messages: messages, Config: &agentgo.CallConfig{MaxTokens: maxTokens, ThinkingLevel: agentgo.NormalizeThinkingLevel(agentgo.ThinkingLevel(task.ReasoningEffort))}})
	if err != nil {
		fmt.Fprintf(console.Out(), "[ccr] Re-location LLM call failed for %s: %v\n", cm.Path, err)
		return false, resp, messages
	}

	content, err := llm.CompleteText(resp)
	if err != nil {
		return false, resp, messages
	}
	code := extractCodeBlock(content)
	if code == "" {
		return false, resp, messages
	}

	original := cm.ExistingCode
	cm.ExistingCode = code
	if ResolveComment(cm, d) {
		return true, resp, messages
	}
	cm.ExistingCode = original
	return false, resp, messages
}

// extractCodeBlock extracts the content of the first fenced code block from text.
// Returns empty string if no code block is found.
func extractCodeBlock(text string) string {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "```")
	if start < 0 {
		return ""
	}
	afterOpen := start + 3
	// Skip optional language tag on the opening fence line.
	if nl := strings.IndexByte(text[afterOpen:], '\n'); nl >= 0 {
		afterOpen += nl + 1
	} else {
		return ""
	}
	end := strings.Index(text[afterOpen:], "```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[afterOpen : afterOpen+end])
}

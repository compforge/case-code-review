// Derived from compforge/agentgo context compactors (Apache-2.0).
// CCR owns history-only policy; ZoneCompactor owns all suffix protection.
package compactor

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

const summarySystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI coding assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation.

First think briefly inside <analysis>...</analysis>. Then output the final checkpoint inside <summary>...</summary>.`

const summaryPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any, or "(none)"]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, file paths, function names, or references needed to continue]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummaryPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing goals, ADD new ones if the task expanded
- PRESERVE existing constraints, ADD new ones discovered
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Blocked" with any new issues, remove resolved ones
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any, or "(none)"]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, file paths, function names, or references needed to continue]`

// summaryPrompts holds the resolved prompt texts for a summarization call.
// Zero-value fields fall back to the package-level defaults.
type summaryPrompts struct {
	System  string
	Summary string
	Update  string
}

func (p summaryPrompts) system() string {
	if p.System != "" {
		return p.System
	}
	return summarySystemPrompt
}
func (p summaryPrompts) summary() string {
	if p.Summary != "" {
		return p.Summary
	}
	return summaryPrompt
}
func (p summaryPrompts) update() string {
	if p.Update != "" {
		return p.Update
	}
	return updateSummaryPrompt
}

// generateSummary calls the ChatModel to produce a conversation summary.
// If previousSummary is non-empty, uses incremental update prompt.
func generateSummary(ctx context.Context, model agentgo.ChatModel, execution agentgo.Execution, prompts summaryPrompts, msgs []agentgo.AgentMessage, previousSummary string, opts ...agentgo.CallOption) (string, error) {
	const maxRetries = 3
	current := append([]agentgo.AgentMessage(nil), msgs...)
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		summary, err := generateSummaryOnce(ctx, model, execution.WithAttempt(attempt+1), prompts, current, previousSummary, opts...)
		if err == nil {
			return summary, nil
		}
		lastErr = err
		if !agentgo.IsContextOverflow(err) {
			return "", err
		}
		next := truncateOldestUserGroups(current, 0.2)
		if len(next) == 0 || len(next) == len(current) {
			break
		}
		current = next
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("summarization failed")
}

func generateSummaryOnce(ctx context.Context, model agentgo.ChatModel, execution agentgo.Execution, prompts summaryPrompts, msgs []agentgo.AgentMessage, previousSummary string, opts ...agentgo.CallOption) (string, error) {
	var fresh []agentgo.AgentMessage
	for _, message := range msgs {
		if _, ok := message.(agentcontext.ContextSummary); !ok {
			fresh = append(fresh, message)
		}
	}
	conversation := serializeConversation(fresh)
	if conversation == "" && previousSummary == "" {
		return "", fmt.Errorf("no conversation content to summarize")
	}

	var userPrompt string
	if previousSummary != "" {
		userPrompt = "<conversation>\n" + conversation + "\n</conversation>\n\n" +
			"<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n" +
			prompts.update()
	} else {
		userPrompt = "<conversation>\n" + conversation + "\n</conversation>\n\n" +
			prompts.summary()
	}

	message, err := executeSummaryModel(ctx, model, execution, []agentgo.Message{
		agentgo.SystemMsg(prompts.system()),
		agentgo.UserMsg(userPrompt),
	}, opts...)
	if err != nil {
		return "", fmt.Errorf("summarization failed: %w", err)
	}

	summary := extractStoredSummary(message.TextContent())
	if summary == "" {
		return "", fmt.Errorf("summarization returned empty content")
	}
	return summary, nil
}

// executeSummaryModel keeps summary calls on AgentGo's model execution path so
// they inherit the enclosing Loop's middleware and observable coordinates.
func executeSummaryModel(ctx context.Context, model agentgo.ChatModel, execution agentgo.Execution, messages []agentgo.Message, opts ...agentgo.CallOption) (agentgo.Message, error) {
	call := agentgo.ModelExecution{
		Execution: execution,
		Request:   agentgo.LLMRequest{Messages: messages},
		Options:   opts,
	}
	result, err := agentgo.ExecuteModel(ctx, call, func(ctx context.Context, execution agentgo.ModelExecution) (agentgo.ModelResult, error) {
		response, err := model.Generate(ctx, execution.Request.Messages, execution.Request.Tools, execution.Options...)
		if err != nil {
			return agentgo.ModelResult{}, err
		}
		return agentgo.ModelResult{Message: response.Message}, nil
	})
	return result.Message, err
}

func extractStoredSummary(text string) string {
	text = stripAnalysisBlock(strings.TrimSpace(text))
	if summary := extractTaggedBlock(text, "summary"); summary != "" {
		return strings.TrimSpace(summary)
	}
	return strings.TrimSpace(text)
}

func stripAnalysisBlock(text string) string {
	start := strings.Index(text, "<analysis>")
	end := strings.Index(text, "</analysis>")
	if start < 0 || end < 0 || end < start {
		return text
	}
	return strings.TrimSpace(text[:start] + text[end+len("</analysis>"):])
}

func extractTaggedBlock(text, tag string) string {
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"
	start := strings.Index(text, startTag)
	end := strings.Index(text, endTag)
	if start < 0 || end < 0 || end < start {
		return ""
	}
	start += len(startTag)
	return strings.TrimSpace(text[start:end])
}

func truncateOldestUserGroups(msgs []agentgo.AgentMessage, fraction float64) []agentgo.AgentMessage {
	if len(msgs) == 0 {
		return nil
	}
	var starts []int
	for i, msg := range msgs {
		if msg != nil && msg.GetRole() == agentgo.RoleUser {
			starts = append(starts, i)
		}
	}
	var result []agentgo.AgentMessage
	if len(starts) <= 1 {
		drop := int(math.Ceil(float64(len(msgs)) * fraction))
		if drop <= 0 || drop >= len(msgs) {
			return msgs
		}
		result = append([]agentgo.AgentMessage(nil), msgs[drop:]...)
	} else {
		dropGroups := int(math.Ceil(float64(len(starts)) * fraction))
		if dropGroups <= 0 {
			dropGroups = 1
		}
		if dropGroups >= len(starts) {
			dropGroups = len(starts) - 1
		}
		cut := starts[dropGroups]
		result = append([]agentgo.AgentMessage(nil), msgs[cut:]...)
	}

	// Ensure the truncated result starts with a user message.
	// LLM APIs reject conversations that start with an assistant message.
	if len(result) > 0 {
		if result[0].GetRole() != agentgo.RoleUser {
			result = append([]agentgo.AgentMessage{
				agentgo.UserMsg("[Earlier context truncated for summarization]"),
			}, result...)
		}
	}
	return result
}

// truncateForSummary caps s at roughly max bytes for summarization input,
// backing the cut up to a rune boundary so multi-byte UTF-8 sequences (e.g.
// CJK text) are never split. A half rune here poisons the summarization
// request: providers reject invalid UTF-8, the whole compaction fails, and —
// because history is deterministic — keeps failing at the same byte on every
// retry.
func truncateForSummary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// formatArgsKeyValue formats JSON tool args as key=value pairs.
// More token-efficient than raw JSON for LLM summarization input.
func formatArgsKeyValue(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return truncateForSummary(string(raw), 197)
	}

	var pairs []string
	for k, v := range obj {
		s := fmt.Sprintf("%v", v)
		pairs = append(pairs, k+"="+truncateForSummary(s, 97))
	}
	return strings.Join(pairs, ", ")
}

// serializeConversation converts messages to readable text for LLM input.
func serializeConversation(msgs []agentgo.AgentMessage) string {
	var parts []string

	for _, message := range agentgo.ToMessages(msgs) {
		switch message.Role {
		case agentgo.RoleUser:
			if text := message.TextContent(); text != "" {
				parts = append(parts, "[User]: "+text)
			}
		case agentgo.RoleAssistant:
			if thinking := message.ThinkingContent(); thinking != "" {
				parts = append(parts, "[Assistant thinking]: "+thinking)
			}
			if text := message.TextContent(); text != "" {
				parts = append(parts, "[Assistant]: "+text)
			}
			if toolCalls := message.ToolCalls(); len(toolCalls) > 0 {
				var calls []string
				for _, tc := range toolCalls {
					calls = append(calls, tc.Name+"("+formatArgsKeyValue(tc.Args)+")")
				}
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		case agentgo.RoleTool:
			content := truncateForSummary(message.TextContent(), 497)
			if content != "" {
				parts = append(parts, "[Tool result]: "+content)
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

// Package msg contains CCR's concrete review-domain AgentMessages. Each type
// owns its raw form, compaction policy, priority, and model projection; Harness
// passes the values to AgentGo; request-local ToolView values retain the original
// AgentMessage while changing only its displayed representation.
package msg

import (
	"encoding/json"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/llm"
)

type messageMeta struct {
	timestamp time.Time
}

func newMessageMeta() messageMeta { return messageMeta{timestamp: time.Now()} }

func (m messageMeta) GetTimestamp() time.Time { return m.timestamp }

// Text creates a generic model-level message. Steering prompts have no CCR
// domain lifecycle, so agentgo.Message is their native representation.
func Text(role, content string) agentgo.AgentMessage {
	message := wireMessage(llm.NewTextMessage(role, content), "")
	message.Timestamp = time.Now()
	return message
}

// Wrap converts existing prompt-builder output to AgentGo's model message.
// It is a construction helper, not a second application message interface.
func Wrap(messages []llm.Message) []agentgo.AgentMessage {
	out := make([]agentgo.AgentMessage, len(messages))
	for i, message := range messages {
		converted := wireMessage(message, "")
		converted.Timestamp = time.Now()
		out[i] = converted
	}
	return out
}

func wireMessage(message llm.Message, toolName string) agentgo.Message {
	content := []agentgo.ContentBlock{agentgo.TextBlock(message.ExtractText())}
	for _, call := range message.ToolCalls {
		content = append(content, agentgo.ToolCallBlock(agentgo.ToolCall{
			ID: call.ID, Name: call.Function.Name,
			Args: json.RawMessage(call.Function.Arguments),
		}))
	}
	converted := agentgo.Message{Role: agentgo.Role(message.Role), Content: content}
	if message.ToolCallID != "" || toolName != "" {
		converted.Metadata = make(map[string]any, 2)
		if message.ToolCallID != "" {
			converted.Metadata["tool_call_id"] = message.ToolCallID
		}
		if toolName != "" {
			converted.Metadata["tool_name"] = toolName
		}
	}
	return converted
}

func domainRole(message llm.Message) agentgo.Role { return agentgo.Role(message.Role) }

func domainText(message llm.Message) string { return message.ExtractText() }

func domainHasToolCalls(message llm.Message) bool { return len(message.ToolCalls) > 0 }

func domainToMessage(message llm.Message, toolName string, timestamp time.Time) (agentgo.Message, bool) {
	converted := wireMessage(message, toolName)
	converted.Timestamp = timestamp
	return converted, true
}

// compactRepresentation selects the first message-owned representation that
// satisfies expect. Representation types stay private to each message; this
// helper only centralizes ratio accounting.
func compactRepresentation[R ~uint8](
	expect float64,
	current, maxRepresentation R,
	render func(R) llm.Message,
) (R, float64) {
	var rawRepresentation R
	raw := render(rawRepresentation)
	rawTokens := llm.CountTokens(raw.ExtractText())
	if rawTokens <= 0 {
		return current, 1
	}
	ratio := func(representation R) float64 {
		wire := render(representation)
		return float64(llm.CountTokens(wire.ExtractText())) / float64(rawTokens)
	}
	actual := ratio(current)
	if actual <= expect {
		return current, actual
	}
	for representation := current + 1; representation <= maxRepresentation; representation++ {
		current = representation
		actual = ratio(representation)
		if actual <= expect {
			break
		}
	}
	return current, actual
}

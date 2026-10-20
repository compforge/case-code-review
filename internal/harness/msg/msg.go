// Package msg contains CCR's concrete review-domain AgentMessages. Each type
// owns its raw form, compaction policy, priority, and model projection; Harness
// passes the values to AgentGo; request-local ToolView values retain the original
// AgentMessage while changing only its displayed representation.
package msg

import (
	"maps"
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
	return agentgo.Message{Role: agentgo.Role(role), Content: []agentgo.ContentBlock{agentgo.TextBlock(content)}, Timestamp: time.Now()}
}

// Wrap boxes native messages as AgentMessages without changing their contents.
func Wrap(messages []agentgo.Message) []agentgo.AgentMessage {
	out := make([]agentgo.AgentMessage, len(messages))
	for i, m := range messages {
		out[i] = m
	}
	return out
}

func domainRole(message agentgo.Message) agentgo.Role { return message.Role }
func domainText(message agentgo.Message) string       { return message.TextContent() }
func domainHasToolCalls(message agentgo.Message) bool { return len(message.ToolCalls()) > 0 }
func domainToMessage(message agentgo.Message, toolName string, timestamp time.Time) (agentgo.Message, bool) {
	message.Timestamp = timestamp
	if toolName != "" {
		message.Metadata = maps.Clone(message.Metadata)
		if message.Metadata == nil {
			message.Metadata = map[string]any{}
		}
		message.Metadata["tool_name"] = toolName
	}
	return message, true
}

// compactRepresentation selects the first message-owned representation that
// satisfies expect. Representation types stay private to each message; this
// helper only centralizes ratio accounting.
func compactRepresentation[R ~uint8](expect float64, current, maxRepresentation R, render func(R) agentgo.Message) (R, float64) {
	var rawRepresentation R
	raw := render(rawRepresentation)
	rawTokens := llm.CountTokens(raw.TextContent())
	if rawTokens <= 0 {
		return current, 1
	}
	ratio := func(representation R) float64 {
		wire := render(representation)
		return float64(llm.CountTokens(wire.TextContent())) / float64(rawTokens)
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

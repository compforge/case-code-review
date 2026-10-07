package msg

import (
	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

// ToolView is a request-local representation. Its original message retains raw
// evidence even when the displayed text is bounded or deduplicated.
type ToolView struct {
	agentgo.AgentMessage
	content string
}

func (v *ToolView) Raw() agentgo.AgentMessage { return v.AgentMessage.Raw() }
func (v *ToolView) TextContent() string       { return v.content }
func (v *ToolView) ThinkingContent() string   { return "" }
func (v *ToolView) ToMessage() (agentgo.Message, bool) {
	m, include := v.AgentMessage.ToMessage()
	m.Content = []agentgo.ContentBlock{agentgo.TextBlock(v.content)}
	return m, include
}
func (v *ToolView) Compact(expect float64) (agentgo.AgentMessage, float64) {
	next, ratio := v.AgentMessage.Compact(expect)
	return limitToolMessage(next), ratio
}
func (v *ToolView) ToolName() string {
	if named, ok := v.AgentMessage.(interface{ ToolName() string }); ok {
		return named.ToolName()
	}
	return ""
}

// A clipped range must not advertise full source through ContextItems or the
// harness's typed-file visibility checks. It remains available through Raw.
func (v *ToolView) ContextItems() []agentgo.ContextItem { return nil }

func limitToolMessage(message agentgo.AgentMessage) agentgo.AgentMessage {
	if message == nil || message.GetRole() != agentgo.RoleTool {
		return message
	}
	if len(message.TextContent()) <= tool.MaxResultBytes {
		return message
	}
	return &ToolView{AgentMessage: message, content: tool.LimitResult(message.TextContent(), tool.MaxResultBytes)}
}

func LimitToolMessages(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
	out := append([]agentgo.AgentMessage(nil), messages...)
	for i, message := range out {
		out[i] = limitToolMessage(message)
	}
	return out
}

package compactor

import (
	"context"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

// ToolResultCompactor clears historical results oldest first. ZoneCompactor
// already protects complete active rounds, so no additional tail is retained.
type ToolResultCompactor struct{}

func (*ToolResultCompactor) Compact(_ context.Context, input agentgo.TransformContext, expect float64) ([]agentgo.AgentMessage, error) {
	messages := input.Messages
	if len(messages) == 0 || expect >= 1 {
		return messages, nil
	}
	out := copyMessages(messages)
	tokens := agentcontext.EstimateTotal(out)
	target := int(float64(tokens) * clampRatio(expect))
	calls := make(map[string]string)
	for i, message := range out {
		if tokens <= target {
			break
		}
		wire, ok := message.ToMessage()
		if !ok {
			continue
		}
		for _, call := range wire.ToolCalls() {
			calls[call.ID] = call.Name
		}
		if wire.Role != agentgo.RoleTool {
			continue
		}
		callID, _ := wire.Metadata["tool_call_id"].(string)
		name, ok := calls[callID]
		if !ok {
			continue
		}
		before := agentcontext.EstimateTokens(message)
		wire.Content = []agentgo.ContentBlock{agentgo.TextBlock(agentcontext.DefaultClearedToolResult)}
		wire.Metadata = cloneMetadata(wire.Metadata)
		wire.Metadata["compacted_tool_result"] = true
		wire.Metadata["compacted_tool_name"] = name
		if after := agentcontext.EstimateTokens(wire); after < before {
			out[i] = newProjectedMessage(message, wire)
			tokens -= before - after
		}
	}
	return out, nil
}

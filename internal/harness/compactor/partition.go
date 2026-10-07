package compactor

import (
	"fmt"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

type zone uint8

const (
	history zone = iota
	fixed
	active
)

// FixedContext supplies the essential task projection that must survive generic
// trimming and summarization. Its Raw must retain the original task message.
type FixedContext interface {
	FixedContext() agentgo.AgentMessage
}

type segment struct {
	zone     zone
	messages []agentgo.AgentMessage
}

// partition keeps chronological anchors in place, including instructions added
// between review tasks in a continued execution. History never crosses an anchor.
func partition(messages []agentgo.AgentMessage, recentTokens int) []segment {
	start := activeStart(messages, recentTokens)
	// A continued Lane can append another task. Its latest anchor supersedes
	// earlier task anchors; retaining every completed hypothesis forever would
	// turn the fixed zone into another unbounded history.
	taskAnchor := -1
	for i, message := range messages {
		if _, ok := message.(FixedContext); ok {
			taskAnchor = i
		}
	}
	var segments []segment
	for i, message := range messages {
		kind := history
		if i >= start {
			kind = active
		} else if message.GetRole() == agentgo.RoleSystem {
			kind = fixed
		} else if keeper, ok := message.(FixedContext); ok && i == taskAnchor {
			kind, message = fixed, keeper.FixedContext()
		}
		if len(segments) == 0 || segments[len(segments)-1].zone != kind {
			segments = append(segments, segment{zone: kind})
		}
		last := &segments[len(segments)-1]
		last.messages = append(last.messages, message)
	}
	return segments
}

func activeStart(messages []agentgo.AgentMessage, budget int) int {
	// An assistant response and everything up to the next assistant response is
	// one round. This includes all parallel tool results and subsequent steering.
	var rounds []int
	for i, message := range messages {
		if message.GetRole() == agentgo.RoleAssistant {
			rounds = append(rounds, i)
		}
	}
	if len(rounds) == 0 {
		// Initial source messages may compact before the first model call.
		return len(messages)
	}
	start := rounds[len(rounds)-1]
	used := agentcontext.EstimateTotal(messages[start:])
	for i := len(rounds) - 2; i >= 0; i-- {
		tokens := agentcontext.EstimateTotal(messages[rounds[i]:start])
		if used+tokens > budget {
			break
		}
		start, used = rounds[i], used+tokens
	}
	return start
}

func flatten(segments []segment) []agentgo.AgentMessage {
	var messages []agentgo.AgentMessage
	for _, part := range segments {
		messages = append(messages, part.messages...)
	}
	return messages
}

// validateTools rejects orphaned results and incomplete historical rounds.
// A pending call at the very end is allowed for explicit/manual compaction.
func validateTools(messages []agentgo.AgentMessage) error {
	pending := make(map[string]bool)
	for _, message := range messages {
		wire, include := message.ToMessage()
		if !include {
			continue
		}
		if wire.Role == agentgo.RoleAssistant {
			if len(pending) != 0 {
				return fmt.Errorf("context has an incomplete tool round")
			}
			for _, call := range wire.ToolCalls() {
				pending[call.ID] = true
			}
		}
		if wire.Role == agentgo.RoleTool {
			id, _ := wire.Metadata["tool_call_id"].(string)
			if !pending[id] {
				return fmt.Errorf("context has an orphaned tool result")
			}
			delete(pending, id)
		}
	}
	return nil
}

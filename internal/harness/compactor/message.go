// Derived from compforge/agentgo context compactors (Apache-2.0).
// CCR owns history-only policy; ZoneCompactor owns all suffix protection.
package compactor

import (
	"context"
	"sort"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

// MessageCompactor asks application messages for progressively smaller
// semantic representations before generic trimming or summarization discards
// structure the application understands better.
type MessageCompactor struct{}

func NewMessageCompactor() *MessageCompactor {
	return &MessageCompactor{}
}

func (s *MessageCompactor) Compact(
	_ context.Context,
	input agentgo.TransformContext,
	expect float64,
) ([]agentgo.AgentMessage, error) {
	messages := input.Messages
	if len(messages) == 0 || expect >= 1 {
		return messages, nil
	}

	out := copyMessages(messages)
	tokens := agentcontext.EstimateTotal(out)
	target := int(float64(tokens) * clampRatio(expect))

	// Low priority goes first; ties use chronological order to preserve newer evidence.
	for _, priority := range compactionPriorities(out) {
		for i := 0; i < len(out) && tokens > target; i++ {
			message := out[i]
			if message.Priority() != priority {
				continue
			}

			before := agentcontext.EstimateTokens(message)
			rawTokens := agentcontext.EstimateTokens(message.Raw())
			if before <= 0 || rawTokens <= 0 {
				continue
			}

			deficit := tokens - target
			desiredTokens := max(0, before-deficit)
			messageExpect := float64(desiredTokens) / float64(rawTokens)
			next, _ := message.Compact(clampRatio(messageExpect))
			if next == nil {
				continue
			}

			after := agentcontext.EstimateTokens(next)
			if after >= before {
				continue
			}
			out[i] = next
			tokens -= before - after
		}
		if tokens <= target {
			break
		}
	}

	return out, nil
}

func compactionPriorities(messages []agentgo.AgentMessage) []int {
	seen := make(map[int]struct{})
	for _, message := range messages {
		seen[message.Priority()] = struct{}{}
	}
	priorities := make([]int, 0, len(seen))
	for priority := range seen {
		priorities = append(priorities, priority)
	}
	sort.Ints(priorities)
	return priorities
}

func clampRatio(ratio float64) float64 {
	return min(1, max(0, ratio))
}

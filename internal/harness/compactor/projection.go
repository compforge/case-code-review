// Derived from compforge/agentgo context compactors (Apache-2.0).
// CCR owns history-only policy; ZoneCompactor owns all suffix protection.
package compactor

import (
	"maps"
	"time"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

// projectedMessage keeps a generic model-level projection beside the original
// domain message. Context policies can therefore trim ordinary Message values
// without destroying data needed by later compaction or persistence.
type projectedMessage struct {
	raw     agentgo.AgentMessage
	current agentgo.AgentMessage
}

func newProjectedMessage(raw agentgo.AgentMessage, current agentgo.AgentMessage) agentgo.AgentMessage {
	return projectedMessage{raw: raw.Raw(), current: current}
}

func (m projectedMessage) GetRole() agentgo.Role              { return m.current.GetRole() }
func (m projectedMessage) GetTimestamp() time.Time            { return m.current.GetTimestamp() }
func (m projectedMessage) Raw() agentgo.AgentMessage          { return m.raw }
func (m projectedMessage) Priority() int                      { return m.raw.Priority() }
func (m projectedMessage) TextContent() string                { return m.current.TextContent() }
func (m projectedMessage) ThinkingContent() string            { return m.current.ThinkingContent() }
func (m projectedMessage) HasToolCalls() bool                 { return m.current.HasToolCalls() }
func (m projectedMessage) ToMessage() (agentgo.Message, bool) { return m.current.ToMessage() }
func (m projectedMessage) Compact(expect float64) (agentgo.AgentMessage, float64) {
	return m.raw.Compact(expect)
}
func (m projectedMessage) ContextItems() []agentgo.ContextItem {
	provider, ok := m.current.(agentgo.ContextItemProvider)
	if !ok {
		return nil
	}
	return provider.ContextItems()
}
func (m projectedMessage) ContextDemands() []agentgo.ContextDemand {
	provider, ok := m.current.(agentgo.ContextDemandProvider)
	if !ok {
		return nil
	}
	return provider.ContextDemands()
}

// rawSummaryView restores per-message domain evidence before a checkpoint is
// generated. Existing summaries stay summarized so incremental compaction does
// not replay an unbounded history into the model.
func rawSummaryView(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
	out := make([]agentgo.AgentMessage, len(messages))
	for i, message := range messages {
		if _, ok := message.(agentcontext.ContextSummary); ok {
			out[i] = message
			continue
		}
		out[i] = message.Raw()
	}
	return out
}

func sourceMessages(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
	var out []agentgo.AgentMessage
	for _, message := range messages {
		if summary, ok := message.(agentcontext.ContextSummary); ok && len(summary.RawMessages) > 0 {
			out = append(out, summary.RawMessages...)
			continue
		}
		out = append(out, message.Raw())
	}
	return out
}

func copyMessages(messages []agentgo.AgentMessage) []agentgo.AgentMessage {
	return append([]agentgo.AgentMessage(nil), messages...)
}
func cloneMetadata(metadata map[string]any) map[string]any { return maps.Clone(metadata) }

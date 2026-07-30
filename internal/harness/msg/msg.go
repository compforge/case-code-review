// Package msg is ccr's review-domain message model: the review loop's
// conversation is a []Msg of DOMAIN messages (what the content IS — a unit's
// briefing, a file's source, a board note), and the LLM wire format
// (llm.Message's user/assistant/tool roles) appears only at the Lower
// boundary, immediately before an API call.
//
// Why a domain layer (see docs/message-model.md): wire roles erase identity.
// Once a file's content is flattened into a user-role string, nothing can
// tell it apart from instructions — so it can't be deduplicated against a
// later file_read, evicted by staleness when the context tightens, or
// re-rendered for a provider that prefers tool_result form. Typed messages
// keep that identity until the last moment; rendering decisions become
// per-type policy in one place instead of assembly-time string concatenation.
//
// Evolution is incremental, pi-style (passthrough first): Raw wraps an
// llm.Message unchanged, so swapping the loop's currency is byte-identical
// on the wire; typed messages (file / board / bulletin …) are introduced one
// consumer at a time.
//
// Invariant: lowering is 1:1 — one Msg lowers to exactly one llm.Message.
// The compression engine partitions the conversation by message INDEX
// (frozen/compress/active zones, assistant+tool rounds); a 1:N or dropping
// lowering would silently misalign those zones. A future type that needs to
// vanish from context must do so by being REMOVED from the []Msg (eviction),
// not by lowering to nothing.
package msg

import "github.com/qiankunli/case-code-review/internal/llm"

// Msg is one review-domain message in a loop's conversation.
type Msg interface {
	// Lower renders the message into its LLM wire form (exactly one message —
	// see the package invariant).
	Lower() llm.Message
}

// Reclaimable is a message whose content can be RE-DERIVED — a file re-read, a
// board re-pull — so under token pressure it may be shed (Reclaim) before the
// loop pays for LLM summarization, which is neither free nor lossless. Eviction
// is idempotent. File and Board implement it; the compression engine sheds
// reclaimables oldest-first before summarizing (see evictReclaimable).
type Reclaimable interface {
	Msg
	Reclaim() // elide content, keeping a one-line pointer
	Reclaimed() bool
}

// Raw is the passthrough type: an llm.Message carried as-is (task prompts,
// assistant turns, tool results, wrap-up nudges). It keeps the currency swap
// byte-identical and remains the right type for anything that is genuinely
// wire-shaped rather than domain-shaped.
type Raw struct {
	M llm.Message
}

func (r Raw) Lower() llm.Message { return r.M }

// Text is shorthand for a Raw text message — the loop's steering nudges
// ("call task_done", wrap-up) are wire-shaped user/assistant text by nature.
func Text(role, content string) Msg {
	return Raw{M: llm.NewTextMessage(role, content)}
}

// Wrap lifts wire messages into the domain as Raw passthroughs.
func Wrap(msgs []llm.Message) []Msg {
	out := make([]Msg, len(msgs))
	for i, m := range msgs {
		out[i] = Raw{M: m}
	}
	return out
}

// Lower renders a conversation for an API call. len(out) == len(msgs), in
// order (the package invariant), so index-based reasoning done on []Msg
// (compression zones, rounds) holds on the wire form too.
func Lower(msgs []Msg) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m.Lower()
	}
	return out
}

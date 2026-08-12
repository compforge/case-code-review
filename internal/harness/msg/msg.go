// Package msg is ccr's review-domain message model: the review loop's
// conversation is a []Msg of DOMAIN messages (what the content IS — a unit's
// initial context, a file's source, a board note), and the LLM wire format
// (llm.Message's user/assistant/tool roles) appears only at the Lower
// boundary, immediately before an API call.
//
// Why a domain layer (see docs/harness.md): wire roles erase identity.
// Once a file's content is flattened into a user-role string, nothing can
// tell it apart from instructions — so it can't be deduplicated against a
// later read_files, evicted by staleness when the context tightens, or
// re-rendered for a provider that prefers tool_result form. Typed messages
// keep that identity until the last moment; rendering decisions become
// per-type policy in one place instead of assembly-time string concatenation.
//
// Evolution is incremental, pi-style (passthrough first): Raw wraps an
// llm.Message unchanged, so swapping the loop's currency is byte-identical
// on the wire; typed messages (file / board / bulletin …) are introduced one
// consumer at a time.
//
// Harness adapts each Msg to AgentGo AgentMessage and preserves a 1:1 model
// projection. AgentGo may ask that wrapper for a smaller representation, but
// the domain value keeps its raw form for later compaction decisions.
package msg

import (
	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/llm"
)

// Msg is one review-domain message in a loop's conversation.
type Msg interface {
	// ToLLM renders the message's current projection into exactly one LLM wire
	// message. Representation selection belongs to Compact, not the renderer.
	ToLLM() llm.Message
	// Compact returns the highest-fidelity immutable projection that satisfies
	// expect, measured against this message's full representation. The ratio is
	// only a budget target: each domain message owns its semantic trade-offs.
	Compact(expect float64) (Msg, float64)
	// Priority is execution-local information importance. Higher-priority
	// messages are compacted after lower-priority messages.
	Priority() int
}

// ContextItemProvider lets a domain message expose structured admission facts
// through AgentGo's shared protocol without changing its LLM rendering.
type ContextItemProvider interface {
	Msg
	ContextItems() []agentgo.ContextItem
}

// ToolResultMsg preserves the tool identity needed to reconstruct typed
// results after AgentGo or a context strategy temporarily lowers them.
type ToolResultMsg interface {
	Msg
	ToolName() string
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

func (r Raw) ToLLM() llm.Message             { return r.M }
func (r Raw) Compact(float64) (Msg, float64) { return r, 1 }
func (r Raw) Priority() int                  { return 0 }

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
		out[i] = m.ToLLM()
	}
	return out
}

// CloneAll copies a conversation without sharing mutable typed messages.
// Context projection may stub File or Board values, while the runtime
// transcript must remain unchanged until a rewrite is explicitly committed.
func CloneAll(msgs []Msg) []Msg {
	out := make([]Msg, len(msgs))
	for i, m := range msgs {
		switch value := m.(type) {
		case *File:
			cp := *value
			out[i] = &cp
		case *FileBatch:
			out[i] = value.clone()
		case *FileContext:
			out[i] = value.clone()
		case *SearchBatch:
			out[i] = value.clone()
		case *SearchResult:
			cp := *value
			cp.Paths = append([]string(nil), value.Paths...)
			if value.SearchedFiles != nil {
				count := *value.SearchedFiles
				cp.SearchedFiles = &count
			}
			out[i] = &cp
		case *Diff:
			cp := *value
			cp.Paths = append([]string(nil), value.Paths...)
			out[i] = &cp
		case *Board:
			cp := *value
			out[i] = &cp
		case Raw:
			out[i] = Raw{M: value.M}
		default:
			out[i] = value
		}
	}
	return out
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

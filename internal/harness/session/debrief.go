package session

import (
	"fmt"
	"time"

	"github.com/qiankunli/case-code-review/internal/stdout"
)

// Debrief is a unit's terminal record — one line per review Unit written when
// its loop ends. Briefing goes in, debrief comes out: it captures what the run
// knew at that moment and what post-hoc analysis cannot reconstruct (outcome
// vs policy skip, unit formation, briefing degradations), plus a per-unit cost
// rollup so eval never re-aggregates raw llm_response records.
//
// Field groups mirror the metric dashboards (eval/README.md): outcome →
// robustness, formed → granularity, clues/materials/usage → context, the
// aggregated tail → cost.
type Debrief struct {
	// Outcome is the loop's terminal state: llmloop's completed / truncated /
	// timeout / llm_error, or "skipped_policy" when a governor (token guard)
	// decided not to run the loop at all — a deliberate skip, not a failure.
	Outcome string
	// Reason qualifies non-completed outcomes (truncation cause, error, policy).
	Reason string

	// Formed is unit.Formation: why the unit has its shape (func/file/coalesce/
	// chain). Distinguishes the cost governor's coalesced file units from
	// naturally whole-file ones — the granularity dashboard's too-coarse signal.
	Formed     string
	Fragments  int
	Insertions int64
	Deletions  int64

	// Degradations lists briefing content dropped after assembly (token guard:
	// related bodies first, then own source). Budget misses during assembly are
	// per-material entries in Materials instead.
	Degradations []string
	// Clues is the dossier tally on the relation×kind matrix ("caller/spec" -> n).
	Clues map[string]int
	// ClueRefs are the deduped symbol-ids the dossier points at — kept because
	// coverage COUNTS can stay flat while the pointed-at symbols all change
	// (the typed-graph lesson: compare content, not counts).
	ClueRefs []string
	// Materials records each briefing material's fate: "whole <path>",
	// "ranged <path>", "budget_miss <path>", "dropped <label>".
	Materials []string
	// UsageSites is how many pre-grepped use sites the briefing carried.
	UsageSites int

	// Review Team board activity (docs/cross-unit.md), 0 when the board is off.
	BoardPulled         int // peer bulletins injected into this unit's loop
	BoardInjectedTokens int // their rough token cost
	BoardPosted         int // facts this unit published for peers

	// Cost rollup — filled by WriteDebrief from the scope's task records.
	Rounds     map[string]int // task type -> LLM rounds
	ToolCalls  map[string]int // tool name -> calls
	Tokens     TokenUsage
	DurationMs int64
}

// CloseScope declares a scope's work DONE and hands over its debrief — the
// explicit end of the unit lifecycle (open → closing → closed; see
// docs/unit-model.md 关键设计 8). The debrief persists immediately when no
// async work is in flight, or parks until the scope's last async task ends
// (EndAsync finalizes). The caller supplies only what it alone knows (outcome,
// briefing fate); cost is aggregated at finalize so it can't drift from the
// llm_request/llm_response records it summarizes.
func (sh *SessionHistory) CloseScope(sc Scope, d Debrief) {
	sh.GetOrCreateScope(sc).Close(d)
}

// Close implements CloseScope on the scope itself. Idempotent: a second Close
// keeps the first debrief (and warns — two owners closing one scope is a bug).
func (ss *ScopeSession) Close(d Debrief) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.state != scopeOpen {
		fmt.Fprintf(stdout.Err(), "[ccr session] warning: scope %q closed twice; keeping the first debrief\n", ss.ID)
		return
	}
	if ss.pendingAsync > 0 {
		ss.state = scopeClosing
		ss.parkedDebrief = &d
		return
	}
	ss.finalizeLocked(d)
}

// BeginAsync registers one in-flight async task (e.g. a comment worker that
// may append relocation records to this scope). Called BEFORE the task is
// submitted, from the loop that owns the scope — so a Close racing the task
// always sees the pending count.
func (ss *ScopeSession) BeginAsync() {
	ss.mu.Lock()
	ss.pendingAsync++
	ss.mu.Unlock()
}

// EndAsync retires one in-flight async task; the last one out finalizes a
// closing scope. This is what makes the debrief's cost rollup complete AND
// race-free: nothing mutates the scope's records once the loop has Closed and
// the async count hits zero.
func (ss *ScopeSession) EndAsync() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.pendingAsync > 0 {
		ss.pendingAsync--
	}
	if ss.state == scopeClosing && ss.pendingAsync == 0 {
		d := *ss.parkedDebrief
		ss.parkedDebrief = nil
		ss.finalizeLocked(d)
	}
}

// finalizeLocked aggregates the scope's cost and persists the debrief.
// Callers hold ss.mu.
func (ss *ScopeSession) finalizeLocked(d Debrief) {
	ss.state = scopeClosed
	d.Rounds, d.ToolCalls, d.Tokens, d.DurationMs = ss.aggregateLocked()
	if p := ss.session.persist; p != nil {
		p.WriteDebrief(ss, d)
	}
}

// aggregateLocked sums this scope's recorded LLM traffic: rounds and duration
// per task type, tool-call counts, and token usage (cache split kept — real
// cost differs by an order of magnitude). Callers hold ss.mu.
//
// Concurrency: individual records' fields (Response, Duration) are written by
// SetResponse/SetError without ss.mu; safety here comes from the LIFECYCLE —
// this only runs once the owning loop has Closed the scope and its last async
// task has ended, so nothing is mutating the records anymore.
func (ss *ScopeSession) aggregateLocked() (rounds map[string]int, toolCalls map[string]int, tokens TokenUsage, durationMs int64) {
	rounds = make(map[string]int, len(ss.TaskRecords))
	toolCalls = map[string]int{}
	var dur time.Duration
	for taskType, recs := range ss.TaskRecords {
		rounds[string(taskType)] = len(recs)
		for _, r := range recs {
			dur += r.Duration
			if r.Response == nil {
				continue
			}
			for _, tc := range r.Response.ToolCalls {
				toolCalls[tc.Function.Name]++
			}
			if u := r.Response.Usage; u != nil {
				tokens.PromptTokens += u.PromptTokens
				tokens.CompletionTokens += u.CompletionTokens
				tokens.CacheReadTokens += u.CacheReadTokens
				tokens.CacheWriteTokens += u.CacheWriteTokens
			}
		}
	}
	return rounds, toolCalls, tokens, dur.Milliseconds()
}

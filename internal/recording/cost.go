package recording

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// Cost is a projection of recorded model calls, including unfinished calls.
// Reported and estimated usage are kept distinguishable. Missing usage is a
// gap, never evidence that a failed or interrupted request was free.
type Cost struct {
	Tokens         TokenUsage `json:"tokens"`
	Calls          int        `json:"calls"`
	Errors         int        `json:"errors"`
	Pending        int        `json:"pending"`
	UnknownUsage   int        `json:"unknown_usage"`
	EstimatedUsage int        `json:"estimated_usage"`
}

func (c *Cost) add(other Cost) {
	c.Tokens.PromptTokens += other.Tokens.PromptTokens
	c.Tokens.CompletionTokens += other.Tokens.CompletionTokens
	c.Tokens.ReasoningTokens += other.Tokens.ReasoningTokens
	c.Tokens.CacheReadTokens += other.Tokens.CacheReadTokens
	c.Tokens.CacheWriteTokens += other.Tokens.CacheWriteTokens
	c.Calls += other.Calls
	c.Errors += other.Errors
	c.Pending += other.Pending
	c.UnknownUsage += other.UnknownUsage
	c.EstimatedUsage += other.EstimatedUsage
}

type OperationCost struct {
	Operation  operation.Record `json:"operation"`
	DurationMS float64          `json:"duration_ms"`
	SelfMS     float64          `json:"self_ms"`
	Finished   bool             `json:"finished"`
	Cost       Cost             `json:"cost"` // includes descendant calls; do not sum parent and child rows
}

type CostReport struct {
	Total           Cost            `json:"total"`
	Unattributed    Cost            `json:"unattributed"`
	Operations      []OperationCost `json:"operations"`
	ObservedThrough time.Time       `json:"observed_through"`
}

// Costs associates each request with its native timeline ancestry. Durations are
// intervals; self time excludes the union of children, not their summed lengths.
// Running intervals end at the last recorded observation and are lower bounds.
//
// +spec=`Each model request contributes once to run cost and once to each ancestor; debrief existence never controls accounting`
func (t *Transcript) Costs() CostReport {
	report := CostReport{}
	calls := map[string]map[string]any{}
	for i, r := range t.Records {
		if stamp, err := time.Parse(time.RFC3339Nano, stringField(r, "timestamp")); err == nil && stamp.After(report.ObservedThrough) {
			report.ObservedThrough = stamp
		}
		kind := stringField(r, "type")
		if kind != "llm_request" && kind != "llm_response" && kind != "llm_error" {
			continue
		}
		id := CallID(r)
		if id == "" {
			id = stringField(r, "uuid")
			if id == "" {
				id = time.Unix(0, int64(i)).String()
			}
		}
		// A terminal record may arrive before its stage's final flush.
		if prev, ok := calls[id]; !ok || kind != "llm_request" || prev["type"] == "llm_request" {
			calls[id] = r
		}
	}
	stages := t.OperationIndex()
	for _, s := range stages {
		for _, at := range []time.Time{s.StartedAt, s.FinishedAt} {
			if at.After(report.ObservedThrough) {
				report.ObservedThrough = at
			}
		}
	}
	rollup := map[operation.ID]Cost{}
	for _, r := range calls {
		cost := Cost{Calls: 1}
		switch r["type"] {
		case "llm_request":
			cost.Pending = 1
		case "llm_error":
			cost.Errors = 1
		}
		if usage, ok := r["usage"].(map[string]any); ok {
			raw, _ := json.Marshal(usage)
			_ = json.Unmarshal(raw, &cost.Tokens)
			if r["usage_source"] == "estimated" {
				cost.EstimatedUsage = 1
			}
		} else {
			cost.UnknownUsage = 1
		}
		report.Total.add(cost)
		id := operation.ID(stringField(r, "operation_id"))
		if _, ok := stages[id]; !ok {
			report.Unattributed.add(cost)
			continue
		}
		visited := map[operation.ID]bool{}
		for id != "" && !visited[id] {
			s, ok := stages[id]
			if !ok {
				break
			}
			visited[id] = true
			value := rollup[id]
			value.add(cost)
			rollup[id] = value
			id = s.ParentID
		}
	}
	children := map[operation.ID][]operation.Record{}
	for _, s := range stages {
		children[s.ParentID] = append(children[s.ParentID], s)
	}
	for id, s := range stages {
		end := s.FinishedAt
		if end.IsZero() {
			end = report.ObservedThrough
		}
		duration := max(0, end.Sub(s.StartedAt).Seconds()*1000)
		var intervals [][2]time.Time
		for _, child := range children[id] {
			a, b := child.StartedAt, child.FinishedAt
			if b.IsZero() || b.After(end) {
				b = end
			}
			if a.Before(s.StartedAt) {
				a = s.StartedAt
			}
			if b.After(a) {
				intervals = append(intervals, [2]time.Time{a, b})
			}
		}
		sort.Slice(intervals, func(i, j int) bool { return intervals[i][0].Before(intervals[j][0]) })
		covered := 0.0
		cursor := s.StartedAt
		for _, span := range intervals {
			a, b := span[0], span[1]
			if a.Before(cursor) {
				a = cursor
			}
			if b.After(a) {
				covered += b.Sub(a).Seconds() * 1000
			}
			if b.After(cursor) {
				cursor = b
			}
		}
		report.Operations = append(report.Operations, OperationCost{Operation: s, DurationMS: duration, SelfMS: max(0, duration-covered), Finished: !s.FinishedAt.IsZero(), Cost: rollup[id]})
	}
	sort.Slice(report.Operations, func(i, j int) bool {
		a, b := report.Operations[i].Operation, report.Operations[j].Operation
		if a.StartedAt.Equal(b.StartedAt) {
			return a.ID < b.ID
		}
		return a.StartedAt.Before(b.StartedAt)
	})
	return report
}

func stringField(r map[string]any, key string) string { s, _ := r[key].(string); return s }

// IncompleteReasons describes recording evidence, not the review's verdict.
func (t *Transcript) IncompleteReasons() []string {
	reasons := append([]string(nil), t.Gaps...)
	if t.TruncatedTail {
		reasons = append(reasons, "truncated final record")
	}
	closed := false
	for _, r := range t.Records {
		if r["type"] == "artifact" && r["artifact_kind"] == "recording_gap" {
			reasons = append(reasons, "local Session recording failed")
		}
		if r["type"] == "session_end" {
			closed = true
		}
	}
	if !closed {
		reasons = append(reasons, "missing session_end")
	}
	for _, s := range t.Journal.Operations {
		if s.FinishedAt.IsZero() {
			reasons = append(reasons, "unfinished timeline stages")
			break
		}
	}
	return reasons
}

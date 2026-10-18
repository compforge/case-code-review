package recording

import (
	"encoding/json"
	"slices"
	"sort"
)

// UnitResult is the owning pipeline's terminal judgment of its review coverage.
// Findings are recorded independently and remain useful when coverage is partial.
type UnitResult struct {
	ID         string   `json:"id"`
	Paths      []string `json:"paths,omitempty"`
	Outcome    string   `json:"outcome"`
	Reason     string   `json:"reason,omitempty"`
	Unassessed []string `json:"unassessed_hypotheses,omitempty"`
}

// Completion is shared by CLI, Timeline and offline readers. Warnings explain
// failures; their names and multiplicity never determine review completeness.
// +spec=`A review is complete only after finalization and every admitted Unit completed; zero Findings alone never means clean`
// +link=component://docs/observability.md
type Completion struct {
	Status string       `json:"status"` // running, completed, partial, failed, unknown
	Units  []UnitResult `json:"units,omitempty"`
	Error  string       `json:"error,omitempty"`
}

func (c Completion) Complete() bool { return c.Status == "completed" }

func (sh *Review) Completion() Completion {
	if sh == nil {
		return Completion{Status: "unknown"}
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ended, failure := !sh.EndTime.IsZero(), sh.operationError
	result := Completion{Status: "running", Error: failure}
	incomplete := false
	for _, scope := range sh.Scopes {
		scope.mu.Lock()
		if scope.Kind == "unit" {
			unit := UnitResult{ID: scope.ID, Paths: slices.Clone(scope.Paths), Outcome: scope.outcome, Reason: scope.reason, Unassessed: slices.Clone(scope.unassessed)}
			if unit.Outcome == "" {
				unit.Outcome = "incomplete"
			}
			if unit.Outcome != "completed" || len(unit.Unassessed) > 0 {
				incomplete = true
			}
			result.Units = append(result.Units, unit)
		}
		scope.mu.Unlock()
	}
	sort.Slice(result.Units, func(i, j int) bool { return result.Units[i].ID < result.Units[j].ID })
	if ended {
		switch {
		case failure != "":
			result.Status = "failed"
		case incomplete:
			result.Status = "partial"
		default:
			result.Status = "completed"
		}
	}
	return result
}

// Completion returns the final business result as recorded by the pipeline.
// An absent terminal record is incomplete evidence, never inferred success.
func (t *Transcript) Completion() Completion {
	for i := len(t.Records) - 1; i >= 0; i-- {
		record := t.Records[i]
		if record["type"] != "session_end" {
			continue
		}
		var result Completion
		raw, err := json.Marshal(record["completion"])
		if err == nil && json.Unmarshal(raw, &result) == nil && result.Status != "" {
			return result
		}
		return Completion{Status: "unknown"}
	}
	return Completion{Status: "running"}
}

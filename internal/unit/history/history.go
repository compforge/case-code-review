// Package history feeds a previous review's findings back into the next review
// as per-unit context, so the reviewer can judge whether the current change
// addresses what an earlier round flagged. It is the review-history counterpart
// of spec/rule — another symbol-id-keyed input, which ccr can express because it
// treats the unit as a first-class concept.
package history

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/qiankunli/case-code-review/internal/unit"
)

// Finding is one prior-review finding bound to a unit, supplied by the caller
// (e.g. devloop, from its review history) — already filtered to trustworthy
// (successfully-reviewed) findings.
type Finding struct {
	Msg string `json:"msg"`
	Sha string `json:"sha,omitempty"`
}

// Index maps a symbol-id to the findings a previous review raised on it.
type Index map[string][]Finding

// Load reads a --history JSON file (symbol-id -> []Finding). An empty path (or an
// empty file) yields nil — no history, finders no-op. A malformed file is an
// error for the caller to surface.
func Load(path string) (Index, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// Finder attaches each unit's prior findings as a ClueHistory, framed so the
// reviewer adjudicates whether the change addresses them. Cheap (a map lookup),
// so it runs unconditionally — no call-graph walk, no budget gate.
type Finder struct {
	Index Index
}

func (f Finder) Find(u unit.Unit) []unit.Clue {
	if f.Index == nil {
		return nil
	}
	var clues []unit.Clue
	for _, sym := range u.AllSymbols() {
		fs := f.Index[sym]
		if len(fs) == 0 {
			continue
		}
		clues = append(clues, unit.Clue{
			Kind:     unit.ClueHistory,
			Relation: unit.RelSelf,
			Text:     render(sym, fs),
			Ref:      sym,
		})
	}
	return clues
}

// render frames the prior findings as an adjudication task for the reviewer:
// confirm fixes, re-raise survivors, don't re-flag what's resolved.
func render(symbolID string, fs []Finding) string {
	var b strings.Builder
	b.WriteString("A previous review flagged " + symbolID +
		" — for each, check whether the current code addresses it: if fixed, say so briefly; if still present, re-raise it; do not re-flag what's resolved.\n")
	for _, fnd := range fs {
		b.WriteString("- " + fnd.Msg)
		if fnd.Sha != "" {
			b.WriteString(" (at " + fnd.Sha + ")")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Package history feeds a previous review's findings back into the next review
// as per-unit context, so the reviewer can judge whether the current change
// addresses what an earlier round flagged. It is the review-history counterpart
// of spec/rule — another unit-keyed input, which ccr can express because it
// treats the unit as a first-class concept. Symbol IDs are preferred; file paths
// are the fallback for callers that only have forge comment anchors.
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
	Msg         string `json:"msg"`
	Sha         string `json:"sha,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CommentID   string `json:"comment_id,omitempty"`
	URL         string `json:"url,omitempty"`
}

// Index maps a symbol-id or repo-relative path to prior findings.
type Index map[string][]Finding

// Load reads a --history JSON file (symbol-id/path -> []Finding). An empty path
// (or an empty file) yields nil — no history, finders no-op. A malformed file is
// an error for the caller to surface.
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
	seen := make(map[string]bool)
	keys := append(u.AllSymbols(), u.Paths()...)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		fs := f.Index[key]
		if len(fs) == 0 {
			continue
		}
		clues = append(clues, unit.Clue{
			Kind:     unit.ClueHistory,
			Relation: unit.RelSelf,
			Text:     render(key, fs),
			Ref:      key,
		})
	}
	return clues
}

// render frames Forge history as prior delivery, not as a backlog to re-raise.
// Whether the old issue remains is useful evidence, but a durable MR thread is
// already its delivery channel; a new Finding would only create repetition.
func render(symbolID string, fs []Finding) string {
	var b strings.Builder
	b.WriteString("PRIOR DELIVERY for " + symbolID +
		": these issues already have a durable comment on this MR. Use them to understand the current revision, but do not create a new Hypothesis for the same underlying issue, whether it is fixed or still present. Only report a distinct regression with a different trigger or impact.\n")
	for _, fnd := range fs {
		b.WriteString("- " + fnd.Msg)
		if fnd.Sha != "" {
			b.WriteString(" (at " + fnd.Sha + ")")
		}
		if fnd.Fingerprint != "" {
			b.WriteString(" [fingerprint=" + fnd.Fingerprint + "]")
		}
		if fnd.CommentID != "" {
			b.WriteString(" [comment=" + fnd.CommentID + "]")
		}
		if fnd.URL != "" {
			b.WriteString(" [url=" + fnd.URL + "]")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

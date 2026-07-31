package scan

import (
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// Item represents a single file enumerated by full-scan mode. Unlike
// change.Change (which carries a unified diff text), Item carries the
// entire file content because scan reviews whole files with no diff
// context.
type Item struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	IsBinary  bool   `json:"is_binary,omitempty"`
	LineCount int    `json:"line_count,omitempty"`
}

// AsChange maps the full file into the common Unit input shape.
func (s *Item) AsChange() *change.Change {
	if s == nil {
		return nil
	}
	return &change.Change{
		OldPath:        s.Path,
		NewPath:        s.Path,
		NewFileContent: s.Content,
		IsBinary:       s.IsBinary,
		Insertions:     int64(s.LineCount),
	}
}

// AsUnit forms scan's review currency: one whole-file Unit. Scan contributes
// no diff hunks or extra clues yet, but it enters the same Harness boundary as
// diff-derived Units instead of inventing a second per-file execution object.
func (s Item) AsUnit() unit.Unit {
	return unit.Unit{
		ID:     s.Path,
		Scope:  unit.ScopeFile,
		Formed: unit.FormedFile,
		Fragments: []unit.Fragment{{
			Path:       s.Path,
			Diff:       s.Content,
			Insertions: int64(s.LineCount),
		}},
	}
}

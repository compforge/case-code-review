package codegraph

import (
	"strconv"
	"strings"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/language"
)

// Usage is one repository reference to a changed symbol outside the reviewed
// files — a raw `git grep` hit, kept as text (not resolved to an enclosing
// function): the briefing shows it as a blast-radius map, and session traces
// show impact scans ("who else uses this name") are the review loop's largest
// remaining tool expense.
type Usage struct {
	Symbol string // the symbol-id whose name matched
	File   string
	Line   int
	Text   string // the matching line, trimmed
}

const (
	usagePerSymbolMax = 8
	usageTotalMax     = 40
)

// FindUsages greps the repo for word matches of each changed symbol's bare name
// and returns the hits outside excludePaths (the unit's own files — their
// internal uses are already visible in the inlined source). Same cost class as
// the caller/callee walk (one `git grep` per symbol), so callers must apply the
// same costly-context budget gate. Bounded per symbol and in total; degrades to
// nil on any miss.
func FindUsages(repoDir string, runner *gitcmd.Runner, symbolIDs []string, excludePaths map[string]bool) []Usage {
	if repoDir == "" {
		return nil
	}
	var out []Usage
	// Dedup is per symbol (key carries the id): the map is rendered grouped by
	// symbol, and a line referencing two changed symbols is a hit for each.
	seen := map[string]bool{}
	for _, id := range symbolIDs {
		name := language.BareSymbolName(id)
		if name == "" {
			continue
		}
		// Unexported Go symbols can only be referenced from their own package —
		// scope the grep there (same soundness argument as caller resolution).
		path, _, _ := language.SplitSymbolID(id)
		scope := language.ReferenceScope(path, name)
		perSym := 0
		// Over-fetch: exclusions (own files, dupes) eat into the raw hit list.
		for _, h := range grepCode(repoDir, runner, []string{"-F", "-w", "-e", name}, usagePerSymbolMax*4, scope) {
			if excludePaths[h.file] || language.IsCommentLine(h.file, h.text) {
				continue
			}
			key := id + "\x00" + h.file + ":" + strconv.Itoa(h.line)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Usage{Symbol: id, File: h.file, Line: h.line, Text: strings.TrimSpace(h.text)})
			perSym++
			if perSym >= usagePerSymbolMax || len(out) >= usageTotalMax {
				break
			}
		}
		if len(out) >= usageTotalMax {
			break
		}
	}
	return out
}

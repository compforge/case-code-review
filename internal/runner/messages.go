package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/codegraph"
)

// preloadSourceBudget caps source injected before one Unit Review. Typical
// units fit whole; a giant file cannot crowd the prompt toward the token guard.
const preloadSourceBudget = 32 * 1024

const (
	sourceNotPreloaded   = "(not preloaded — fetch what you need via file_read)"
	unitSourcePointer    = "(provided as separate messages after this task — do NOT call file_read on those ranges again)"
	relatedSourcePointer = "(provided as separate messages after this task)"
	maxNeighborSources   = 6
)

// preloadReviewFiles reads the high-confidence source already implied by the
// Unit. Own files are filled first; call-adjacent bodies use the remaining
// budget. The returned File messages preserve path/range identity for later
// coverage checks and compaction.
func (a *Runner) preloadReviewFiles(
	ctx context.Context,
	u unit.Unit,
) (own, related []*msg.File, notes, outcomes []string) {
	if a.fileReader() == nil {
		return nil, nil, nil, nil
	}

	budget := preloadSourceBudget
	symbols := symbolsByPath(u)
	for _, filePath := range u.Paths() {
		files, note, outcome := a.preloadPath(
			ctx, filePath, symbols[filePath], true, "code under review", &budget,
		)
		own = append(own, files...)
		if note != "" {
			notes = append(notes, note)
		}
		if outcome != "" {
			outcomes = append(outcomes, outcome)
		}
	}

	for _, clue := range a.relatedSourceClues(u) {
		filePath, _, _ := language.SplitSymbolID(clue.Ref)
		label := "related " + string(clue.Relation) + " " + clue.Ref
		files, _, outcome := a.preloadPath(
			ctx, filePath, []string{clue.Ref}, false, label, &budget,
		)
		related = append(related, files...)
		if outcome != "" {
			outcomes = append(outcomes, outcome)
		}
	}
	return own, related, notes, outcomes
}

func symbolsByPath(u unit.Unit) map[string][]string {
	out := make(map[string][]string)
	for _, fragment := range u.Fragments {
		out[fragment.Path] = append(out[fragment.Path], fragment.Symbols...)
	}
	return out
}

// relatedSourceClues selects a bounded set of already-known call neighbors.
// Only functions outside the Unit are useful as extra source context.
func (a *Runner) relatedSourceClues(u unit.Unit) []unit.Clue {
	if u.Scope != unit.ScopeCallChain || !a.features.Enabled(feature.NeighborSource) {
		return nil
	}
	own := make(map[string]bool)
	for _, filePath := range u.Paths() {
		own[filePath] = true
	}
	seen := make(map[string]bool)
	var out []unit.Clue
	for _, clue := range u.Clues {
		if clue.Relation != unit.RelCaller && clue.Relation != unit.RelCallee {
			continue
		}
		filePath, _, ok := language.SplitSymbolID(clue.Ref)
		if !ok || own[filePath] || seen[clue.Ref] {
			continue
		}
		seen[clue.Ref] = true
		out = append(out, clue)
		if len(out) >= maxNeighborSources {
			break
		}
	}
	return out
}

// preloadPath mirrors file_read's numbered-line format. Own source prefers the
// whole file and falls back to changed function spans; related source always
// carries only the named function bodies.
func (a *Runner) preloadPath(
	ctx context.Context,
	filePath string,
	symbols []string,
	whole bool,
	label string,
	budget *int,
) (files []*msg.File, note, outcome string) {
	content, err := a.fileReader().Read(ctx, filePath)
	if err != nil {
		return nil, "", "unreadable " + filePath
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	if whole && len(content) <= *budget {
		*budget -= len(content)
		var b strings.Builder
		fmt.Fprintf(&b, "File: %s (Total lines: %d)\n", filePath, len(lines))
		for i, line := range lines {
			fmt.Fprintf(&b, "%d|%s\n", i+1, line)
		}
		return []*msg.File{
			msg.NewFile(filePath, 1, len(lines), len(lines), strings.TrimRight(b.String(), "\n")).
				ConfigurePresentation(label, ""),
		}, "", "whole " + filePath
	}

	if len(symbols) > 0 && (!whole || a.features.Enabled(feature.RangedPreload)) {
		files = a.preloadSpans(ctx, filePath, symbols, label, content, lines, budget)
		if len(files) > 0 {
			return files, "", "ranged " + filePath
		}
	}

	if whole {
		return nil, fmt.Sprintf(
			"File: %s — %d bytes exceeds the preload budget; read on demand via file_read",
			filePath, len(content),
		), "budget_miss " + filePath
	}
	return nil, "", "dropped " + label
}

func (a *Runner) preloadSpans(
	ctx context.Context,
	filePath string,
	symbols []string,
	label, content string,
	lines []string,
	budget *int,
) []*msg.File {
	var out []*msg.File
	for _, symbol := range symbols {
		definition, ok := a.sourceAnalyzer().DefinitionByID(
			ctx, language.Source{Path: filePath, Content: content}, symbol,
		)
		start, end := definition.Span.Start, definition.Span.End
		if !ok || start < 1 || end > len(lines) {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "File: %s (Total lines: %d)\nLINE_RANGE: %d-%d\n", filePath, len(lines), start, end)
		for i := start; i <= end; i++ {
			fmt.Fprintf(&b, "%d|%s\n", i, lines[i-1])
		}
		if b.Len() > *budget {
			continue // a smaller later function may still fit
		}
		*budget -= b.Len()
		out = append(out, msg.NewFile(
			filePath, start, end, len(lines), strings.TrimRight(b.String(), "\n"),
		).ConfigurePresentation(label, ""))
	}
	return out
}

// assembleReviewMessages keeps [system, task] as a stable prefix and appends
// each source range as a separate File message. If the assembled prompt is too
// large, related source is removed before the Unit's own source.
func (a *Runner) assembleReviewMessages(
	build func(unitSlot, relatedSlot string) []llm.Message,
	own, related []*msg.File,
	notes []string,
	tokenLimit int,
	deb *session.Debrief,
) []msg.Msg {
	assemble := func(withOwn, withRelated bool) []msg.Msg {
		unitSlot := sourceNotPreloaded
		if withOwn && len(own) > 0 {
			unitSlot = unitSourcePointer
		}
		if len(notes) > 0 {
			unitSlot += "\n" + strings.Join(notes, "\n")
		}
		relatedSlot := ""
		if withRelated && len(related) > 0 {
			relatedSlot = relatedSourcePointer
		}

		out := msg.Wrap(build(unitSlot, relatedSlot))
		if withOwn {
			for _, file := range own {
				out = append(out, file)
			}
		}
		if withRelated {
			for _, file := range related {
				out = append(out, file)
			}
		}
		return out
	}

	over := func(messages []msg.Msg) bool {
		return llm.CountMessagesTokens(msg.Lower(messages)) > tokenLimit
	}
	domain := assemble(true, true)
	if len(related) > 0 && over(domain) {
		domain = assemble(true, false)
		deb.Degradations = append(deb.Degradations, "related_source_dropped")
	}
	if len(own) > 0 && over(domain) {
		domain = assemble(false, false)
		deb.Degradations = append(deb.Degradations, "unit_source_dropped")
	}
	return domain
}

func (a *Runner) describePreloadedSources(u unit.Unit) []string {
	symbols := symbolsByPath(u)
	var out []string
	for _, filePath := range u.Paths() {
		if len(symbols[filePath]) > 0 {
			out = append(out, filePath+" (whole; ranged fallback: "+strings.Join(symbols[filePath], ", ")+")")
		} else {
			out = append(out, filePath+" (whole)")
		}
	}
	for _, clue := range a.relatedSourceClues(u) {
		out = append(out, string(clue.Relation)+" "+clue.Ref+" (body)")
	}
	return out
}

// renderUsageSites pre-greps where else the repo references the unit's changed
// symbols and renders a `path:line: text` blast-radius map for {{usage_sites}},
// plus the site count for the unit's debrief. Same cost class as the
// caller/callee walk, so it honors the same costly-context budget gate
// (a.costlyContext) on top of its own feature gate. Returns "" when gated off
// or nothing was found.
func (a *Runner) renderUsageSites(u unit.Unit) (string, int) {
	if !a.features.Enabled(feature.UsageSites) || !a.costlyContext {
		return "", 0
	}
	symbols := u.AllSymbols()
	if len(symbols) == 0 {
		return "", 0
	}
	exclude := map[string]bool{}
	for _, p := range u.Paths() {
		exclude[p] = true
	}
	usages := codegraph.FindUsages(a.args.RepoDir, a.args.GitRunner, symbols, exclude)
	if len(usages) == 0 {
		return "", 0
	}
	var b strings.Builder
	last := ""
	for _, us := range usages {
		if us.Symbol != last {
			fmt.Fprintf(&b, "`%s`:\n", us.Symbol)
			last = us.Symbol
		}
		fmt.Fprintf(&b, "  %s:%d: %s\n", us.File, us.Line, us.Text)
	}
	return strings.TrimRight(b.String(), "\n"), len(usages)
}

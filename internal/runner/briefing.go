package runner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/codegraph"
)

// The briefing is what a review Unit's loop starts from: the shared user-message
// skeleton (one template for every scope) filled with the Unit's diff, Clues,
// and the source MATERIALS assembled here. Materials exist
// because session traces show the loop's early rounds are otherwise spent
// fetching content ccr already knows the Unit needs (file_read of the reviewed
// file, code_search for callers/usages). Briefing is the budgeted prompt
// projection laid on the reviewer's desk before execution starts.

// material is one piece of source content the briefing wants inlined — the
// render protocol's data contract between the per-scope briefer (which decides
// WHAT the reviewer should see without searching) and renderMaterials (which
// reads, formats, and enforces the byte budget). Keeping the contract
// data-shaped rather than "render yourself" keeps budgeting and the
// file_read-mirroring line format single-sourced across scopes.
type material struct {
	path    string
	symbols []string // the functions in path that matter (span fallback / span-only content)
	whole   bool     // prefer the whole file (the unit's own source); false = symbols' spans only
	label   string   // how the material relates to the unit, e.g. "caller a/b.go::F"; "" for own source
	prio    int      // 0 = the reviewed source itself; higher drops first when the budget tightens
}

// briefer is the render protocol: a Unit scope's answer to "which source belongs
// in this unit's briefing". Implementations are per unit.Scope, return
// descriptors only (no IO), and stay ignorant of budgets and formatting.
type briefer interface {
	materials(u unit.Unit) []material
}

// brieferFor picks the scope's briefer. Func and file units differ only in what
// their Fragments carry, so both present their own source; call-chain units
// additionally surface the bodies of the chain's caller/callee neighbors.
func (a *Runner) brieferFor(s unit.Scope) briefer {
	if s == unit.ScopeCallChain {
		return chainBriefer{neighbors: a.features.Enabled(feature.NeighborSource)}
	}
	return ownSourceBriefer{}
}

// ownSourceBriefer presents the unit's own file(s) whole, carrying the unit's
// symbols so an over-budget file can fall back to just the changed functions'
// bodies (see renderMaterials).
type ownSourceBriefer struct{}

func (ownSourceBriefer) materials(u unit.Unit) []material {
	symbolsByPath := map[string][]string{}
	for _, f := range u.Fragments {
		symbolsByPath[f.Path] = append(symbolsByPath[f.Path], f.Symbols...)
	}
	var out []material
	for _, p := range u.Paths() {
		out = append(out, material{path: p, symbols: symbolsByPath[p], whole: true})
	}
	return out
}

// maxNeighborMaterials caps how many neighbor bodies a chain briefing inlines —
// a chain reviews an edge set, so a handful of far-side bodies carries the
// signal; past that the budget is better spent on member source.
const maxNeighborMaterials = 6

// chainBriefer presents member source first, then the bodies of the
// caller/callee neighbors the Unit's clues already resolved: a chain
// review reasons across call edges, so the far side of each edge should be
// visible without a search. Only bodies of functions OUTSIDE the chain — a
// member is never its sibling's neighbor (walkNeighbors seeds visited).
type chainBriefer struct {
	neighbors bool // the neighbor_source gate
}

func (b chainBriefer) materials(u unit.Unit) []material {
	mats := ownSourceBriefer{}.materials(u)
	if !b.neighbors {
		return mats
	}
	own := map[string]bool{}
	for _, p := range u.Paths() {
		own[p] = true
	}
	seen := map[string]bool{}
	count := 0
	for _, c := range u.Clues {
		if c.Relation != unit.RelCaller && c.Relation != unit.RelCallee {
			continue
		}
		path, _, ok := language.SplitSymbolID(c.Ref)
		if !ok || own[path] || seen[c.Ref] {
			continue
		}
		seen[c.Ref] = true
		mats = append(mats, material{
			path:    path,
			symbols: []string{c.Ref},
			label:   string(c.Relation) + " " + c.Ref,
			prio:    1,
		})
		count++
		if count >= maxNeighborMaterials {
			break
		}
	}
	return mats
}

// preloadSourceBudget caps the total bytes of source inlined into one briefing.
// Sized so typical units fit whole while a giant file can't crowd the prompt
// toward the token guard (which would skip the unit's review outright).
const preloadSourceBudget = 32 * 1024

// sourceNotPreloaded fills {{unit_source}} when nothing was inlined, so the
// literal placeholder never leaks and the reviewer knows to fetch on demand.
const sourceNotPreloaded = "(not preloaded — fetch what you need via file_read)"

// piece is one rendered briefing fragment — the shared engine's output,
// assembled either into the classic template slots (one big user message) or
// into per-file typed messages (typed_briefing). A piece with file identity is
// inlined source; one without is an advisory note (budget-miss pointer).
// text keeps the exact bytes the classic path would have written (including
// the trailing blank-line separator) so classic assembly stays byte-identical.
type piece struct {
	prio            int
	text            string
	path            string // file identity; "" for advisory notes
	start, end, tot int
}

// renderPieces reads and formats a briefer's materials under one shared byte
// budget, filled in priority order (own source first — an over-budget aux
// material is dropped silently, never at the essentials' expense). outcomes
// records each material's fate ("whole p" / "ranged p" / "budget_miss p" /
// "dropped label" / "unreadable p") for the unit's debrief.
//
// Reads go through the file_read tool's own FileReader (mode-aware: workspace
// reads disk, range/commit read `git show <ref>:`) and mirror its numbered-line
// format, so inlined source and tool output look identical to the model. A
// whole-file material over the remaining budget falls back to just its
// functions' bodies (ranged_preload gate); with no symbols (or gate off) it is
// named but not inlined so a ranged file_read still works.
func (a *Runner) renderPieces(ctx context.Context, mats []material) (pieces []piece, outcomes []string) {
	fr := a.fileReader()
	if fr == nil {
		return nil, nil
	}

	// Stable fill order: essentials before aux. Sort is by prio only (slice order
	// within a prio is already meaningful: member files, then clue order).
	ordered := make([]material, len(mats))
	copy(ordered, mats)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].prio < ordered[j].prio })

	budget := preloadSourceBudget
	for _, m := range ordered {
		content, err := fr.Read(ctx, m.path)
		if err != nil {
			// Deleted/unreadable — the placeholder degrade downstream handles it.
			outcomes = append(outcomes, "unreadable "+m.path)
			continue
		}
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

		if m.whole && len(content) <= budget {
			budget -= len(content)
			var b strings.Builder
			fmt.Fprintf(&b, "File: %s (Total lines: %d)\n", m.path, len(lines))
			for i, line := range lines {
				fmt.Fprintf(&b, "%d|%s\n", i+1, line)
			}
			b.WriteString("\n")
			pieces = append(pieces, piece{prio: m.prio, text: b.String(),
				path: m.path, start: 1, end: len(lines), tot: len(lines)})
			outcomes = append(outcomes, "whole "+m.path)
			continue
		}

		// Whole file didn't fit (or was never wanted): inline the named functions'
		// bodies as ranged blocks. Span-only materials always take this path;
		// whole-file fallback additionally needs the ranged_preload gate.
		if len(m.symbols) > 0 && (!m.whole || a.features.Enabled(feature.RangedPreload)) {
			if spans := renderSpans(ctx, a.sourceAnalyzer(), m, content, lines, &budget); len(spans) > 0 {
				pieces = append(pieces, spans...)
				outcomes = append(outcomes, "ranged "+m.path)
				continue
			}
		}

		if m.whole {
			pieces = append(pieces, piece{prio: m.prio, text: fmt.Sprintf(
				"File: %s — %d bytes exceeds the preload budget; read on demand via file_read\n\n", m.path, len(content))})
			outcomes = append(outcomes, "budget_miss "+m.path)
			continue
		}
		// Aux material that couldn't be inlined is dropped silently — it's bonus
		// context; a note would spend prompt on what the reviewer can't use.
		outcomes = append(outcomes, "dropped "+m.label)
	}
	return pieces, outcomes
}

// renderMaterials is the classic assembly of renderPieces: prio 0 concatenated
// into {{unit_source}} (the reviewed files), prio >0 into {{related_source}}
// (context-only bodies) — the two carry different review semantics (review
// this vs. don't). typed_briefing replaces this with per-file messages; the
// bytes here are unchanged from before the pieces refactor.
func (a *Runner) renderMaterials(ctx context.Context, mats []material) (unitSource, relatedSource string, outcomes []string) {
	if a.fileReader() == nil {
		return sourceNotPreloaded, "", nil
	}
	pieces, outcomes := a.renderPieces(ctx, mats)
	var essential, aux strings.Builder
	for _, p := range pieces {
		if p.prio > 0 {
			aux.WriteString(p.text)
		} else {
			essential.WriteString(p.text)
		}
	}
	unitSource = strings.TrimRight(essential.String(), "\n")
	if unitSource == "" {
		unitSource = sourceNotPreloaded
	}
	return unitSource, strings.TrimRight(aux.String(), "\n"), outcomes
}

// renderSpans inlines each of m.symbols' bodies from content as one ranged
// piece per symbol, mirroring file_read's range output (File header +
// LINE_RANGE + numbered lines), charging bytes against budget. Returns nil
// when nothing fit or no symbol resolved to a span.
func renderSpans(ctx context.Context, analyzer *language.Analyzer, m material, content string, lines []string, budget *int) []piece {
	var out []piece
	for _, sym := range m.symbols {
		definition, ok := analyzer.DefinitionByID(ctx, language.Source{Path: m.path, Content: content}, sym)
		start, end := definition.Span.Start, definition.Span.End
		if !ok || start < 1 || end > len(lines) {
			continue
		}
		var s strings.Builder
		if m.label != "" {
			fmt.Fprintf(&s, "// %s\n", m.label)
		}
		fmt.Fprintf(&s, "File: %s (Total lines: %d)\nLINE_RANGE: %d-%d\n", m.path, len(lines), start, end)
		for i := start; i <= end; i++ {
			fmt.Fprintf(&s, "%d|%s\n", i, lines[i-1])
		}
		s.WriteString("\n")
		if s.Len() > *budget {
			continue // this body doesn't fit; a smaller later one still might
		}
		*budget -= s.Len()
		out = append(out, piece{prio: m.prio, text: s.String(),
			path: m.path, start: start, end: end, tot: len(lines)})
	}
	return out
}

// Typed-briefing slot pointers: with sources carried as separate messages, the
// template's source slots hold pointers so the instructions around them keep
// making sense (and custom templates keep substituting cleanly).
const (
	typedUnitSourcePointer = "(provided as separate messages after this task — do NOT call file_read on those ranges again)"
	typedRelatedPointer    = "(provided as separate messages after this task)"
)

// assembleTypedBriefing builds the loop's opening conversation in the
// typed_briefing shape: [template messages (source slots hold pointers), own
// Files, related Files]. Files come AFTER the task message — the compression
// engine freezes messages[0:2] as [system, task] and appends its summary to
// index 1, so nothing may sit between them. Degradation under tokenLimit
// mirrors the classic path: drop related Files first, then own Files (the
// slot then shows the fetch-on-demand sentinel), recorded in deb.
func (a *Runner) assembleTypedBriefing(build func(unitSlot, relatedSlot string) []llm.Message, pieces []piece, tokenLimit int, deb *session.Debrief) []msg.Msg {
	var own, related []msg.Msg
	var notes strings.Builder // budget-miss pointers stay in the slot, not as Files
	for _, p := range pieces {
		if p.path == "" {
			notes.WriteString(p.text)
			continue
		}
		f := msg.NewFile(p.path, p.start, p.end, p.tot, strings.TrimRight(p.text, "\n"))
		if p.prio > 0 {
			related = append(related, f)
		} else {
			own = append(own, f)
		}
	}

	assemble := func(withOwn, withRelated bool) []msg.Msg {
		unitSlot := sourceNotPreloaded
		if withOwn && len(own) > 0 {
			unitSlot = typedUnitSourcePointer
		}
		if n := strings.TrimRight(notes.String(), "\n"); n != "" {
			unitSlot += "\n" + n
		}
		relatedSlot := ""
		if withRelated && len(related) > 0 {
			relatedSlot = typedRelatedPointer
		}
		out := msg.Wrap(build(unitSlot, relatedSlot))
		if withOwn {
			out = append(out, own...)
		}
		if withRelated {
			out = append(out, related...)
		}
		return out
	}

	over := func(m []msg.Msg) bool { return llm.CountMessagesTokens(msg.Lower(m)) > tokenLimit }
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

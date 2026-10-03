package runner

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/sourcecontext"
)

const (
	sourceNotPreloaded   = "(not preloaded — fetch what you need via read_files)"
	unitSourcePointer    = "(provided as separate messages after this task — do NOT call read_files on those ranges again)"
	relatedSourcePointer = "(provided as separate messages after this task)"
	maxNeighborSources   = 6
	maxInitialOutlines   = 6
	maxInitialReferences = 24
	initialOutlineBudget = 12 * 1024

	// Priority is relative within one Review 1 execution. Files containing the
	// reviewed diff are primary evidence; statically selected neighbors are
	// useful context; files discovered during the loop keep the default 0.
	priorityRelatedSource = 10
	priorityUnitSource    = 20
)

// preloadReviewFiles reads the high-confidence source already implied by the
// Unit. Every selected source enters Harness as a full-fidelity File; the File
// itself chooses source, outline, or path when ContextManager projects it.
func (a *Runner) preloadReviewFiles(
	ctx context.Context,
	u unit.Unit,
) (own, related []*msg.File, outcomes []string) {
	if a.fileReader() == nil {
		return nil, nil, nil
	}

	symbols := symbolsByPath(u)
	for _, filePath := range u.Paths() {
		files, outcome := a.preloadPath(ctx, filePath, symbols[filePath], true, "code under review")
		for _, file := range files {
			file.ConfigureContext("unit", strings.Join(symbols[filePath], ", ")).
				ConfigurePriority(priorityUnitSource)
		}
		own = append(own, files...)
		if outcome != "" {
			outcomes = append(outcomes, outcome)
		}
	}

	for _, clue := range a.relatedSourceClues(u) {
		filePath, _, _ := language.SplitSymbolID(clue.Ref)
		label := "related " + string(clue.Relation) + " " + clue.Ref
		files, outcome := a.preloadPath(ctx, filePath, []string{clue.Ref}, false, label)
		for _, file := range files {
			file.ConfigureContext(string(clue.Relation), clue.Ref).
				ConfigurePriority(priorityRelatedSource)
		}
		related = append(related, files...)
		if outcome != "" {
			outcomes = append(outcomes, outcome)
		}
	}
	return own, related, outcomes
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

// preloadPath mirrors read_files's numbered-line format. Own source carries the
// whole file; related source carries only the named function bodies.
func (a *Runner) preloadPath(
	ctx context.Context,
	filePath string,
	symbols []string,
	whole bool,
	label string,
) (files []*msg.File, outcome string) {
	content, err := a.fileReader().Read(ctx, filePath)
	if err != nil {
		return nil, "unreadable " + filePath
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	outline, _ := a.sourceAnalyzer().FileOutline(ctx, language.Source{Path: filePath, Content: content})

	if whole {
		var b strings.Builder
		fmt.Fprintf(&b, "File: %s (Total lines: %d)\n", filePath, len(lines))
		for i, line := range lines {
			fmt.Fprintf(&b, "%d|%s\n", i+1, line)
		}
		return []*msg.File{
			msg.NewFile(filePath, 1, len(lines), len(lines), strings.TrimRight(b.String(), "\n")).
				ConfigurePresentation(label, outline.Render()),
		}, "whole " + filePath
	}

	if len(symbols) > 0 {
		files = a.preloadSpans(ctx, filePath, symbols, label, content, lines, outline)
		if len(files) > 0 {
			return files, "ranged " + filePath
		}
	}
	return nil, "dropped " + label
}

func (a *Runner) preloadSpans(
	ctx context.Context,
	filePath string,
	symbols []string,
	label, content string,
	lines []string,
	outline language.FileOutline,
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
		out = append(out, msg.NewFile(
			filePath, start, end, len(lines), strings.TrimRight(b.String(), "\n"),
		).ConfigurePresentation(label, outline.RenderRange(start, end)))
	}
	return out
}

// assembleReviewMessages keeps [system, task] as a stable prefix and appends
// every selected source range as a separate full-fidelity File message.
func (a *Runner) assembleReviewMessages(
	build func(unitSlot, relatedSlot string) []llm.Message,
	own, related []*msg.File,
	initial []msg.FileContextEntry,
) []agentgo.AgentMessage {
	unitSlot := sourceNotPreloaded
	if len(own) > 0 {
		unitSlot = unitSourcePointer
	}
	relatedSlot := ""
	if len(related) > 0 {
		relatedSlot = relatedSourcePointer
	}

	out := msg.Wrap(build(unitSlot, relatedSlot))
	if len(initial) > 0 {
		out = append(out, msg.NewFileContext(initial))
	}
	for _, file := range own {
		out = append(out, file)
	}
	for _, file := range related {
		out = append(out, file)
	}
	return out
}

// initialFileContext turns statically known relationships into a bounded
// navigation catalog. Unit source and resolved call-neighbor source remain
// separate File messages; other related files receive an Outline when the language
// boundary can provide one, otherwise a path-only reference.
func (a *Runner) initialFileContext(
	ctx context.Context,
	u unit.Unit,
	usagePaths []string,
	own, related []*msg.File,
) ([]msg.FileContextEntry, []session.InitialOutlineAttempt) {
	if a.fileReader() == nil {
		return nil, nil
	}
	source := make(map[string]bool)
	for _, file := range append(append([]*msg.File(nil), own...), related...) {
		source[path.Clean(file.Path)] = true
	}

	type candidate struct{ path, reason, ref string }
	seen := make(map[string]bool)
	var candidates []candidate
	add := func(filePath, reason, ref string) {
		filePath = path.Clean(strings.TrimSpace(filePath))
		if filePath == "." || filePath == "" || source[filePath] || seen[filePath] {
			return
		}
		seen[filePath] = true
		candidates = append(candidates, candidate{path: filePath, reason: reason, ref: ref})
	}
	for _, clue := range u.Clues {
		filePath := ""
		if clue.Relation == unit.RelProject {
			filePath = clue.Ref
		} else {
			filePath, _, _ = language.SplitSymbolID(clue.Ref)
		}
		add(filePath, string(clue.Relation), clue.Ref)
	}
	for _, filePath := range usagePaths {
		add(filePath, "usage_site", "")
	}
	for _, filePath := range a.repositoryReferencePaths(u, maxInitialReferences) {
		add(filePath, "repository_reference", "")
	}

	entries := make([]msg.FileContextEntry, 0, min(len(candidates), maxInitialReferences))
	attempts := make([]session.InitialOutlineAttempt, 0, min(len(candidates), maxInitialOutlines))
	outlineCount, outlineBytes := 0, 0
	for _, candidate := range candidates {
		if len(entries) >= maxInitialReferences {
			break
		}
		entry := msg.FileContextEntry{
			Path: candidate.path, View: msg.ViewReference,
			Reason: candidate.reason, Ref: candidate.ref,
		}
		if candidate.reason != string(unit.RelProject) {
			attempt := session.InitialOutlineAttempt{
				Path: candidate.path, Language: outlineLanguage(candidate.path),
			}
			if outlineCount >= maxInitialOutlines {
				attempt.Outcome = "capacity_rejected"
				attempts = append(attempts, attempt)
				entries = append(entries, entry)
				continue
			}
			content, err := a.fileReader().Read(ctx, candidate.path)
			if err != nil {
				attempt.Outcome = "read_error"
				attempts = append(attempts, attempt)
				entries = append(entries, entry)
				continue
			}
			outline, outlineErr := a.sourceAnalyzer().FileOutline(ctx, language.Source{Path: candidate.path, Content: content})
			if outlineErr != nil {
				attempt.Outcome = "analysis_error"
				attempts = append(attempts, attempt)
				entries = append(entries, entry)
				continue
			}
			rendered := outline.Render()
			attempt.Bytes = len(rendered)
			switch {
			case rendered == "":
				attempt.Outcome = "empty"
			case outlineBytes+len(rendered) > initialOutlineBudget:
				attempt.Outcome = "budget_rejected"
			default:
				attempt.Outcome = "admitted"
				entry.View = msg.ViewOutline
				entry.Content = rendered
				outlineCount++
				outlineBytes += len(rendered)
			}
			attempts = append(attempts, attempt)
		}
		entries = append(entries, entry)
	}
	return entries, attempts
}

func outlineLanguage(filePath string) string {
	if detected, ok := language.Detect(filePath); ok {
		return string(detected)
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), ".")
}

func (a *Runner) repositoryReferencePaths(u unit.Unit, limit int) []string {
	if a.repoIndex == nil || limit <= 0 {
		return nil
	}
	type rankedPath struct {
		path  string
		score int
	}
	var ranked []rankedPath
	scores := map[string]int{}
	seen := map[string]bool{}
	index := &language.RepositoryIndex{Graph: a.repoIndex.Graph}
	for _, id := range u.AllSymbols() {
		for _, use := range index.UsesOf(id) {
			if seen[use.ID] {
				continue
			}
			seen[use.ID] = true
			scores[use.Location.Path]++
		}
	}
	for path, score := range scores {
		ranked = append(ranked, rankedPath{path, score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].path < ranked[j].path
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]string, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].path
	}
	return out
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

// renderUsageSites selects CodeGraph references to the unit's changed
// symbols and renders a `path:line: text` blast-radius map for {{usage_sites}},
// plus the site count for the unit's debrief. Same cost class as the
// caller/callee walk, so it honors the same costly-context budget gate
// (a.costlyContext) on top of its own feature gate. Returns "" when gated off
// or nothing was found.
func (a *Runner) renderUsageSites(u unit.Unit) (string, int, []string) {
	if !a.features.Enabled(feature.UsageSites) || !a.costlyContext {
		return "", 0, nil
	}
	symbols := u.AllSymbols()
	if len(symbols) == 0 {
		return "", 0, nil
	}
	exclude := map[string]bool{}
	for _, p := range u.Paths() {
		exclude[p] = true
	}
	usages := sourcecontext.FindUsages(a.sourceAnalyzer(), symbols, exclude)
	if len(usages) == 0 {
		return "", 0, nil
	}
	var b strings.Builder
	last := ""
	seenPaths := make(map[string]bool)
	var paths []string
	for _, us := range usages {
		if us.Symbol != last {
			fmt.Fprintf(&b, "`%s`:\n", us.Symbol)
			last = us.Symbol
		}
		fmt.Fprintf(&b, "  %s:%d: %s\n", us.File, us.Line, us.Text)
		if !seenPaths[us.File] {
			seenPaths[us.File] = true
			paths = append(paths, us.File)
		}
	}
	return strings.TrimRight(b.String(), "\n"), len(usages), paths
}

package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// UnitContext is the review context ccr would inject for one review unit — what
// `--dry-run` prints (text) or emits (json) instead of running the LLM. The
// structural fields (Scope/Paths/Fragments/Clues) let `--format json` be used to
// compare how features (call-chain merge, clues) change unit shape, for free.
type UnitContext struct {
	ID             string         `json:"id"`
	Path           string         `json:"path"`                      // representative member path
	Scope          string         `json:"scope"`                     // func / file / callchain
	Paths          []string       `json:"paths"`                     // member files; len>1 = cross-file (call-chain) unit
	Fragments      int            `json:"fragments"`                 // changed regions merged into this unit
	Clues          map[string]int `json:"clues"`                     // "<relation>/<kind>" -> count (e.g. owner/rule, used/doc, caller/spec)
	SpecCases      string         `json:"spec_cases,omitempty"`      // contract: own spec/case + inherited caller spec + depended-on callee contracts
	Rules          string         `json:"rules,omitempty"`           // path-glob rule.json + function-level @rule
	SeeAlso        string         `json:"see_also,omitempty"`        // curated @link pointers
	Prior          string         `json:"prior,omitempty"`           // a previous review's findings on this unit (to reconcile)
	ProjectContext string         `json:"project_context,omitempty"` // changed manifest/lock pointers from the same Component
	Materials      []string       `json:"materials,omitempty"`       // briefing materials (descriptors, not content): own source + related bodies
	UsageSites     string         `json:"usage_sites,omitempty"`     // pre-grepped use sites of the changed symbols
}

// describeMaterials renders a briefer's materials as one-line descriptors so
// --dry-run shows what the briefing would inline without reading any file.
func describeMaterials(mats []material) []string {
	out := make([]string, 0, len(mats))
	for _, m := range mats {
		switch {
		case m.whole && len(m.symbols) > 0:
			out = append(out, m.path+" (whole; ranged fallback: "+strings.Join(m.symbols, ", ")+")")
		case m.whole:
			out = append(out, m.path+" (whole)")
		default:
			out = append(out, m.label+" (body)")
		}
	}
	return out
}

// countClues tallies a Unit's Clues on the relation×kind matrix, keyed
// "<relation>/<kind>" (e.g. self/spec, owner/rule, used/doc, caller/spec) — so
// --dry-run shows which relation contributed which evidence, for free.
func countClues(clues []unit.Clue) map[string]int {
	m := make(map[string]int, len(clues))
	for _, c := range clues {
		m[string(c.Relation)+"/"+string(c.Kind)]++
	}
	return m
}

// DryRun loads diffs once and returns the complete no-LLM view behind
// `ccr review --dry-run`: the file-selection preview (which files are reviewed /
// excluded — the `--preview` subset) plus each review unit's assembled context
// (spec/case/rule/link + caller/callee) and the run-level repo map. So file
// filtering, spec.json / call-graph coverage and map injection can all be
// inspected in one pass, for free.
func (a *Runner) DryRun(ctx context.Context) (*Preview, []UnitContext, string, error) {
	if err := a.loadChanges(ctx); err != nil {
		return nil, nil, "", fmt.Errorf("load diffs: %w", err)
	}
	a.prepareFileSelections(ctx)
	preview := a.buildPreview()
	a.changes = a.filterDiffs(a.changes)
	units, err := a.splitUnits()
	if err != nil {
		return nil, nil, "", fmt.Errorf("split units: %w", err)
	}
	repoMap := ""
	if a.features.Enabled(feature.RepoMap) {
		repoMap = a.buildRepoMap(units)
	}

	out := make([]UnitContext, 0, len(units))
	for _, u := range units {
		// Mirror reviewUnit's context assembly: clues + the path-glob rule.json.
		specCases, specRules, seeAlso, prior := renderClues(u.Clues)
		usageSites, _ := a.renderUsageSites(u)
		rule := a.resolveSystemRule(strings.ToLower(u.Path()))
		if specRules != "" {
			if rule != "" {
				rule += "\n"
			}
			rule += specRules
		}
		out = append(out, UnitContext{
			ID:             u.ID,
			Path:           u.Path(),
			Scope:          string(u.Scope),
			Paths:          u.Paths(),
			Fragments:      len(u.Fragments),
			Clues:          countClues(u.Clues),
			SpecCases:      specCases,
			Rules:          rule,
			SeeAlso:        seeAlso,
			Prior:          prior,
			ProjectContext: renderProjectContext(u.Clues),
			Materials:      describeMaterials(a.brieferFor(u.Scope).materials(u)),
			UsageSites:     usageSites,
		})
	}
	return preview, out, repoMap, nil
}

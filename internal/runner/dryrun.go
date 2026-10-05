package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/formation"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// UnitContext is the review context ccr would inject for one review unit — what
// `--dry-run` prints (text) or emits (json) instead of running the LLM. The
// structural fields (Scope/Paths/Fragments/Clues) let `--format json` be used to
// compare how features (relation grouping, clues) change unit shape, for free.
type UnitContext struct {
	BudgetExceeded bool                    `json:"budget_exceeded,omitempty"`
	Grouping       []unit.GroupingEvidence `json:"grouping,omitempty"`
	Boundaries     []unit.GroupingEvidence `json:"boundaries,omitempty"`
	Targets        []unit.TargetSummary    `json:"targets"`
	DiffTokens     int                     `json:"diff_tokens"`
	ID             string                  `json:"id"`
	Path           string                  `json:"path"`                      // representative member path
	Scope          string                  `json:"scope"`                     // func / file / related
	Paths          []string                `json:"paths"`                     // member files
	Fragments      int                     `json:"fragments"`                 // changed regions merged into this unit
	Clues          map[string]int          `json:"clues"`                     // "<relation>/<kind>" -> count (e.g. owner/rule, used/doc, caller/spec)
	SpecCases      string                  `json:"spec_cases,omitempty"`      // contract: own spec/case + inherited caller spec + depended-on callee contracts
	Rules          string                  `json:"rules,omitempty"`           // path-glob rule.json + function-level @rule
	SeeAlso        string                  `json:"see_also,omitempty"`        // curated @link pointers
	Prior          string                  `json:"prior,omitempty"`           // a previous review's findings on this unit (to reconcile)
	ProjectContext string                  `json:"project_context,omitempty"` // changed manifest/lock pointers from the same Component
	SourcePreloads []string                `json:"source_preloads,omitempty"` // descriptors, not content: own source + related bodies
	UsageSites     string                  `json:"usage_sites,omitempty"`     // CodeGraph reference sites of the changed symbols
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
	if a.session != nil {
		defer a.session.Flush()
		ctx = a.session.Context(ctx)
	}
	if err := a.loadChanges(ctx); err != nil {
		return nil, nil, "", fmt.Errorf("load diffs: %w", err)
	}
	a.prepareFileSelections(ctx)
	preview := a.buildPreview()
	a.changes = a.filterDiffs(a.changes)
	formationCtx, finish := session.Begin(ctx, "unit.formation")
	units, err := a.splitUnits(formationCtx)
	if err == nil && a.session != nil {
		a.persistFormedUnits(formationCtx, units)
	}
	finish(err)
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
		usageSites, _, _ := a.renderUsageSites(u)
		rule := a.resolveSystemRule(strings.ToLower(u.Path()))
		if specRules != "" {
			if rule != "" {
				rule += "\n"
			}
			rule += specRules
		}
		out = append(out, UnitContext{
			ID:       u.ID,
			Grouping: u.Grouping, Boundaries: u.Boundaries, Targets: u.Targets(), DiffTokens: u.DiffTokens, BudgetExceeded: u.BudgetExceeded,
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
			SourcePreloads: a.describePreloadedSources(u),
			UsageSites:     usageSites,
		})
	}
	return preview, out, repoMap, nil
}

// GroupingReport explains the strategies and count target used in this run.
func (a *Runner) GroupingReport() formation.GroupingReport { return a.grouping }

// Package feature is ccr's feature-gate registry: named, process-level capability
// toggles resolved once at startup (Kubernetes-style --feature-gates, NOT dynamic
// product feature flags). Their purpose is ablation — turning a review capability
// off to measure its marginal effect (leave-one-out), so `--dry-run --format json`
// and real review both honor the same gates and every run self-describes its config.
package feature

import (
	"fmt"
	"sort"
	"strings"
)

// Gate is a feature-gate name. Keep values stable — they appear in config, env,
// CLI, and echoed JSON (eval join keys).
type Gate string

const (
	Plan             Gate = "plan"              // PLAN_TASK pre-pass per unit
	CallChain        Gate = "callchain"         // call-chain merge axis (cross-file units via call graph)
	CallerCallee     Gate = "caller_callee"     // caller/callee context clues (call-graph grep)
	SpecCase         Gate = "spec_case"         // spec/case contract clues (authored; all relations)
	Rule             Gate = "rule"              // @rule clues (authored; all relations)
	Link             Gate = "link"              // @link see-also clues (authored; all relations)
	Doc              Gate = "doc"               // docstring clues (derived from source; all relations)
	History          Gate = "history"           // prior-review findings clues (reconciliation)
	HypothesisReview Gate = "hypothesis_review" // independent evidence review before deterministic Trial
	Relocation       Gate = "relocation"        // LLM re-location of comments to the right line
	Routing          Gate = "routing"           // multi-model round-robin pool; off = single model (deterministic)
	RepoMap          Gate = "repo_map"          // ranked symbol map injected per run (anti guessed-name searches)
	TypedGraph       Gate = "typed_graph"       // type-checker-resolved call edges for caller/callee/merge (Go)

	// Briefing gates: what source material each unit's briefing pre-inlines so the
	// review loop doesn't spend rounds fetching it (see internal/runner/briefing.go).
	UsageSites     Gate = "usage_sites"     // pre-grepped use sites of the changed symbols
	RangedPreload  Gate = "ranged_preload"  // over-budget file fallback: inline the unit's function bodies
	NeighborSource Gate = "neighbor_source" // callchain briefing: inline caller/callee neighbor bodies
	FileDedup      Gate = "file_dedup"      // stub earlier file_read results superseded by a later covering read
	FileEvict      Gate = "file_evict"      // under token pressure, shed re-derivable file content before LLM compression

	// TypedBriefing: briefing preloads as per-file messages. Flipped ON by the
	// corpus replay A/B (eval/README §9): timeouts 9→3 across 70 units/arm
	// (robustness win via File dedup/evict covering preloads), cost flat, no
	// quality regression evidence.
	TypedBriefing Gate = "typed_briefing"
	// ReviewTeam is the Review Team v0 (docs/unit_review.md): concurrent unit
	// loops share an in-memory case board — auto-published facts, directed
	// incremental injection at turn boundaries. Default OFF (experimental)
	// until the regression corpus shows cross-file recall or dedup benefit.
	ReviewTeam Gate = "review_team"
	// PostBulletin is the model-initiated publish side of the Review Team
	// (see docs/unit_review.md): a `post_bulletin` tool for observation-level
	// suspicions to the board — the channel the cross-file consistency prize
	// actually rides on (auto facts only say who read what). Effective only when
	// review_team is also on (no board, no tool).
	PostBulletin Gate = "post_bulletin"
)

// def is a gate's registry entry: default state + one-line description.
// def is a gate's registry entry. Experimental marks a gate as default-off
// pending validation (replay A/B / corpus evidence) — the two-tier semantics
// lives HERE, in the registry, not in a test allowlist.
type def struct {
	Default      bool
	Desc         string
	Experimental bool
}

// registry is the single source of truth for which gates exist and their defaults
// (all on — the full feature set is the product default; gates exist to turn things
// OFF for ablation).
var registry = map[Gate]def{
	Plan:             {true, "PLAN_TASK pre-pass per unit", false},
	CallChain:        {true, "call-chain merge axis (cross-file units via call graph)", false},
	CallerCallee:     {true, "caller/callee context clues (call-graph grep)", false},
	SpecCase:         {true, "spec/case contract clues (authored; all relations)", false},
	Rule:             {true, "@rule clues (authored; all relations)", false},
	Link:             {true, "@link see-also clues (authored; all relations)", false},
	Doc:              {true, "docstring clues (derived from source; all relations)", false},
	History:          {true, "prior-review findings clues (reconciliation)", false},
	HypothesisReview: {true, "independent evidence review of hypotheses before deterministic Trial", false},
	Relocation:       {true, "LLM re-location of comments to the right line", false},
	Routing:          {true, "multi-model round-robin pool; off = single model (deterministic)", false},
	RepoMap:          {true, "ranked symbol map injected per run (anti guessed-name searches)", false},
	TypedGraph:       {true, "type-checker-resolved call edges for caller/callee/merge (Go)", false},

	UsageSites:     {true, "pre-grepped use sites of the changed symbols in the briefing", false},
	RangedPreload:  {true, "over-budget file fallback: inline the unit's function bodies", false},
	NeighborSource: {true, "callchain briefing: inline caller/callee neighbor bodies", false},
	FileDedup:      {true, "stub earlier file_read results superseded by a later covering read", false},
	FileEvict:      {true, "under token pressure, shed re-derivable file content before LLM compression", false},
	TypedBriefing:  {true, "briefing preloads as per-file messages (File dedup/evict cover preloads)", false},
	ReviewTeam:     {false, "Review Team v0: units share an in-memory case board (auto facts + directed injection)", true},
	PostBulletin:   {false, "post_bulletin tool: the model posts observation-level suspicions to the team board (needs review_team)", true},
}

// Set is a resolved gate configuration. nil is valid and means "all defaults".
type Set map[Gate]bool

// Enabled reports whether gate g is on, falling back to its registry default when
// unset. Unknown gates report false (they were rejected at Resolve time anyway).
func (s Set) Enabled(g Gate) bool {
	if v, ok := s[g]; ok {
		return v
	}
	if d, ok := registry[g]; ok {
		return d.Default
	}
	return false
}

// Resolved returns the full gate->bool map with every registered gate present
// (defaults filled in). Use it to echo the effective config into output so eval
// artifacts self-describe.
func (s Set) Resolved() map[string]bool {
	out := make(map[string]bool, len(registry))
	for g := range registry {
		out[string(g)] = s.Enabled(g)
	}
	return out
}

// Resolve layers overrides over the registry defaults in precedence order
// (default < config < env < cli), matching ccr's other config resolution. Each
// layer is a gate->bool map; nil layers are skipped. An unknown gate name in any
// layer is a hard error (typo protection — a silently-ignored gate would make an
// ablation run lie about what it tested).
func Resolve(layers ...map[Gate]bool) (Set, error) {
	s := Set{}
	for _, layer := range layers {
		for g, v := range layer {
			if _, ok := registry[g]; !ok {
				return nil, fmt.Errorf("unknown feature gate %q; known: %s", g, strings.Join(Names(), ", "))
			}
			s[g] = v
		}
	}
	return s, nil
}

// Names returns all registered gate names, sorted (stable help / error text).
func Names() []string {
	out := make([]string, 0, len(registry))
	for g := range registry {
		out = append(out, string(g))
	}
	sort.Strings(out)
	return out
}

// Describe returns "name  (default on/off)  desc" lines for CLI help, sorted.
// Column width is computed from the registry so adding a longer gate name never
// silently misaligns the help output.
func Describe() []string {
	names := Names()
	maxW := 0
	for _, n := range names {
		if len(n) > maxW {
			maxW = len(n)
		}
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		d := registry[Gate(n)]
		state := "on"
		if !d.Default {
			state = "off"
		}
		tag := ""
		if d.Experimental {
			tag = " [experimental]"
		}
		out = append(out, fmt.Sprintf("%-*s (default %s)%s  %s", maxW, n, state, tag, d.Desc))
	}
	return out
}

// Experimental reports whether a gate is validation-pending (default-off tier).
func Experimental(g Gate) bool { return registry[g].Experimental }

// Parse reads a "k=v,k=v" spec (CLI --features / CCR_FEATURES env). Values accept
// on/off/true/false/1/0/yes/no (case-insensitive). Unknown gate names are NOT
// rejected here (Resolve does that against the registry); Parse only handles
// syntax. Empty spec → empty map.
func Parse(spec string) (map[Gate]bool, error) {
	out := map[Gate]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("bad feature spec %q (want name=value)", part)
		}
		b, err := parseBool(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("feature %q: %w", k, err)
		}
		out[Gate(strings.TrimSpace(k))] = b
	}
	return out, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool %q (want on/off/true/false/1/0/yes/no)", v)
	}
}

// Package codegraph builds a bounded, per-review picture of "what symbols
// exist and how files reference each other", and ranks the symbols most
// relevant to a change.
//
// It is the shared substrate for several consumers with DIFFERENT precision
// needs: ranking a symbol map for prompt injection tolerates noisy edges
// (a wrong edge wastes a few tokens), while chain-merge and caller-ascent
// (future backends) demand resolved, high-confidence edges (a wrong edge
// corrupts the review scope or the governing contract). Edges therefore
// carry a Confidence level and consumers filter to what they can stomach —
// one graph, tiered consumption, instead of per-feature heuristics.
//
// Source parsing and language-native facts live in internal/language. This
// package adapts those facts into graph-backed review features: low-confidence
// name-paired edges for ranking, and higher-confidence call neighbors for
// context and unit merging.
package codegraph

import (
	"fmt"
	"sort"
	"strings"
)

// Def is one package-level definition extracted from a source file.
type Def struct {
	Ident     string // bare identifier (method: "Recv.Name" like unit symbol)
	SymbolID  string // <relpath>::<symbol> — same join key as unit/spec
	File      string // repo-relative path, forward slashes
	Line      int    // 1-based line of the definition
	Signature string // one-line signature for rendering
}

// Extraction is a language backend's raw output: definitions per file and
// identifier reference counts per file. Ranking pairs them by name — the
// aider repo-map model — so backends stay trivial.
type Extraction struct {
	Defs map[string][]Def          // file -> defs
	Refs map[string]map[string]int // file -> ident -> occurrence count
}

// RankedSymbol is one entry of the ranked symbol map.
type RankedSymbol struct {
	Def   Def
	Score float64
}

// MapRequest bounds one symbol-map build. Seeds come from the diff: the
// files it touches and the identifiers it defines or modifies.
type MapRequest struct {
	SeedFiles    []string // repo-relative changed files (personalization seeds)
	SeedIdents   []string // identifiers touched by the diff (edge-weight boost)
	BudgetTokens int      // approximate token budget for the rendered map
}

// BuildMap ranks the extraction against the request seeds and renders a
// budget-bounded symbol map for prompt injection. Deterministic for a given
// input. Returns "" when there is nothing worth injecting.
func BuildMap(ex *Extraction, req MapRequest) string {
	ranked := Rank(ex, req.SeedFiles, req.SeedIdents)
	if len(ranked) == 0 {
		return ""
	}
	budget := req.BudgetTokens
	if budget <= 0 {
		budget = DefaultMapTokens
	}
	return renderBudget(ranked, budget)
}

// DefaultMapTokens caps the rendered map. Keep well under the host's inline
// tool-result/injection comfort zone — an oversized blob gets externalized
// or compressed away, costing an extra read instead of saving one.
const DefaultMapTokens = 1024

// estimateTokens is a cheap chars/4 heuristic — the budget is approximate by
// design; precision here buys nothing.
func estimateTokens(s string) int { return len(s) / 4 }

// renderBudget renders the top-ranked symbols grouped by file, growing the
// cut until the budget is met (binary search on the prefix length).
func renderBudget(ranked []RankedSymbol, budgetTokens int) string {
	lo, hi := 1, len(ranked)
	best := ""
	for lo <= hi {
		mid := (lo + hi) / 2
		s := render(ranked[:mid])
		if estimateTokens(s) <= budgetTokens {
			best = s
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// render groups symbols by file (file order = best-ranked symbol first),
// one "L<line>: <signature>" per symbol, symbols within a file in line order.
func render(symbols []RankedSymbol) string {
	if len(symbols) == 0 {
		return ""
	}
	byFile := map[string][]RankedSymbol{}
	var fileOrder []string
	for _, rs := range symbols {
		f := rs.Def.File
		if _, ok := byFile[f]; !ok {
			fileOrder = append(fileOrder, f)
		}
		byFile[f] = append(byFile[f], rs)
	}
	var sb strings.Builder
	sb.WriteString("Symbols that EXIST in this repo, ranked by relevance to the change.\n")
	sb.WriteString("Reference them by these exact names (do not guess names):\n")
	for _, f := range fileOrder {
		fmt.Fprintf(&sb, "%s:\n", f)
		syms := byFile[f]
		sort.Slice(syms, func(i, j int) bool { return syms[i].Def.Line < syms[j].Def.Line })
		for _, rs := range syms {
			fmt.Fprintf(&sb, "  L%d: %s\n", rs.Def.Line, rs.Def.Signature)
		}
	}
	return sb.String()
}

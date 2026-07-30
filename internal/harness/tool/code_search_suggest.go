package tool

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Empty-search rescue: when a code_search returns zero hits the agent is
// usually guessing an identifier name that doesn't exist (measured on real
// review trajectories: ~half of all searches). A bare "No matches found"
// gives it nothing to climb on, so the next guess is just as blind — each
// miss costs a full LLM round. Instead we mine the repo for identifiers
// similar to the query and hand back candidates, turning a dead round into
// a corrected one.

const (
	suggestMaxParts      = 2    // longest identifier fragments to probe
	suggestMaxCandidates = 5    // suggestions returned to the model
	suggestMaxScanLines  = 5000 // hard cap on grep output processed
	suggestMinScore      = 0.3  // bigram-dice floor to avoid noise
)

// identLikeRe matches queries that look like a (possibly qualified)
// identifier — the only shape worth fuzzy-matching. Phrases, operators and
// regexes fall outside and get no suggestions.
var identLikeRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{2,79}$`)

// noMatchesWithSuggestions returns the zero-hit message, augmented with
// similar identifiers mined from the repo when the query is identifier-like.
// Best-effort: any failure degrades to the plain message.
func (p *CodeSearchProvider) noMatchesWithSuggestions(ctx context.Context, searchText string, usePerlRegexp bool, pathspec []string) string {
	const plain = "No matches found"
	if usePerlRegexp || !identLikeRe.MatchString(searchText) {
		return plain
	}
	cands := p.suggestIdentifiers(ctx, searchText, pathspec)
	if len(cands) == 0 {
		return plain
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "No matches found for %q.\n", searchText)
	sb.WriteString("Similar identifiers that DO exist in this repo — retry with one of these:\n")
	for _, c := range cands {
		fmt.Fprintf(&sb, "  %s (%d occurrence(s))\n", c.name, c.count)
	}
	return sb.String()
}

type candidate struct {
	name  string
	count int
	score float64
}

// suggestIdentifiers greps for the query's longest fragments and collects
// whole identifiers containing them, ranked by bigram similarity to the
// original query. Fragments (not the full query) are probed because the
// full query just returned zero hits — its parts are the recoverable signal.
func (p *CodeSearchProvider) suggestIdentifiers(ctx context.Context, query string, pathspec []string) []candidate {
	parts := identifierParts(query)
	if len(parts) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, part := range parts {
		// -o prints only the matched token; the pattern extends the fragment
		// to full identifier boundaries so we collect real symbol names.
		pattern := `[A-Za-z0-9_]*` + part + `[A-Za-z0-9_]*`
		cmdArgs := []string{"--no-pager", "grep", "-I", "-i", "-o", "-h", "-E",
			"--max-count", "20", "-e", pattern}
		if p.FileReader.Ref != "" {
			cmdArgs = append(cmdArgs, "--end-of-options", p.FileReader.Ref)
		} else {
			cmdArgs = append(cmdArgs, "--untracked")
		}
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, pathspec...)

		out, _, err := p.runGitGrep(ctx, cmdArgs)
		if err != nil && out == "" {
			continue
		}
		lines := strings.Split(out, "\n")
		if len(lines) > suggestMaxScanLines {
			lines = lines[:suggestMaxScanLines]
		}
		for _, tok := range lines {
			tok = strings.TrimSpace(tok)
			// Skip the degenerate world: empty, the fragment itself as a bare
			// word is fine, but tokens identical to the failed query would
			// have matched the original search already.
			if tok == "" || strings.EqualFold(tok, query) {
				continue
			}
			counts[tok]++
		}
	}

	lowerQuery := strings.ToLower(query)
	var cands []candidate
	for name, n := range counts {
		s := diceBigram(lowerQuery, strings.ToLower(name))
		if s >= suggestMinScore {
			cands = append(cands, candidate{name: name, count: n, score: s})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > suggestMaxCandidates {
		cands = cands[:suggestMaxCandidates]
	}
	return cands
}

// identifierParts splits an identifier query on case/underscore/dot
// boundaries and returns the longest fragments worth probing (len >= 4,
// deduped, longest first, capped at suggestMaxParts). Short fragments like
// "get"/"run" match half the repo and drown the ranking.
func identifierParts(query string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 4 {
			words = append(words, strings.ToLower(cur.String()))
		}
		cur.Reset()
	}
	runes := []rune(query)
	for i, r := range runes {
		switch {
		case r == '_' || r == '.':
			flush()
		case i > 0 && isUpper(r) && !isUpper(runes[i-1]):
			flush()
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()

	seen := map[string]bool{}
	var uniq []string
	for _, w := range words {
		if !seen[w] {
			seen[w] = true
			uniq = append(uniq, w)
		}
	}
	sort.Slice(uniq, func(i, j int) bool { return len(uniq[i]) > len(uniq[j]) })
	if len(uniq) > suggestMaxParts {
		uniq = uniq[:suggestMaxParts]
	}
	return uniq
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

// diceBigram is the Sørensen–Dice coefficient over character bigrams —
// cheap, order-aware-enough similarity for ranking identifier candidates.
func diceBigram(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	grams := map[string]int{}
	for i := 0; i+2 <= len(a); i++ {
		grams[a[i:i+2]]++
	}
	inter := 0
	for i := 0; i+2 <= len(b); i++ {
		g := b[i : i+2]
		if grams[g] > 0 {
			grams[g]--
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(a)+len(b)-2)
}

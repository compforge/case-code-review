package codegraph

import (
	"math"
	"sort"
)

// Ranking = the aider repo-map kernel, reshaped for review: build a
// file-level reference graph (referencer -> definer, paired by identifier),
// personalize PageRank on the diff's files, then split each file's rank
// across its out-edges to score individual (file, ident) definitions.
// File-level nodes keep the graph tiny (#files, not #symbols) while the
// distribution step still yields per-symbol scores.

const (
	dampingFactor  = 0.85
	prIterations   = 50
	prEpsilon      = 1e-8
	seedIdentBoost = 10.0 // ident touched by the diff — what review cares about
	commonPenalty  = 0.1  // ident defined in many files — near-zero signal
	commonDefFiles = 5
)

type edge struct {
	from, to string
	ident    string
	weight   float64
}

// Rank scores every definition by relevance to the seed files/idents.
// Result is sorted best-first and deterministic; empty (len 0) means "no
// cross-file signal" — callers should test len(), not nil.
//
// Seed files that contribute no defs/refs (pure-config or non-code files)
// drop out of the personalization vector; if none survive, PageRank falls
// back to uniform teleport — a deliberately silent degradation: the map is
// then merely less focused, never wrong.
func Rank(ex *Extraction, seedFiles, seedIdents []string) []RankedSymbol {
	if ex == nil || len(ex.Defs) == 0 {
		return nil
	}
	seedFile := map[string]bool{}
	for _, f := range seedFiles {
		seedFile[f] = true
	}
	seedIdent := map[string]bool{}
	for _, id := range seedIdents {
		seedIdent[id] = true
	}

	// definers[ident] = files defining it; defs[(file,ident)] for scoring.
	definers := map[string][]string{}
	type defKey struct{ file, ident string }
	defsByKey := map[defKey][]Def{}
	for f, defs := range ex.Defs {
		seen := map[string]bool{}
		for _, d := range defs {
			k := defKey{f, d.Ident}
			defsByKey[k] = append(defsByKey[k], d)
			if !seen[d.Ident] {
				seen[d.Ident] = true
				definers[d.Ident] = append(definers[d.Ident], f)
			}
		}
	}

	// Edges: file that references ident -> each file defining ident.
	var edges []edge
	nodes := map[string]bool{}
	for f := range ex.Defs {
		nodes[f] = true
	}
	for f := range ex.Refs {
		nodes[f] = true
	}
	for refFile, refCounts := range ex.Refs {
		for ident, n := range refCounts {
			defFiles := definers[ident]
			if len(defFiles) == 0 {
				continue
			}
			w := math.Sqrt(float64(n))
			if seedIdent[ident] {
				w *= seedIdentBoost
			}
			if len(defFiles) > commonDefFiles {
				w *= commonPenalty
			}
			for _, defFile := range defFiles {
				if defFile == refFile {
					continue // self-reference adds no cross-file signal
				}
				edges = append(edges, edge{from: refFile, to: defFile, ident: ident, weight: w})
			}
		}
	}
	if len(edges) == 0 {
		return nil
	}

	rank := pagerank(nodes, edges, seedFile)

	// Distribute each referencer's rank over its out-edges onto the defining
	// (file, ident) — the bridge from file rank to symbol choice.
	outTotal := map[string]float64{}
	for _, e := range edges {
		outTotal[e.from] += e.weight
	}
	score := map[defKey]float64{}
	for _, e := range edges {
		score[defKey{e.to, e.ident}] += rank[e.from] * e.weight / outTotal[e.from]
	}

	var out []RankedSymbol
	for k, s := range score {
		for _, d := range defsByKey[k] {
			out = append(out, RankedSymbol{Def: d, Score: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Def.File != out[j].Def.File {
			return out[i].Def.File < out[j].Def.File
		}
		return out[i].Def.Line < out[j].Def.Line
	})
	return out
}

// pagerank is a plain power iteration with personalization on seed files
// (dangling mass also redistributed to seeds, mirroring networkx's
// dangling=personalization used by aider).
func pagerank(nodes map[string]bool, edges []edge, seeds map[string]bool) map[string]float64 {
	n := len(nodes)
	if n == 0 {
		return nil
	}
	// Personalization: seeds share the teleport mass; no seeds -> uniform.
	pers := map[string]float64{}
	if len(seeds) > 0 {
		p := 1.0 / float64(len(seeds))
		for f := range seeds {
			if nodes[f] {
				pers[f] = p
			}
		}
	}
	if len(pers) == 0 {
		p := 1.0 / float64(n)
		for f := range nodes {
			pers[f] = p
		}
	}

	out := map[string][]edge{}
	outW := map[string]float64{}
	for _, e := range edges {
		out[e.from] = append(out[e.from], e)
		outW[e.from] += e.weight
	}

	rank := map[string]float64{}
	for f := range nodes {
		rank[f] = 1.0 / float64(n)
	}
	for range prIterations {
		next := map[string]float64{}
		dangling := 0.0
		for f, r := range rank {
			if outW[f] == 0 {
				dangling += r
				continue
			}
			for _, e := range out[f] {
				next[e.to] += dampingFactor * r * e.weight / outW[f]
			}
		}
		delta := 0.0
		for f := range nodes {
			teleport := (1 - dampingFactor) * pers[f]
			next[f] += teleport + dampingFactor*dangling*pers[f]
			delta += math.Abs(next[f] - rank[f])
		}
		rank = next
		if delta < prEpsilon {
			break
		}
	}
	return rank
}

// IsLikelySymbolName reports whether a diff-extracted token is worth using
// as a seed ident: identifier-shaped and not a stopword-short fragment.
func IsLikelySymbolName(s string) bool {
	if len(s) < 3 || len(s) > 80 {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '.':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

package sourcecontext

import (
	"fmt"
	"sort"
	"strings"

	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/language"
)

// Usage is a CodeGraph reference projected into bounded review context.
type Usage struct {
	Symbol string
	File   string
	Line   int
	Text   string
}

const (
	usagePerSymbolMax = 8
	usageTotalMax     = 40
)

func FindUsages(analyzer *language.Analyzer, symbolIDs []string, excludePaths map[string]bool) []Usage {
	index := analyzer.Repository()
	if index.Graph == nil {
		return nil
	}
	var out []Usage
	seen := map[string]bool{}
	for _, id := range symbolIDs {
		path, name, ok := language.SplitSymbolID(id)
		if !ok {
			continue
		}
		var candidates []Usage
		nodes := index.Graph.Find(path, "", name)
		if len(nodes) != 1 {
			continue
		}
		for _, node := range nodes {
			for _, edge := range index.Graph.RelationsTo(node.ID, cg.References, cg.Calls) {
				if !edge.Confidence.AtLeast(cg.Scoped) || excludePaths[edge.Location.Path] {
					continue
				}
				loc := edge.Location
				key := fmt.Sprintf("%s:%s:%d", id, loc.Path, loc.Line)
				if seen[key] {
					continue
				}
				seen[key] = true
				lines := strings.Split(index.Sources[loc.Path], "\n")
				if loc.Line < 1 || loc.Line > len(lines) {
					continue
				}
				candidates = append(candidates, Usage{Symbol: id, File: loc.Path, Line: loc.Line, Text: strings.TrimSpace(lines[loc.Line-1])})
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].File != candidates[j].File {
				return candidates[i].File < candidates[j].File
			}
			return candidates[i].Line < candidates[j].Line
		})
		out = append(out, candidates[:min(len(candidates), usagePerSymbolMax, usageTotalMax-len(out))]...)
		if len(out) == usageTotalMax {
			break
		}
	}
	return out
}

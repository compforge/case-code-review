package viewer

import (
	"encoding/json"
	"path"
	"strings"
)

// FileReadMetrics separates two different sources of waste: reads that the
// current prompt already covered, and repeated reads of the same path. The
// latter is only a diagnostic because different ranges may be intentional.
type FileReadMetrics struct {
	Calls           int
	UniqueFiles     int
	CoveredCalls    int
	SamePathRepeats int
	MaterialFiles   int
	UnitKnownFiles  int
	CallGraphFiles  int
}

func analyzeFileReads(r *ReviewRun) FileReadMetrics {
	metrics := FileReadMetrics{}
	readCounts := map[string]int{}
	for _, card := range r.Calls {
		for _, call := range card.ToolCalls {
			if call.Name != "file_read" {
				continue
			}
			metrics.Calls++
			if strings.HasPrefix(call.Result, "Already available in the current context") {
				metrics.CoveredCalls++
			}
			var args struct {
				FilePath string `json:"file_path"`
			}
			if json.Unmarshal([]byte(call.Arguments), &args) != nil {
				continue
			}
			filePath := cleanReviewPath(args.FilePath)
			if filePath != "" {
				readCounts[filePath]++
			}
		}
	}
	metrics.UniqueFiles = len(readCounts)
	for _, count := range readCounts {
		if count > 1 {
			metrics.SamePathRepeats += count - 1
		}
	}

	materials := map[string]bool{}
	for _, outcome := range r.Materials {
		for _, prefix := range []string{"whole ", "ranged "} {
			if strings.HasPrefix(outcome, prefix) {
				if filePath := cleanReviewPath(strings.TrimPrefix(outcome, prefix)); filePath != "" {
					materials[filePath] = true
				}
			}
		}
	}
	known := pathSet(r.Paths)
	callGraph := map[string]bool{}
	for relation, paths := range r.ContextPaths {
		for filePath := range pathSet(paths) {
			known[filePath] = true
			if relation == "caller" || relation == "callee" {
				callGraph[filePath] = true
			}
		}
	}
	for filePath := range readCounts {
		if materials[filePath] {
			metrics.MaterialFiles++
		}
		if known[filePath] {
			metrics.UnitKnownFiles++
		}
		if callGraph[filePath] {
			metrics.CallGraphFiles++
		}
	}
	return metrics
}

func pathSet(paths []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range paths {
		if filePath := cleanReviewPath(value); filePath != "" {
			out[filePath] = true
		}
	}
	return out
}

func cleanReviewPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = path.Clean(value)
	if value == "." {
		return ""
	}
	return value
}

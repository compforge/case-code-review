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
	Requests        int
	UniqueFiles     int
	CoveredRequests int
	SamePathRepeats int
	PreloadedFiles  int
	UnitKnownFiles  int
	CallGraphFiles  int
}

type fileReadArguments struct {
	Reads []fileReadArgument `json:"reads"`
	// Historical traces used a singular top-level shape. Viewer still reads
	// them, but the runtime tool accepts only reads[].
	FilePath string `json:"file_path"`
}

type fileReadArgument struct {
	FilePath string `json:"file_path"`
}

func analyzeFileReads(r *ReviewScope) FileReadMetrics {
	metrics := FileReadMetrics{}
	readCounts := map[string]int{}
	for _, card := range r.Calls {
		for _, call := range card.ToolCalls {
			if call.Name != "file_read" {
				continue
			}
			metrics.Calls++
			metrics.CoveredRequests += strings.Count(call.Result, "Already available in the current context")
			var args fileReadArguments
			if json.Unmarshal([]byte(call.Arguments), &args) != nil {
				continue
			}
			if len(args.Reads) == 0 && args.FilePath != "" {
				args.Reads = append(args.Reads, fileReadArgument{FilePath: args.FilePath})
			}
			metrics.Requests += len(args.Reads)
			for _, read := range args.Reads {
				filePath := cleanReviewPath(read.FilePath)
				if filePath != "" {
					readCounts[filePath]++
				}
			}
		}
	}
	metrics.UniqueFiles = len(readCounts)
	for _, count := range readCounts {
		if count > 1 {
			metrics.SamePathRepeats += count - 1
		}
	}

	preloads := map[string]bool{}
	for _, outcome := range r.SourcePreloads {
		for _, prefix := range []string{"whole ", "ranged "} {
			if strings.HasPrefix(outcome, prefix) {
				if filePath := cleanReviewPath(strings.TrimPrefix(outcome, prefix)); filePath != "" {
					preloads[filePath] = true
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
		if preloads[filePath] {
			metrics.PreloadedFiles++
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

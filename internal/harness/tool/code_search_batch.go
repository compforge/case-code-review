package tool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const codeSearchSharedHeader = "===== CODE_SEARCH SHARED SOURCE =====\n"
const codeSearchMaxLineBytes = 1024

// SplitCodeSearchSource separates shared source from per-query outcomes. Older
// session records without a shared section remain readable.
func SplitCodeSearchSource(result string) (queries, source string) {
	before, after, ok := strings.Cut(result, codeSearchSharedHeader)
	if !ok {
		return result, ""
	}
	return before, after
}

// AppendCodeSearchSource preserves the single tool-call/result envelope.
func AppendCodeSearchSource(queries, source string) string {
	if source == "" {
		return queries
	}
	return queries + codeSearchSharedHeader + source
}

type searchSourceFile struct {
	path  string
	lines map[int]string
}

// MergeCodeSearchResults keeps query identity and outcomes separate from source.
// Within a batch, a file/line is emitted once even when it was both a hit and
// context, or several queries resolved to the same enclosing symbol.
func MergeCodeSearchResults(results []string) string {
	if len(results) == 0 {
		return ""
	}
	files := make(map[string]*searchSourceFile)
	var order []*searchSourceFile
	var ranges []CodeSearchSourceRange
	parts := make([]string, len(results))
	for i, result := range results {
		var meta strings.Builder
		var file *searchSourceFile
		context := false
		selectFile := func(path string) {
			file = files[path]
			if file == nil {
				file = &searchSourceFile{path: path, lines: make(map[int]string)}
				files[path] = file
				order = append(order, file)
			}
		}
		for _, line := range strings.Split(result, "\n") {
			switch {
			case strings.HasPrefix(line, "File: "):
				selectFile(strings.TrimPrefix(line, "File: "))
				context = false
			case strings.HasPrefix(line, codeSearchSymbolSourcePrefix):
				var r CodeSearchSourceRange
				if json.Unmarshal([]byte(strings.TrimPrefix(line, codeSearchSymbolSourcePrefix)), &r) == nil {
					selectFile(r.Path)
					ranges = append(ranges, r)
				}
				context = true
				continue
			case line == "Context:" || strings.HasPrefix(line, "LINE_RANGE: "):
				context = true
				continue
			}
			number, content, numbered := strings.Cut(line, "|")
			n, err := strconv.Atoi(number)
			if numbered && err == nil && n > 0 && file != nil {
				file.lines[n] = content
				if !context {
					fmt.Fprintf(&meta, "Hit line: %d (see shared source)\n", n)
				}
				continue
			}
			meta.WriteString(line)
			meta.WriteByte('\n')
		}
		// Reserve half the result for source. Each query retains an independent
		// allowance so a broad first query cannot erase a later error or no-match.
		parts[i] = LimitResult(strings.TrimSpace(meta.String()), MaxResultBytes/2/len(results)-64)
	}
	queries := EncodeCodeSearchResults(parts)
	var source strings.Builder
	// Leave room for the explicit omission notice and the shared envelope.
	budget := MaxResultBytes - len(queries) - len(codeSearchSharedHeader) - 256
	complete := make(map[string]map[int]bool)
	omitted := false
	for _, file := range order {
		numbers := make([]int, 0, len(file.lines))
		for n := range file.lines {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		header := "File: " + file.path + "\n"
		if source.Len()+len(header) > budget {
			omitted = true
			continue
		}
		source.WriteString(header)
		complete[file.path] = make(map[int]bool)
		previous := -1
		for _, n := range numbers {
			original := file.lines[n]
			content := LimitResult(original, codeSearchMaxLineBytes)
			content = strings.TrimRight(content, "\n")
			row := fmt.Sprintf("%d|%s\n", n, content)
			if n != previous+1 {
				row = "...\n" + row
			}
			if source.Len()+len(row) > budget {
				omitted = true
				continue
			}
			source.WriteString(row)
			complete[file.path][n] = len(original) <= codeSearchMaxLineBytes
			previous = n
		}
	}
	// Source-range receipts drive read deduplication. Advertise a range only when
	// every byte of every line survived; clipped source must remain re-readable.
	seen := make(map[CodeSearchSourceRange]bool)
	for _, r := range ranges {
		if seen[r] || r.StartLine < 1 || r.EndLine < r.StartLine {
			continue
		}
		seen[r] = true
		visible := complete[r.Path]
		if r.EndLine-r.StartLine+1 > len(visible) {
			continue
		}
		full := true
		for n := r.StartLine; n <= r.EndLine; n++ {
			if !visible[n] {
				full = false
				break
			}
		}
		if !full {
			continue
		}
		data, _ := json.Marshal(r)
		receipt := codeSearchSymbolSourcePrefix + string(data) + "\n"
		if source.Len()+len(receipt) <= budget {
			source.WriteString(receipt)
		}
	}
	if omitted {
		source.WriteString("[Output truncated: shared source exceeds the batch byte budget. Narrow file_patterns/query or read a smaller source range.]\n")
	}
	return AppendCodeSearchSource(queries, source.String())
}

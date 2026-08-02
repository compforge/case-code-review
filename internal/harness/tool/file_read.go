package tool

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"sync"
)

// FileReadMaxLines is the largest range returned for one read_files batch member.
// Context coverage checks use the same limit to recognize an exact repeat of
// an unbounded read without executing the provider again.
const FileReadMaxLines = 500

// FileReadMaxBatchLines bounds source lines returned by one batch. Without a
// shared budget, a valid 16-member call could inject 8,000 lines in one turn.
const FileReadMaxBatchLines = 2000

// FileReadMaxBatch bounds fan-out from one model response. Commit/range reads
// may spawn git subprocesses; the model should issue another batch if needed.
const FileReadMaxBatch = 16

// FileReadRequest is one range in read_files's batch-only request contract.
// Even a single range is carried in reads[] so the model has one stable shape
// and can batch independent reads without spending extra turns.
type FileReadRequest struct {
	FilePath  string
	StartLine int
	EndLine   int
}

// ParseFileReadRequests parses the provider's only contract: reads[]. The
// model boundary may wrap one direct item before it reaches this package, but
// providers and domain handlers never carry a second argument shape.
func ParseFileReadRequests(args map[string]any) ([]FileReadRequest, error) {
	values, ok := args["reads"].([]any)
	if !ok || len(values) == 0 {
		return nil, fmt.Errorf("reads must be a non-empty array")
	}
	if len(values) > FileReadMaxBatch {
		return nil, fmt.Errorf("reads may contain at most %d items", FileReadMaxBatch)
	}
	requests := make([]FileReadRequest, len(values))
	for i, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reads[%d] must be an object", i)
		}
		requests[i] = FileReadRequest{
			FilePath:  stringValue(item["file_path"]),
			StartLine: intValue(item["start_line"]),
			EndLine:   intValue(item["end_line"]),
		}
	}
	return requests, nil
}

// FileReadArgs rebuilds the public tool arguments for a subset of requests.
// Context middleware uses it after removing ranges already visible to the
// model, while preserving the same batch-only provider contract.
func FileReadArgs(requests []FileReadRequest) map[string]any {
	reads := make([]any, len(requests))
	for i, request := range requests {
		item := map[string]any{"file_path": request.FilePath}
		if request.StartLine > 0 {
			item["start_line"] = request.StartLine
		}
		if request.EndLine > 0 {
			item["end_line"] = request.EndLine
		}
		reads[i] = item
	}
	return map[string]any{"reads": reads}
}

var fileReadBatchHeader = regexp.MustCompile(`(?m)^===== FILE_READ RESULT \d+/\d+ =====\n`)

// EncodeFileReadResults joins ordered per-range results into one tool result.
// One LLM tool call must still receive exactly one tool-result message.
func EncodeFileReadResults(results []string) string {
	var out strings.Builder
	for i, result := range results {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "===== FILE_READ RESULT %d/%d =====\n", i+1, len(results))
		out.WriteString(strings.TrimRight(result, "\n"))
		out.WriteByte('\n')
	}
	return out.String()
}

// DecodeFileReadResults splits the stable batch envelope while leaving each
// read_files result body unchanged.
func DecodeFileReadResults(result string) ([]string, bool) {
	matches := fileReadBatchHeader.FindAllStringIndex(result, -1)
	if len(matches) == 0 || matches[0][0] != 0 {
		return nil, false
	}
	items := make([]string, len(matches))
	for i, match := range matches {
		end := len(result)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		items[i] = strings.TrimSpace(result[match[1]:end])
	}
	return items, true
}

// DiffPaths records what this review's diff did to paths that no longer exist
// at the review ref (renames and deletions). A read_files miss on such a path
// is the model following a stale reference from the diff itself (rename
// headers, leftover imports), so it gets redirected or explained instead of
// surfacing a raw git error the model can't act on. Frozen after
// construction; safe for concurrent reads.
type DiffPaths struct {
	renamedTo map[string]string // old path -> new path
	deleted   map[string]bool   // old path -> removed in this diff
}

// NewDiffPaths creates a frozen DiffPaths from plain maps.
func NewDiffPaths(renamedTo map[string]string, deleted map[string]bool) DiffPaths {
	r := make(map[string]string, len(renamedTo))
	maps.Copy(r, renamedTo)
	d := make(map[string]bool, len(deleted))
	maps.Copy(d, deleted)
	return DiffPaths{renamedTo: r, deleted: d}
}

// FileReadProvider reads independent file ranges concurrently as one tool call.
type FileReadProvider struct {
	FileReader *FileReader
	diffPaths  DiffPaths
	identity   Tool
}

func NewFileRead(fr *FileReader) *FileReadProvider { return &FileReadProvider{FileReader: fr} }

// NewFileReadBase creates the baseline counterpart of read_files. Runner sets
// its immutable base ref after resolving workspace/range/commit semantics and
// before freezing the registry.
func NewFileReadBase(fr *FileReader) *FileReadProvider {
	return &FileReadProvider{FileReader: fr, identity: FileReadBase}
}

func (p *FileReadProvider) SetRef(ref string) {
	p.FileReader.Mode = ModeCommit
	p.FileReader.Ref = ref
}

// SetDiffPaths installs the rename/delete map for this run. Must be called
// before concurrent access begins (same contract as FileReadDiffProvider.SetDiffMap).
func (p *FileReadProvider) SetDiffPaths(dp DiffPaths) {
	p.diffPaths = dp
}

func (p *FileReadProvider) Tool() Tool {
	if p.identity.IsKnown() {
		return p.identity
	}
	return FileRead
}

func (p *FileReadProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	requests, err := ParseFileReadRequests(args)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	memberLimit := min(FileReadMaxLines, max(1, FileReadMaxBatchLines/len(requests)))
	results := make([]string, len(requests))
	var wg sync.WaitGroup
	for i, request := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := p.executeOne(ctx, request, memberLimit)
			if err != nil {
				result = "Error: " + err.Error()
			}
			results[i] = result
		}()
	}
	wg.Wait()
	return EncodeFileReadResults(results), nil
}

func (p *FileReadProvider) executeOne(
	ctx context.Context,
	request FileReadRequest,
	memberLimit int,
) (string, error) {
	filePath := request.FilePath
	if filePath == "" {
		return "Error: file_path is required", nil
	}
	if p.Tool() == FileReadBase && p.FileReader.Ref == "" {
		return "Baseline is the empty tree; the requested file did not exist before this change.", nil
	}

	startLine := request.StartLine
	endLine := request.EndLine
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 {
		endLine = 0
	}

	maxLines := memberLimit
	if endLine > 0 {
		requested := endLine - startLine + 1
		if requested <= 0 {
			// A reversed range is a model tool-call mistake (same class as a
			// missing file_path above), not a fatal condition. Return it as a
			// recoverable observation so the loop feeds it back and the model
			// can retry with a corrected range, instead of aborting the review.
			return fmt.Sprintf("Error: invalid line range: start_line %d is greater than end_line %d", startLine, endLine), nil
		}
		if requested < maxLines {
			maxLines = requested
		}
	}

	lines, totalLines, err := p.FileReader.ReadLines(ctx, filePath, startLine, maxLines)
	var renameNote string
	if err != nil {
		// The miss may be the model chasing a path this very diff moved or
		// removed (rename headers and stale imports keep the old path visible).
		if to, ok := p.diffPaths.renamedTo[filePath]; ok {
			renameNote = fmt.Sprintf("NOTE: %q was renamed to %q in this diff; showing the renamed file.\n", filePath, to)
			filePath = to
			lines, totalLines, err = p.FileReader.ReadLines(ctx, filePath, startLine, maxLines)
		} else if p.diffPaths.deleted[filePath] {
			return fmt.Sprintf("File %q was deleted in this diff; it no longer exists at the review ref. Use file_read_diff to see the removed content.", filePath), nil
		}
	}
	if err != nil && p.Tool() == FileReadBase {
		return fmt.Sprintf("File %q did not exist at baseline ref %s.", filePath, p.FileReader.Ref), nil
	}
	if err != nil {
		return "", fmt.Errorf("file %q not found: %w", filePath, err)
	}

	if totalLines > 0 && startLine-1 >= totalLines {
		return "", fmt.Errorf("file %q has only %d lines, requested range %d-%d", filePath, totalLines, startLine, endLine)
	}

	effectiveEnd := totalLines
	if endLine > 0 && endLine < effectiveEnd {
		effectiveEnd = endLine
	}
	fullRange := effectiveEnd - (startLine - 1)
	truncated := fullRange > maxLines

	displayEnd := startLine - 1 + len(lines)

	var sb strings.Builder
	if p.Tool() == FileReadBase {
		sb.WriteString(fmt.Sprintf("Baseline ref: %s\n", p.FileReader.Ref))
	}
	sb.WriteString(renameNote)
	sb.WriteString(fmt.Sprintf("File: %s (Total lines: %d)\n", filePath, totalLines))
	sb.WriteString(fmt.Sprintf("IS_TRUNCATED: %t\n", truncated))
	sb.WriteString(fmt.Sprintf("LINE_RANGE: %d-%d\n", startLine, displayEnd))
	for i, line := range lines {
		sb.WriteString(fmt.Sprintf("%d|%s\n", startLine+i, line))
	}
	if truncated {
		if memberLimit < FileReadMaxLines {
			sb.WriteString(fmt.Sprintf("\nNote: Results truncated to %d lines to keep this batch within the %d-line output budget. Please narrow your line range.\n", memberLimit, FileReadMaxBatchLines))
		} else {
			sb.WriteString(fmt.Sprintf("\nNote: Results truncated to %d lines. Please narrow your line range.\n", FileReadMaxLines))
		}
	}
	return sb.String(), nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	default:
		return 0
	}
}

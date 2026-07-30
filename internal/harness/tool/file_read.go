package tool

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

const fileReadMaxLines = 500

// DiffPaths records what this review's diff did to paths that no longer exist
// at the review ref (renames and deletions). A file_read miss on such a path
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

// FileReadProvider reads file content at a given path and optional line range.
type FileReadProvider struct {
	FileReader *FileReader
	diffPaths  DiffPaths
}

func NewFileRead(fr *FileReader) *FileReadProvider { return &FileReadProvider{FileReader: fr} }

// SetDiffPaths installs the rename/delete map for this run. Must be called
// before concurrent access begins (same contract as FileReadDiffProvider.SetDiffMap).
func (p *FileReadProvider) SetDiffPaths(dp DiffPaths) {
	p.diffPaths = dp
}

func (p *FileReadProvider) Tool() Tool { return FileRead }

func (p *FileReadProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return "Error: file_path is required", nil
	}

	startLine, hasStart := args["start_line"].(float64)
	endLine, hasEnd := args["end_line"].(float64)
	if !hasStart || startLine <= 0 {
		startLine = 1
	}
	if !hasEnd || endLine <= 0 {
		endLine = 0
	}

	maxLines := fileReadMaxLines
	if endLine > 0 {
		requested := int(endLine) - int(startLine) + 1
		if requested <= 0 {
			// A reversed range is a model tool-call mistake (same class as a
			// missing file_path above), not a fatal condition. Return it as a
			// recoverable observation so the loop feeds it back and the model
			// can retry with a corrected range, instead of aborting the review.
			return fmt.Sprintf("Error: invalid line range: start_line %d is greater than end_line %d", int(startLine), int(endLine)), nil
		}
		if requested < maxLines {
			maxLines = requested
		}
	}

	lines, totalLines, err := p.FileReader.ReadLines(ctx, filePath, int(startLine), maxLines)
	var renameNote string
	if err != nil {
		// The miss may be the model chasing a path this very diff moved or
		// removed (rename headers and stale imports keep the old path visible).
		if to, ok := p.diffPaths.renamedTo[filePath]; ok {
			renameNote = fmt.Sprintf("NOTE: %q was renamed to %q in this diff; showing the renamed file.\n", filePath, to)
			filePath = to
			lines, totalLines, err = p.FileReader.ReadLines(ctx, filePath, int(startLine), maxLines)
		} else if p.diffPaths.deleted[filePath] {
			return fmt.Sprintf("File %q was deleted in this diff; it no longer exists at the review ref. Use file_read_diff to see the removed content.", filePath), nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("file %q not found: %w", filePath, err)
	}

	if totalLines > 0 && int(startLine)-1 >= totalLines {
		return "", fmt.Errorf("file %q has only %d lines, requested range %d-%d", filePath, totalLines, int(startLine), int(endLine))
	}

	effectiveEnd := totalLines
	if endLine > 0 && int(endLine) < effectiveEnd {
		effectiveEnd = int(endLine)
	}
	fullRange := effectiveEnd - (int(startLine) - 1)
	truncated := fullRange > fileReadMaxLines

	displayEnd := int(startLine) - 1 + len(lines)

	var sb strings.Builder
	sb.WriteString(renameNote)
	sb.WriteString(fmt.Sprintf("File: %s (Total lines: %d)\n", filePath, totalLines))
	sb.WriteString(fmt.Sprintf("IS_TRUNCATED: %t\n", truncated))
	sb.WriteString(fmt.Sprintf("LINE_RANGE: %d-%d\n", int(startLine), displayEnd))
	for i, line := range lines {
		sb.WriteString(fmt.Sprintf("%d|%s\n", int(startLine)+i, line))
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("\nNote: Results truncated to %d lines. Please narrow your line range.\n", fileReadMaxLines))
	}
	return sb.String(), nil
}

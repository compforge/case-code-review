package hypothesisreview

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

// EvidenceReceipt proves that the convergent reviewer actually obtained a
// repository fact. It is produced by CCR while executing a read-only tool; the
// model can describe evidence, but cannot mint receipts.
type EvidenceReceipt struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
}

// EvidenceLedger starts from the Lane's retained receipts and appends facts
// read by the current Hypothesis Review.
type EvidenceLedger struct {
	mu       sync.Mutex
	receipts []EvidenceReceipt
}

func (l *EvidenceLedger) Record(request harness.ToolRequest, result string) {
	receipts := receiptsFor(request, result)
	l.mu.Lock()
	l.receipts = append(l.receipts, receipts...)
	l.mu.Unlock()
}

func (l *EvidenceLedger) Receipts() []EvidenceReceipt {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]EvidenceReceipt(nil), l.receipts...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].Ref < out[j].Ref
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func receiptsFor(request harness.ToolRequest, result string) []EvidenceReceipt {
	base := EvidenceReceipt{ToolCallID: request.Call.ID}
	switch request.Tool {
	case tool.FileReadDiff:
		base.Kind = "diff"
		var out []EvidenceReceipt
		for _, path := range stringSlice(request.Args["path_array"]) {
			// file_read_diff accepts several paths and omits misses. Receipt only
			// the paths whose diff block was actually returned.
			if !strings.Contains(result, "==== FILE: "+path+" ====") {
				continue
			}
			receipt := base
			receipt.Ref = path
			out = append(out, receipt)
		}
		return out
	case tool.FileReadBase:
		return fileReadReceipts(base, "base", request.Args, result)
	case tool.FileRead:
		return fileReadReceipts(base, "source", request.Args, result)
	case tool.CodeSearch:
		return codeSearchReceipts(base, request.Args, result)
	case tool.FileFind:
		base.Kind, base.Ref = "discovery", stringValue(request.Args, "query_name")
	default:
		return nil
	}
	if base.Ref == "" {
		return nil
	}
	return []EvidenceReceipt{base}
}

func codeSearchReceipts(
	base EvidenceReceipt,
	args map[string]any,
	result string,
) []EvidenceReceipt {
	requests, err := tool.ParseCodeSearchRequests(args)
	if err != nil {
		return nil
	}
	parts, ok := tool.DecodeCodeSearchResults(result)
	if !ok || len(parts) != len(requests) {
		return nil
	}
	var receipts []EvidenceReceipt
	for i, request := range requests {
		part := strings.TrimSpace(parts[i])
		if request.SearchText == "" || part == "" || strings.HasPrefix(part, "Error:") ||
			strings.HasPrefix(part, "search_code timed out") {
			continue
		}
		receipt := base
		receipt.Kind = "search"
		receipt.Ref = request.SearchText
		receipts = append(receipts, receipt)
	}
	return receipts
}

func fileReadReceipts(
	base EvidenceReceipt,
	kind string,
	args map[string]any,
	result string,
) []EvidenceReceipt {
	requests, err := tool.ParseFileReadRequests(args)
	if err != nil {
		return nil
	}
	parts, ok := tool.DecodeFileReadResults(result)
	if !ok || len(parts) != len(requests) {
		return nil
	}
	var receipts []EvidenceReceipt
	for i, request := range requests {
		// Only a returned File block proves that repository content was read;
		// deletion, missing-file, and context-coverage notices do not mint new
		// evidence receipts for the batch member.
		if request.FilePath == "" || !strings.Contains(parts[i], "File: ") {
			continue
		}
		receipt := base
		receipt.Kind = kind
		receipt.Ref = request.FilePath
		receipts = append(receipts, receipt)
	}
	return receipts
}

// ReviewHandler owns the convergent Review's complete tool boundary. Read-only
// providers are executed here so their successful use can be receipted before
// submit_assessments is accepted; no shell or write provider can enter through
// this handler.
type ReviewHandler struct {
	Assessments *AssessmentHook
	Evidence    *EvidenceLedger
	Tools       *tool.Registry
}

func (h *ReviewHandler) HandleTool(
	ctx context.Context,
	request harness.ToolRequest,
) (tool.TaskCheckpoint, bool) {
	if request.Tool == SubmitAssessments {
		return h.Assessments.HandleTool(ctx, request)
	}
	if !isEvidenceTool(request.Tool) {
		return tool.TaskCheckpoint{}, false
	}
	if h.Tools == nil {
		return tool.Of(tool.NotAvailableMsg), true
	}
	provider, ok := h.Tools.Get(request.Tool.Name())
	if !ok {
		return tool.Of(tool.NotAvailableMsg), true
	}
	result, err := provider.Execute(ctx, request.Args)
	if err != nil {
		return tool.Of(fmt.Sprintf("Error: %v", err)), true
	}
	if h.Evidence != nil && !strings.HasPrefix(strings.TrimSpace(result), "Error:") {
		h.Evidence.Record(request, result)
	}
	return tool.Of(result), true
}

func isEvidenceTool(candidate tool.Tool) bool {
	return candidate == tool.FileRead || candidate == tool.FileReadBase ||
		candidate == tool.FileReadDiff || candidate == tool.CodeSearch ||
		candidate == tool.FileFind
}

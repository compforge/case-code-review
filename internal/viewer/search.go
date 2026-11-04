package viewer

import (
	"encoding/json"
	"github.com/compforge/agent-ledger/go/observation"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"strings"
)

type searchResult struct {
	Query    string   `json:"query"`
	Scope    []string `json:"scope,omitempty"`
	Status   string   `json:"status"`
	Evidence string   `json:"evidence"`
}

// Search batches can complete with item-local errors; the outer tool status
// does not establish successful evidence collection.
// +spec=`Each search query preserves its own outcome; scope-empty and unavailable evidence never become valid negative evidence`
func searchResults(call *observation.ToolCall) []searchResult {
	if call.Request == nil || call.Request.ToolName != "search_code" {
		return nil
	}
	var args map[string]any
	if json.Unmarshal(call.Request.Input, &args) != nil {
		return nil
	}
	requests, err := tool.ParseCodeSearchRequests(args)
	if err != nil {
		return nil
	}
	var parts []string
	if call.Response != nil {
		var output agentsession.ToolResultData
		if json.Unmarshal(call.Response.Output, &output) == nil {
			var content string
			if json.Unmarshal(output.Content, &content) == nil {
				parts, _ = tool.DecodeCodeSearchResults(content)
			}
		}
	}
	result := make([]searchResult, 0, len(requests))
	for i, request := range requests {
		item := searchResult{Query: request.SearchText, Scope: request.FilePatterns, Status: "incomplete"}
		if i < len(parts) {
			item.Evidence = parts[i]
			if outcome, ok := tool.ParseCodeSearchOutcome(parts[i]); ok {
				item.Status = outcome.Status
			} else if strings.HasPrefix(strings.TrimSpace(parts[i]), "Error:") {
				item.Status = "error"
			} else if len(tool.CodeSearchResultPaths(parts[i])) > 0 {
				item.Status = "hits"
			}
		}
		result = append(result, item)
	}
	return result
}

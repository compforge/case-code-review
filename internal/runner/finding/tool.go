package finding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

var CodeComment = tool.Named("code_comment")

const submitSucceeded = "Successfully commented."

const WrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop investigating now. Based only on the evidence you already gathered: call code_comment for each issue you are confident about, then call task_done. If you found no issues in what you managed to review, call task_done and state explicitly which parts you reviewed. Do not call any other tools."

// ToolProvider exposes the review-domain code_comment tool through the generic
// Harness registry.
type ToolProvider struct {
	Collector *Collector
}

func (p *ToolProvider) Tool() tool.Tool { return CodeComment }

func (p *ToolProvider) Execute(_ context.Context, args map[string]any) (string, error) {
	if p.Collector == nil {
		return "Error: comment collector is not configured", nil
	}

	comments, errMsg := ParseComments(args)
	if errMsg != "" {
		return errMsg, nil
	}

	for i := range comments {
		p.Collector.Add(comments[i])
	}
	return submitSucceeded, nil
}

// ParseComments extracts findings from tool call arguments without writing to the
// Collector. Returns parsed findings and an error message (empty on success).
func ParseComments(args map[string]any) ([]Finding, string) {
	var rawComments []any
	if arr, ok := args["comments"].([]any); ok && len(arr) > 0 {
		rawComments = arr
	} else if s, ok := args["comments"].(string); ok && s != "" {
		if err := json.Unmarshal([]byte(s), &rawComments); err != nil {
			return nil, fmt.Sprintf("Error: failed to parse 'comments' JSON string: %v", err)
		}
	}
	if len(rawComments) == 0 {
		raw, _ := json.Marshal(args)
		return nil, fmt.Sprintf("Error: 'comments' array is required. Got args: %s", string(raw))
	}

	var comments []Finding
	for _, raw := range rawComments {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		cm := Finding{}

		if content, ok := obj["content"].(string); ok {
			cm.Content = content
		}
		if suggestion, ok := obj["suggestion_code"].(string); ok {
			cm.SuggestionCode = suggestion
		}
		if existing, ok := obj["existing_code"].(string); ok {
			cm.ExistingCode = existing
		}
		if thinking, ok := obj["thinking"].(string); ok {
			cm.Thinking = thinking
		}
		if category, ok := obj["category"].(string); ok {
			cm.Category = normalizeEnum(category, validCategories)
		}
		if severity, ok := obj["severity"].(string); ok {
			cm.Severity = normalizeEnum(severity, validSeverities)
		}
		if path, ok := args["path"].(string); ok {
			cm.Path = path
		}

		if cm.Path == "" || cm.Content == "" {
			continue
		}

		comments = append(comments, cm)
	}
	return comments, ""
}

// 结构化字段词表（与 tools.json 的 enum 同步）。归一化在解析口做而不是交给下游：
// 越界值置空比透传更好——自由词会把消费方的分组/门禁碎片化，空值至少诚实。
var (
	validCategories = map[string]bool{
		"bug": true, "security": true, "performance": true, "maintainability": true,
		"test": true, "style": true, "documentation": true, "other": true,
	}
	validSeverities = map[string]bool{
		"critical": true, "high": true, "medium": true, "low": true,
	}
)

func normalizeEnum(v string, valid map[string]bool) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if valid[v] {
		return v
	}
	return ""
}

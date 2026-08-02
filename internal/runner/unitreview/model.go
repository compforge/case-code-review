package unitreview

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/unit"
)

type Hypothesis = unit.Hypothesis

// FindingFor converts an approved Hypothesis into the public result shape.
func FindingFor(h Hypothesis) finding.Finding {
	return finding.Finding{
		HypothesisID:   h.ID,
		Path:           h.Path,
		Content:        h.Content,
		SuggestionCode: h.SuggestionCode,
		ExistingCode:   h.ExistingCode,
		StartLine:      h.StartLine,
		EndLine:        h.EndLine,
		Thinking:       h.Thinking,
		Alias:          h.Alias,
		Category:       h.Category,
		Severity:       h.Severity,
	}
}

func IDFor(h Hypothesis) string { return unit.HypothesisIDFor(h) }

func FingerprintFor(h Hypothesis) string { return unit.HypothesisFingerprintFor(h) }

// ParseHypotheses parses the atomic Unit Review submission without storing it.
// An explicitly empty array is a valid completed review with no hypotheses.
func ParseHypotheses(args map[string]any) ([]Hypothesis, string) {
	raw, present := args["hypotheses"]
	if !present {
		return nil, "Error: 'hypotheses' array is required"
	}
	var rawItems []any
	switch raw := raw.(type) {
	case []any:
		rawItems = raw
	case string:
		if err := json.Unmarshal([]byte(raw), &rawItems); err != nil {
			return nil, fmt.Sprintf("Error: failed to parse 'hypotheses' JSON string: %v", err)
		}
	default:
		return nil, "Error: 'hypotheses' must be an array"
	}
	if len(rawItems) == 0 {
		return nil, ""
	}

	out := make([]Hypothesis, 0, len(rawItems))
	for i, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("Error: hypothesis %d must be an object", i+1)
		}
		h := Hypothesis{
			Path:              stringValue(item, "path"),
			Content:           stringValue(item, "content"),
			SuggestionCode:    stringValue(item, "suggestion_code"),
			ExistingCode:      stringValue(item, "existing_code"),
			Trigger:           stringValue(item, "trigger"),
			Impact:            stringValue(item, "impact"),
			ChangeAttribution: stringValue(item, "change_attribution"),
			Uncertainty:       stringValue(item, "uncertainty"),
			Thinking:          stringValue(item, "thinking"),
			Category:          normalizeEnum(stringValue(item, "category"), validCategories),
			Severity:          normalizeEnum(stringValue(item, "severity"), validSeverities),
			Evidence:          stringSlice(item["evidence"]),
		}
		if h.Path == "" || h.Content == "" || h.ExistingCode == "" ||
			h.Trigger == "" || h.Impact == "" || h.ChangeAttribution == "" ||
			len(h.Evidence) == 0 || h.Category == "" || h.Severity == "" {
			return nil, fmt.Sprintf("Error: hypothesis %d is incomplete or has an invalid category/severity", i+1)
		}
		h.Fingerprint = FingerprintFor(h)
		out = append(out, h)
	}
	return out, ""
}

func stringValue(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return strings.TrimSpace(value)
}

func stringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if values, ok := value.([]string); ok {
			return values
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

func normalizeEnum(value string, allowed map[string]bool) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if allowed[value] {
		return value
	}
	return ""
}

var (
	validCategories = map[string]bool{
		"bug": true, "security": true, "performance": true, "maintainability": true,
		"test": true, "style": true, "documentation": true, "other": true,
	}
	validSeverities = map[string]bool{
		"critical": true, "high": true, "medium": true, "low": true,
	}
)

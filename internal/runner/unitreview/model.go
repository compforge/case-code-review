package unitreview

import (
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

// ParseHypothesis validates one incremental Unit Review result without storing
// it. One tool call represents one mature claim so it can enter Review 2
// immediately instead of waiting for a batch.
func ParseHypothesis(args map[string]any) (Hypothesis, string) {
	h := Hypothesis{
		Path:              stringValue(args, "path"),
		Content:           stringValue(args, "content"),
		SuggestionCode:    stringValue(args, "suggestion_code"),
		ExistingCode:      stringValue(args, "existing_code"),
		Trigger:           stringValue(args, "trigger"),
		Impact:            stringValue(args, "impact"),
		ChangeAttribution: stringValue(args, "change_attribution"),
		Uncertainty:       stringValue(args, "uncertainty"),
		Thinking:          stringValue(args, "thinking"),
		Category:          normalizeEnum(stringValue(args, "category"), validCategories),
		Severity:          normalizeEnum(stringValue(args, "severity"), validSeverities),
		Evidence:          stringSlice(args["evidence"]),
	}
	if h.Path == "" || h.Content == "" || h.ExistingCode == "" ||
		h.Trigger == "" || h.Impact == "" || h.ChangeAttribution == "" ||
		len(h.Evidence) == 0 || h.Category == "" || h.Severity == "" {
		return Hypothesis{}, "Error: hypothesis is incomplete or has an invalid category/severity"
	}
	h.Fingerprint = FingerprintFor(h)
	return h, ""
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

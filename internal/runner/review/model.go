package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/qiankunli/case-code-review/internal/runner/finding"
)

// Hypothesis is a falsifiable issue claim produced by the investigative Unit
// review. It is deliberately not a Finding: an independent Review must assess
// both its evidential support and delivery value before Trial can publish it.
type Hypothesis struct {
	ID                string   `json:"id"`
	OriginUnit        string   `json:"origin_unit,omitempty"`
	Path              string   `json:"path"`
	Content           string   `json:"content"`
	SuggestionCode    string   `json:"suggestion_code,omitempty"`
	ExistingCode      string   `json:"existing_code,omitempty"`
	StartLine         int      `json:"start_line,omitempty"`
	EndLine           int      `json:"end_line,omitempty"`
	Trigger           string   `json:"trigger,omitempty"`
	Impact            string   `json:"impact,omitempty"`
	ChangeAttribution string   `json:"change_attribution,omitempty"`
	Evidence          []string `json:"evidence,omitempty"`
	Uncertainty       string   `json:"uncertainty,omitempty"`
	Thinking          string   `json:"thinking,omitempty"`
	Alias             string   `json:"alias,omitempty"`
	Category          string   `json:"category,omitempty"`
	Severity          string   `json:"severity,omitempty"`
}

// Finding converts an approved Hypothesis into the public result shape.
func (h Hypothesis) Finding() finding.Finding {
	return finding.Finding{
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

// IDFor returns the content-stable identity used by Assessment to refer to a
// Hypothesis. Identical proposals from sibling Units intentionally share an ID
// so Trial also removes duplicate delivery.
func IDFor(h Hypothesis) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		h.Path,
		h.Content,
		h.ExistingCode,
		h.Trigger,
		h.Impact,
		h.ChangeAttribution,
	}, "\x00")))
	return "h-" + hex.EncodeToString(sum[:])[:12]
}

// ParseHypotheses parses report_hypothesis arguments without storing them.
func ParseHypotheses(args map[string]any) ([]Hypothesis, string) {
	path, _ := args["path"].(string)
	var rawItems []any
	switch raw := args["hypotheses"].(type) {
	case []any:
		rawItems = raw
	case string:
		if err := json.Unmarshal([]byte(raw), &rawItems); err != nil {
			return nil, fmt.Sprintf("Error: failed to parse 'hypotheses' JSON string: %v", err)
		}
	}
	if len(rawItems) == 0 {
		return nil, "Error: 'hypotheses' array is required"
	}

	out := make([]Hypothesis, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		h := Hypothesis{
			Path:              path,
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
			h.Trigger == "" || h.Impact == "" || h.ChangeAttribution == "" {
			continue
		}
		h.ID = IDFor(h)
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, "Error: no complete hypothesis was provided"
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

// Collector is a thread-safe store for investigative output.
type Collector struct {
	mu         sync.Mutex
	hypotheses []Hypothesis
}

func NewCollector() *Collector { return &Collector{} }

func (c *Collector) Add(h Hypothesis) {
	if h.ID == "" {
		h.ID = IDFor(h)
	}
	c.mu.Lock()
	c.hypotheses = append(c.hypotheses, h)
	c.mu.Unlock()
}

func (c *Collector) Hypotheses() []Hypothesis {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Hypothesis(nil), c.hypotheses...)
}

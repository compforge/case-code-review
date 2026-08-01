package hypothesisreview

import "strings"

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

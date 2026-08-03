package language

import (
	"encoding/json"
	"fmt"
	"strings"
)

func jsonFileOutline(source Source) (FileOutline, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(source.Content))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return FileOutline{}, err
	}
	compact := compactJSONValue(value)
	content, err := json.MarshalIndent(compact, "", "  ")
	if err != nil {
		return FileOutline{}, err
	}
	return FileOutline{
		Path: source.Path, Language: Language("json"),
		rendered: fmt.Sprintf("File outline: %s (json)\n%s", source.Path, content),
	}, nil
}

func compactJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[key] = compactJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = compactJSONValue(child)
		}
		return out
	default:
		encoded, err := json.Marshal(value)
		if err == nil && len([]rune(string(encoded))) <= len([]rune(`"…"`)) {
			return value
		}
		return "…"
	}
}

func markdownFileOutline(source Source) FileOutline {
	lines := strings.Split(strings.ReplaceAll(source.Content, "\r\n", "\n"), "\n")
	var out []string
	var body []string
	inFence := false
	flushBody := func() {
		text := strings.TrimSpace(strings.Join(body, "\n"))
		body = body[:0]
		if text == "" {
			return
		}
		if len([]rune(text)) <= 1 {
			out = append(out, text)
		} else {
			out = append(out, "…")
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			body = append(body, line)
			continue
		}
		if !inFence && markdownHeading(trimmed) {
			flushBody()
			out = append(out, trimmed)
			continue
		}
		body = append(body, line)
	}
	flushBody()
	if len(out) == 0 {
		return FileOutline{}
	}
	return FileOutline{
		Path: source.Path, Language: Language("markdown"),
		rendered: fmt.Sprintf("File outline: %s (markdown)\n%s", source.Path, strings.Join(out, "\n")),
	}
}

func markdownHeading(line string) bool {
	if line == "" {
		return false
	}
	count := 0
	for count < len(line) && line[count] == '#' {
		count++
	}
	return count > 0 && count <= 6 && count < len(line) && line[count] == ' '
}

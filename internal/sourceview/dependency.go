package sourceview

import "encoding/json"

// DependencySource identifies source outside the repository without labeling
// it as current or baseline repository content. Checksums describe captured
// bytes; a declared module version is not proof of an external service state.
type DependencySource struct {
	Status       string   `json:"status"`
	Message      string   `json:"message,omitempty"`
	ManifestPath string   `json:"manifest_path,omitempty"`
	ImportPath   string   `json:"import_path"`
	Module       string   `json:"module,omitempty"`
	Version      string   `json:"version,omitempty"`
	DeclaredGo   string   `json:"declared_go,omitempty"`
	Checksum     string   `json:"checksum,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	Path         string   `json:"path,omitempty"`
	Start        int      `json:"start_line,omitempty"`
	End          int      `json:"end_line,omitempty"`
	Total        int      `json:"total_lines,omitempty"`
	Content      string   `json:"content,omitempty"`
	Files        []string `json:"files,omitempty"`
}

func DecodeDependencySource(text string) (DependencySource, bool) {
	var result DependencySource
	err := json.Unmarshal([]byte(text), &result)
	return result, err == nil && result.Status == "read" && result.Ref != "" && result.Content != "" && result.Start > 0 && result.End >= result.Start
}

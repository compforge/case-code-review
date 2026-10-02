package runner

import (
	"encoding/json"
	"testing"

	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/language"
)

func TestCodeGraphArtifactBoundsLargePartialReports(t *testing.T) {
	index := &language.RepositoryIndex{}
	for range 40000 {
		index.Report.Diagnostics = append(index.Report.Diagnostics, cg.Diagnostic{Code: "unresolved_reference", Message: "reference is outside the supplied source set"})
	}
	for range 2000 {
		index.Gaps = append(index.Gaps, "document byte limit")
	}
	data := codeGraphArtifact(index)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 16000 {
		t.Fatalf("report expanded the session by %d bytes", len(encoded))
	}
	if data["diagnostic_counts"].(map[string]int)["unresolved_reference"] != 40000 || data["gap_count"] != 2000 || data["available"] != false {
		t.Fatalf("bounded report lost coverage totals: %v", data)
	}
	if len(index.Report.Diagnostics) != 40000 {
		t.Fatal("session projection modified upstream evidence")
	}
}

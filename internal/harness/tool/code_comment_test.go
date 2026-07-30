package tool

import "testing"

func TestParseComments_StructuredFields(t *testing.T) {
	args := map[string]any{
		"path": "a.go",
		"comments": []any{
			map[string]any{"content": "c1", "existing_code": "x",
				"category": "Bug", "severity": " HIGH "}, // 大小写/空白归一化
			map[string]any{"content": "c2", "existing_code": "y",
				"category": "vibe", "severity": "blocker"}, // 越界值置空，不透传
			map[string]any{"content": "c3", "existing_code": "z"}, // 缺省容错
		},
	}
	cs, errMsg := ParseComments(args)
	if errMsg != "" || len(cs) != 3 {
		t.Fatalf("parse failed: %q, n=%d", errMsg, len(cs))
	}
	if cs[0].Category != "bug" || cs[0].Severity != "high" {
		t.Fatalf("normalization failed: %+v", cs[0])
	}
	if cs[1].Category != "" || cs[1].Severity != "" {
		t.Fatalf("out-of-vocab must be dropped: %+v", cs[1])
	}
	if cs[2].Category != "" || cs[2].Severity != "" {
		t.Fatalf("absent fields must stay empty: %+v", cs[2])
	}
}

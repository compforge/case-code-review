package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeSession(t *testing.T) {
	lines := `{"type":"session_start","cwd":"/r","gitBranch":"b","timestamp":"2026-07-02T10:00:00Z"}
{"type":"llm_request","scope_id":"u1","taskType":"main_task","timestamp":"2026-07-02T10:00:01Z"}
{"type":"llm_response","scope_id":"u1","filePath":"a.go","model":"m1","duration_ms":10000,"timestamp":"2026-07-02T10:00:11Z"}
{"type":"tool_call","scope_id":"u1","tool_name":"read_files","ok":true,"result":"x","timestamp":"2026-07-02T10:00:11Z"}
{"type":"llm_request","scope_id":"u1","taskType":"main_task","timestamp":"2026-07-02T10:00:12Z"}
{"type":"llm_response","scope_id":"u1","filePath":"a.go","model":"m1","duration_ms":50000,"timestamp":"2026-07-02T10:01:02Z"}
{"type":"llm_request","scope_id":"u2","taskType":"main_task","timestamp":"2026-07-02T10:00:01Z"}
{"type":"llm_response","scope_id":"u2","filePath":"b.go","model":"m2","duration_ms":20000,"timestamp":"2026-07-02T10:00:21Z"}
{"type":"tool_call","scope_id":"u2","tool_name":"search_code","ok":false,"result":"","timestamp":"2026-07-02T10:00:21Z"}
{"type":"llm_error","scope_id":"u2","timestamp":"2026-07-02T10:00:30Z"}
`
	f := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(f, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := analyzeSession(f)
	if err != nil {
		t.Fatal(err)
	}
	if st.Repo != "/r" || st.Branch != "b" {
		t.Fatalf("session_start not picked up: %+v", st)
	}
	if st.LLMCalls != 3 || st.LLMErrors != 1 || st.LLMSumSec != 80 {
		t.Fatalf("llm aggregation wrong: calls=%d errs=%d sum=%.0f", st.LLMCalls, st.LLMErrors, st.LLMSumSec)
	}
	if st.WallSec != 62 { // 10:00:00 → 10:01:02
		t.Fatalf("wall=%.0f", st.WallSec)
	}
	if st.Models["m1"] != 2 || st.Models["m2"] != 1 {
		t.Fatalf("models: %v", st.Models)
	}
	if st.Tools["read_files"].Calls != 1 || st.Tools["search_code"].Failures != 1 {
		t.Fatalf("tools: %+v %+v", st.Tools["read_files"], st.Tools["search_code"])
	}
	if st.Scopes != 2 || st.RoundsMax != 2 {
		t.Fatalf("scopes=%d roundsMax=%d", st.Scopes, st.RoundsMax)
	}
	// Slowest chain first: u1 60s over 2 calls beats u2 20s.
	if len(st.Chains) != 2 || st.Chains[0].Label != "a.go" || st.Chains[0].Seconds != 60 || st.Chains[0].Calls != 2 {
		t.Fatalf("chains: %+v", st.Chains)
	}
}

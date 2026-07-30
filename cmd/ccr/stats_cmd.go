package main

// `ccr stats` — where did a review's wall-time go? Reads session JSONL transcripts
// (the same files the viewer renders) and reports the latency shape: LLM call
// percentiles, per-tool usage/failures, rounds per unit, and the slowest unit
// chains. A unit's rounds run sequentially, so the slowest chain ≈ the review's
// wall time on small MRs — this is the first place to look when a review feels slow.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/viewer"
)

// statsEvent is the lean read-side of the session JSONL records (persist.go writes
// them as maps; only the fields stats aggregates over are decoded).
type statsEvent struct {
	Type       string  `json:"type"`
	Timestamp  string  `json:"timestamp"`
	DurationMS float64 `json:"duration_ms"`
	ScopeID    string  `json:"scope_id"`
	FilePath   string  `json:"filePath"`
	TaskType   string  `json:"taskType"`
	ToolName   string  `json:"tool_name"`
	OK         *bool   `json:"ok"`
	Result     string  `json:"result"`
	Model      string  `json:"model"`
	GitBranch  string  `json:"gitBranch"`
	Cwd        string  `json:"cwd"`
}

type toolStat struct {
	Calls     int
	Failures  int
	ResultLen int
}

type chainStat struct {
	Label   string
	Seconds float64
	Calls   int
}

type sessionStats struct {
	File      string               `json:"file"`
	Repo      string               `json:"repo,omitempty"`
	Branch    string               `json:"branch,omitempty"`
	WallSec   float64              `json:"wall_sec"`
	LLMCalls  int                  `json:"llm_calls"`
	LLMErrors int                  `json:"llm_errors"`
	LLMSumSec float64              `json:"llm_sum_sec"`
	P50Sec    float64              `json:"llm_p50_sec"`
	P90Sec    float64              `json:"llm_p90_sec"`
	MaxSec    float64              `json:"llm_max_sec"`
	Models    map[string]int       `json:"models,omitempty"`
	TaskTypes map[string]int       `json:"task_types,omitempty"`
	Tools     map[string]*toolStat `json:"tools,omitempty"`
	Scopes    int                  `json:"scopes"`
	RoundsP50 int                  `json:"rounds_p50"`
	RoundsMax int                  `json:"rounds_max"`
	Chains    []chainStat          `json:"slowest_chains,omitempty"`
}

func runStats(args []string) error {
	format := "text"
	var paths []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Println(`Usage: ccr stats [session.jsonl | dir]... [--format text|json]

Analyze session transcripts: LLM latency percentiles, tool usage, rounds per
unit and the slowest unit chains. With no path, analyzes the most recent
session under ~/.casecodereview/sessions.`)
			return nil
		default:
			paths = append(paths, args[i])
		}
	}

	files, err := resolveSessionFiles(paths)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no session .jsonl files found")
	}

	var all []*sessionStats
	for _, f := range files {
		st, err := analyzeSession(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ccr stats: skip %s: %v\n", f, err)
			continue
		}
		all = append(all, st)
	}
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(all)
	}
	for _, st := range all {
		printStats(st)
	}
	return nil
}

// resolveSessionFiles expands the CLI args: a file is taken as-is, a directory
// contributes its *.jsonl; no args → the most recently modified session under
// the sessions root (the review you just ran).
func resolveSessionFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		root, err := viewer.SessionsRoot()
		if err != nil {
			return nil, err
		}
		var latest string
		var latestMod time.Time
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			if info, err := d.Info(); err == nil && info.ModTime().After(latestMod) {
				latest, latestMod = p, info.ModTime()
			}
			return nil
		})
		if latest == "" {
			return nil, fmt.Errorf("no sessions under %s", root)
		}
		return []string{latest}, nil
	}
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			matches, _ := filepath.Glob(filepath.Join(p, "*.jsonl"))
			sort.Strings(matches)
			out = append(out, matches...)
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func analyzeSession(path string) (*sessionStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st := &sessionStats{
		File:      path,
		Models:    map[string]int{},
		TaskTypes: map[string]int{},
		Tools:     map[string]*toolStat{},
	}
	var tsMin, tsMax time.Time
	var llmDurs []float64
	chainSec := map[string]float64{}
	chainCalls := map[string]int{}
	chainLabel := map[string]string{}
	rounds := map[string]int{}

	// Session lines carry full prompts/responses and can run to megabytes —
	// a default bufio.Scanner token limit would truncate the read.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e statsEvent
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
			if tsMin.IsZero() || t.Before(tsMin) {
				tsMin = t
			}
			if t.After(tsMax) {
				tsMax = t
			}
		}
		switch e.Type {
		case "session_start":
			st.Repo, st.Branch = e.Cwd, e.GitBranch
		case "llm_request":
			st.TaskTypes[e.TaskType]++
			rounds[e.ScopeID]++
		case "llm_response":
			d := e.DurationMS / 1000
			llmDurs = append(llmDurs, d)
			st.LLMSumSec += d
			st.Models[e.Model]++
			chainSec[e.ScopeID] += d
			chainCalls[e.ScopeID]++
			if e.FilePath != "" {
				chainLabel[e.ScopeID] = e.FilePath
			}
		case "llm_error":
			st.LLMErrors++
		case "tool_call":
			ts := st.Tools[e.ToolName]
			if ts == nil {
				ts = &toolStat{}
				st.Tools[e.ToolName] = ts
			}
			ts.Calls++
			ts.ResultLen += len(e.Result)
			if e.OK != nil && !*e.OK {
				ts.Failures++
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if !tsMin.IsZero() {
		st.WallSec = tsMax.Sub(tsMin).Seconds()
	}
	st.LLMCalls = len(llmDurs)
	sort.Float64s(llmDurs)
	pick := func(q float64) float64 {
		if len(llmDurs) == 0 {
			return 0
		}
		return llmDurs[int(q*float64(len(llmDurs)-1))]
	}
	st.P50Sec, st.P90Sec, st.MaxSec = pick(0.5), pick(0.9), pick(1)

	st.Scopes = len(rounds)
	rs := make([]int, 0, len(rounds))
	for _, n := range rounds {
		rs = append(rs, n)
	}
	sort.Ints(rs)
	if len(rs) > 0 {
		st.RoundsP50, st.RoundsMax = rs[len(rs)/2], rs[len(rs)-1]
	}

	for id, secs := range chainSec {
		label := chainLabel[id]
		if label == "" {
			label = id
		}
		st.Chains = append(st.Chains, chainStat{Label: label, Seconds: secs, Calls: chainCalls[id]})
	}
	sort.Slice(st.Chains, func(i, j int) bool { return st.Chains[i].Seconds > st.Chains[j].Seconds })
	if len(st.Chains) > 5 {
		st.Chains = st.Chains[:5]
	}
	return st, nil
}

func printStats(st *sessionStats) {
	fmt.Printf("== %s\n", st.File)
	if st.Repo != "" {
		fmt.Printf("   repo %s  branch %s\n", st.Repo, st.Branch)
	}
	fmt.Printf("   wall %.1fm · llm %d calls (sum %.1fm, p50 %.0fs, p90 %.0fs, max %.0fs)",
		st.WallSec/60, st.LLMCalls, st.LLMSumSec/60, st.P50Sec, st.P90Sec, st.MaxSec)
	if st.LLMErrors > 0 {
		fmt.Printf(" · %d llm error(s)", st.LLMErrors)
	}
	fmt.Println()
	if st.WallSec > 0 {
		fmt.Printf("   effective concurrency %.1fx\n", st.LLMSumSec/st.WallSec)
	}
	fmt.Printf("   scopes %d · rounds p50 %d max %d\n", st.Scopes, st.RoundsP50, st.RoundsMax)
	fmt.Printf("   models %s\n", formatCounts(st.Models))
	fmt.Printf("   tasks  %s\n", formatCounts(st.TaskTypes))
	if len(st.Tools) > 0 {
		type kv struct {
			name string
			s    *toolStat
		}
		tools := make([]kv, 0, len(st.Tools))
		for n, s := range st.Tools {
			tools = append(tools, kv{n, s})
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].s.Calls > tools[j].s.Calls })
		parts := make([]string, 0, len(tools))
		for _, t := range tools {
			p := fmt.Sprintf("%s×%d", t.name, t.s.Calls)
			if t.s.Failures > 0 {
				p += fmt.Sprintf(" (%d failed)", t.s.Failures)
			}
			parts = append(parts, p)
		}
		fmt.Printf("   tools  %s\n", strings.Join(parts, ", "))
	}
	if len(st.Chains) > 0 {
		fmt.Println("   slowest unit chains (sequential — the wall-time floor):")
		for _, c := range st.Chains {
			fmt.Printf("     %5.1fm / %2d calls  %s\n", c.Seconds/60, c.Calls, c.Label)
		}
	}
}

func formatCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		name := k
		if name == "" {
			name = "?"
		}
		parts = append(parts, fmt.Sprintf("%s×%d", name, m[k]))
	}
	return strings.Join(parts, ", ")
}

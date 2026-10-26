package main

import (
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/recording"
)

func printCost(report recording.CostReport) {
	total := report.Total
	fmt.Printf("   tokens input=%d output=%d cache_read=%d cache_write=%d · estimated=%d unknown=%d pending=%d\n",
		total.Tokens.PromptTokens, total.Tokens.CompletionTokens, total.Tokens.CacheReadTokens, total.Tokens.CacheWriteTokens, total.EstimatedUsage, total.UnknownUsage, total.Pending)
	if len(report.Operations) == 0 {
		return
	}
	fmt.Println("   stages: elapsed / self; tokens include descendants (do not sum nested rows); running times are observed lower bounds")
	parents := map[string]string{}
	for _, s := range report.Operations {
		parents[string(s.Operation.ID)] = string(s.Operation.ParentID)
	}
	for _, s := range report.Operations {
		depth := 0
		seen := map[string]bool{}
		for id := string(s.Operation.ParentID); parents[id] != "" && !seen[id]; id = parents[id] {
			seen[id] = true
			depth++
		}
		fmt.Printf("   %s%s [%s] %.3fs / %.3fs · input=%d output=%d cache=%d/%d unknown=%d",
			strings.Repeat("  ", depth), s.Operation.Name, s.Operation.Status, s.DurationMS/1000, s.SelfMS/1000, s.Cost.Tokens.PromptTokens, s.Cost.Tokens.CompletionTokens, s.Cost.Tokens.CacheReadTokens, s.Cost.Tokens.CacheWriteTokens, s.Cost.UnknownUsage)
		for _, key := range []string{"scope_id", "unit_id", "lane_id", "task_type", "outcome", "reason"} {
			if value, ok := s.Operation.Attributes[key]; ok {
				fmt.Printf(" %s=%s", key, value)
			}
		}
		if s.Operation.Error != "" {
			fmt.Printf(" error=%q", s.Operation.Error)
		}
		fmt.Println()
	}
}

package runner

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
)

// TestCodeSearchReplay compares visible tool results on captured search inputs.
// It runs no LLM and makes no claim about whole-review quality or token savings.
func TestCodeSearchReplay(t *testing.T) {
	path := os.Getenv("CCR_SEARCH_REPLAY")
	if path == "" {
		t.Skip("set CCR_SEARCH_REPLAY, CCR_SEARCH_REPO and CCR_SEARCH_REF for captured search replay")
	}
	repo, ref := os.Getenv("CCR_SEARCH_REPO"), os.Getenv("CCR_SEARCH_REF")
	if repo == "" || ref == "" {
		t.Fatal("replay requires an explicit repository and reviewed commit")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	transcript, err := session.ReadTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.TruncatedTail {
		t.Fatal("replay input has a truncated tail")
	}
	var requests []map[string]any
	for _, record := range transcript.Records {
		if record["type"] != "llm_response" {
			continue
		}
		calls, _ := record["tool_calls"].([]any)
		for _, raw := range calls {
			call, ok := raw.(map[string]any)
			if !ok || call["name"] != tool.CodeSearch.Name() {
				continue
			}
			arguments, ok := call["arguments"].(string)
			if !ok {
				t.Fatal("missing captured arguments")
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, args)
		}
	}
	if len(requests) == 0 {
		t.Fatal("no captured search calls")
	}
	ctx := context.Background()
	git := gitcmd.New(0)
	reader := &tool.FileReader{RepoDir: repo, Mode: tool.ModeCommit, Ref: ref, Runner: git}
	registry := tool.NewRegistry()
	registry.Register(tool.NewFileRead(reader))
	review := New(Args{RepoDir: repo, Commit: ref, GitRunner: git, Tools: registry})
	defer review.session.Finalize()
	if err := review.loadChanges(ctx); err != nil {
		t.Fatal(err)
	}
	analyzer := review.analyzer
	index := analyzer.Repository()
	if index.Graph == nil {
		t.Fatalf("no published graph: %v", index.Gaps)
	}
	t.Logf("snapshot=%s documents=%d gaps=%v graph_build=%s", ref, len(index.Graph.Report().Documents), index.Gaps, index.Duration)
	for _, symbols := range []bool{false, true} {
		name := "default"
		if symbols {
			name = "symbol_context"
		}
		t.Run(name, func(t *testing.T) {
			shared := NewCodeSearchLanguageSource(reader, analyzer)
			local := NewCodeSearchLanguageSource(reader, language.NewAnalyzer(repo))
			providers := []*tool.CodeSearchProvider{
				tool.NewCodeSearch(reader).WithDefinitionSource(local.Definitions),
				tool.NewCodeSearch(reader).WithDefinitionSource(shared.Definitions),
			}
			if symbols {
				providers[0].WithSymbolSource(local.Symbols)
				providers[1].WithSymbolSource(shared.Symbols)
			}
			var elapsed [2]time.Duration
			var bytes int
			for i, args := range requests {
				var results [2]string
				var errs [2]error
				// Rotate execution order to reduce systematic filesystem-cache advantage.
				for j := 0; j < 2; j++ {
					arm := (i + j) % 2
					start := time.Now()
					results[arm], errs[arm] = providers[arm].Execute(ctx, args)
					elapsed[arm] += time.Since(start)
				}
				if results[0] != results[1] || errorText(errs[0]) != errorText(errs[1]) {
					t.Fatalf("call %d changed visible evidence: local=%v shared=%v\nLOCAL:\n%s\nSHARED:\n%s", i+1, errs[0], errs[1], results[0], results[1])
				}
				bytes += len(results[1])
			}
			t.Logf("calls=%d identical_output_bytes=%d local=%s shared=%s (single-pass tool timing, excludes initial graph build)", len(requests), bytes, elapsed[0], elapsed[1])
		})
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

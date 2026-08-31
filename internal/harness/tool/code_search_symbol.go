package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	codeSearchSymbolMaxLines   = 100
	codeSearchSymbolMaxFiles   = 8
	codeSearchSymbolMaxAnchors = 8
)

const (
	CodeSearchSymbolExpanded       = "expanded"
	CodeSearchSymbolAmbiguous      = "ambiguous"
	CodeSearchSymbolOversized      = "oversized"
	CodeSearchSymbolUnsupported    = "unsupported"
	CodeSearchSymbolBudgetRejected = "budget_rejected"
)

const (
	codeSearchSymbolOutcomePrefix = "Symbol context: "
	codeSearchSymbolAnchorPrefix  = "Symbol: "
	codeSearchSymbolSourcePrefix  = "Symbol source: "
)

// CodeSearchHit is the parser-neutral location passed to Runner's Language
// adapter after text search. Harness owns the request; Language owns the fact
// that a hit is enclosed by a particular source symbol.
type CodeSearchHit struct {
	Path string
	Line int
}

// CodeSearchSymbol is Runner's snapshot-consistent projection of one
// language-owned definition into the generic search tool boundary.
type CodeSearchSymbol struct {
	SymbolID    string
	Name        string
	Kind        string
	Signature   string
	Path        string
	StartLine   int
	EndLine     int
	TotalLines  int
	HitLines    []int
	SourceLines []string
}

// CodeSearchSymbolSource is injected by Runner so generic Harness code never
// imports a source parser or reads a different snapshot from the text search.
type CodeSearchSymbolSource func(context.Context, []CodeSearchHit) []CodeSearchSymbol

// CodeSearchSymbolContextOutcome is the stable trajectory fact emitted for
// every automatic symbol projection attempt.
type CodeSearchSymbolContextOutcome struct {
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	HitCount       int    `json:"hit_count"`
	ResolvedHits   int    `json:"resolved_hits"`
	CandidateCount int    `json:"candidate_count"`
}

// CodeSearchSymbolAnchor is a compact, source-free navigation choice retained
// when several enclosing symbols are possible or one body is too large.
type CodeSearchSymbolAnchor struct {
	SymbolID  string `json:"symbol_id"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Signature string `json:"signature,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	HitLines  []int  `json:"hit_lines,omitempty"`
}

// CodeSearchSourceRange describes source visibly embedded in a search result.
// Context management uses it to avoid immediately re-reading the same range.
type CodeSearchSourceRange struct {
	Path       string `json:"path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
}

type codeSearchSymbolRender struct {
	text     string
	expanded bool
}

func (p *CodeSearchProvider) renderSymbolContext(
	ctx context.Context,
	fileOrder []string,
	fileMatches map[string][]codeSearchMatch,
	contextBudget int,
) codeSearchSymbolRender {
	hits := make([]CodeSearchHit, 0)
	for _, path := range fileOrder {
		for _, match := range fileMatches[path] {
			hits = append(hits, CodeSearchHit{Path: path, Line: match.lineNum})
		}
	}
	if len(hits) == 0 {
		return codeSearchSymbolRender{}
	}
	if len(fileOrder) > codeSearchSymbolMaxFiles {
		return renderCodeSearchSymbols(CodeSearchSymbolContextOutcome{
			Status: CodeSearchSymbolAmbiguous, Reason: "matched_file_limit",
			HitCount: len(hits), CandidateCount: len(fileOrder),
		}, nil, false)
	}
	if p.symbolSource == nil {
		return renderCodeSearchSymbols(CodeSearchSymbolContextOutcome{
			Status: CodeSearchSymbolUnsupported, Reason: "language_source_unavailable",
			HitCount: len(hits),
		}, nil, false)
	}

	symbols := validCodeSearchSymbols(p.symbolSource(ctx, hits))
	resolvedHits := 0
	for _, symbol := range symbols {
		resolvedHits += len(symbol.HitLines)
	}
	outcome := CodeSearchSymbolContextOutcome{
		HitCount: len(hits), ResolvedHits: resolvedHits, CandidateCount: len(symbols),
	}
	switch {
	case len(symbols) == 0:
		outcome.Status = CodeSearchSymbolUnsupported
		outcome.Reason = "no_enclosing_symbol"
		return renderCodeSearchSymbols(outcome, nil, false)
	case len(symbols) != 1 || resolvedHits != len(hits):
		outcome.Status = CodeSearchSymbolAmbiguous
		if resolvedHits != len(hits) {
			outcome.Reason = "partial_resolution"
		} else {
			outcome.Reason = "multiple_symbols"
		}
		return renderCodeSearchSymbols(outcome, symbols, false)
	}

	symbol := symbols[0]
	lineCount := symbol.EndLine - symbol.StartLine + 1
	switch {
	case lineCount > codeSearchSymbolMaxLines:
		outcome.Status = CodeSearchSymbolOversized
		outcome.Reason = fmt.Sprintf("symbol_has_%d_lines", lineCount)
		return renderCodeSearchSymbols(outcome, symbols, false)
	case lineCount > contextBudget:
		outcome.Status = CodeSearchSymbolBudgetRejected
		outcome.Reason = fmt.Sprintf("query_budget_%d_lines", contextBudget)
		return renderCodeSearchSymbols(outcome, symbols, false)
	case len(symbol.SourceLines) != lineCount:
		outcome.Status = CodeSearchSymbolUnsupported
		outcome.Reason = "source_span_unavailable"
		return renderCodeSearchSymbols(outcome, symbols, false)
	default:
		outcome.Status = CodeSearchSymbolExpanded
		return renderCodeSearchSymbols(outcome, symbols, true)
	}
}

func validCodeSearchSymbols(symbols []CodeSearchSymbol) []CodeSearchSymbol {
	seen := make(map[string]bool)
	valid := make([]CodeSearchSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol.SymbolID == "" || symbol.Path == "" || symbol.StartLine < 1 ||
			symbol.EndLine < symbol.StartLine || symbol.TotalLines < symbol.EndLine || len(symbol.HitLines) == 0 {
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%d", symbol.Path, symbol.SymbolID, symbol.StartLine, symbol.EndLine)
		if seen[key] {
			continue
		}
		seen[key] = true
		sort.Ints(symbol.HitLines)
		valid = append(valid, symbol)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].Path != valid[j].Path {
			return valid[i].Path < valid[j].Path
		}
		if valid[i].StartLine != valid[j].StartLine {
			return valid[i].StartLine < valid[j].StartLine
		}
		return valid[i].SymbolID < valid[j].SymbolID
	})
	return valid
}

func renderCodeSearchSymbols(
	outcome CodeSearchSymbolContextOutcome,
	symbols []CodeSearchSymbol,
	expand bool,
) codeSearchSymbolRender {
	var out strings.Builder
	writeJSONLine(&out, codeSearchSymbolOutcomePrefix, outcome)
	shown := symbols
	if len(shown) > codeSearchSymbolMaxAnchors {
		shown = shown[:codeSearchSymbolMaxAnchors]
	}
	for _, symbol := range shown {
		writeJSONLine(&out, codeSearchSymbolAnchorPrefix, CodeSearchSymbolAnchor{
			SymbolID: symbol.SymbolID, Name: symbol.Name, Kind: symbol.Kind,
			Signature: oneLine(symbol.Signature), Path: symbol.Path,
			StartLine: symbol.StartLine, EndLine: symbol.EndLine,
			HitLines: append([]int(nil), symbol.HitLines...),
		})
	}
	if len(symbols) > len(shown) {
		fmt.Fprintf(&out, "Note: Symbol anchors truncated to the first %d candidates.\n", len(shown))
	}
	if expand && len(symbols) == 1 {
		symbol := symbols[0]
		writeJSONLine(&out, codeSearchSymbolSourcePrefix, CodeSearchSourceRange{
			Path: symbol.Path, StartLine: symbol.StartLine,
			EndLine: symbol.EndLine, TotalLines: symbol.TotalLines,
		})
		for i, line := range symbol.SourceLines {
			fmt.Fprintf(&out, "%d|%s\n", symbol.StartLine+i, line)
		}
	}
	return codeSearchSymbolRender{text: strings.TrimRight(out.String(), "\n"), expanded: expand}
}

func writeJSONLine(out *strings.Builder, prefix string, value any) {
	encoded, _ := json.Marshal(value)
	out.WriteString(prefix)
	out.Write(encoded)
	out.WriteByte('\n')
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// ParseCodeSearchSymbolContextOutcome decodes one result's stable status line.
func ParseCodeSearchSymbolContextOutcome(result string) (CodeSearchSymbolContextOutcome, bool) {
	for _, line := range strings.Split(result, "\n") {
		if !strings.HasPrefix(line, codeSearchSymbolOutcomePrefix) {
			continue
		}
		var outcome CodeSearchSymbolContextOutcome
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, codeSearchSymbolOutcomePrefix)), &outcome); err != nil {
			return CodeSearchSymbolContextOutcome{}, false
		}
		return outcome, outcome.Status != ""
	}
	return CodeSearchSymbolContextOutcome{}, false
}

// CodeSearchSymbolAnchors parses the bounded navigation choices carried by a
// full search result so the first compaction tier can retain them without body
// source.
func CodeSearchSymbolAnchors(result string) []CodeSearchSymbolAnchor {
	var anchors []CodeSearchSymbolAnchor
	for _, line := range strings.Split(result, "\n") {
		if !strings.HasPrefix(line, codeSearchSymbolAnchorPrefix) {
			continue
		}
		var anchor CodeSearchSymbolAnchor
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, codeSearchSymbolAnchorPrefix)), &anchor); err != nil ||
			anchor.SymbolID == "" || anchor.Path == "" || anchor.StartLine < 1 || anchor.EndLine < anchor.StartLine {
			continue
		}
		anchors = append(anchors, anchor)
	}
	return anchors
}

// CodeSearchSourceRanges returns only source ranges actually visible in the
// current search representation; compacted search results naturally return none.
func CodeSearchSourceRanges(result string) []CodeSearchSourceRange {
	var ranges []CodeSearchSourceRange
	for _, line := range strings.Split(result, "\n") {
		if !strings.HasPrefix(line, codeSearchSymbolSourcePrefix) {
			continue
		}
		var source CodeSearchSourceRange
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, codeSearchSymbolSourcePrefix)), &source); err != nil ||
			source.Path == "" || source.StartLine < 1 || source.EndLine < source.StartLine || source.TotalLines < source.EndLine {
			continue
		}
		ranges = append(ranges, source)
	}
	return ranges
}

package runner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
)

const codeSearchAnalysisMaxBytes = 512 * 1024

// CodeSearchLanguageSource shares one snapshot-aware Analyzer across search
// miss recovery and symbol context. It is the composition adapter between
// Language-owned facts and Harness-owned tool projections.
type CodeSearchLanguageSource struct {
	reader   *tool.FileReader
	analyzer *language.Analyzer
}

func NewCodeSearchLanguageSource(reader *tool.FileReader) *CodeSearchLanguageSource {
	return &CodeSearchLanguageSource{reader: reader, analyzer: language.NewAnalyzer(reader.RepoDir)}
}

// Definitions adapts Language Knowledge to Harness' optional no-match
// recovery hook. The FileReader keeps facts on the reviewed ref.
func (s *CodeSearchLanguageSource) Definitions(ctx context.Context, paths []string) []tool.CodeSearchDefinition {
	var definitions []tool.CodeSearchDefinition
	for _, path := range paths {
		if ctx.Err() != nil {
			break
		}
		content, err := s.reader.Read(ctx, path)
		if err != nil || len(content) > codeSearchAnalysisMaxBytes {
			continue
		}
		analysis, err := s.analyzer.Analyze(ctx, language.Source{Path: path, Content: content})
		if err != nil {
			continue
		}
		for _, definition := range analysis.Definitions {
			definitions = append(definitions, tool.CodeSearchDefinition{
				Name: definition.Name,
				Path: path,
				Line: definition.Span.Start,
			})
		}
	}
	return definitions
}

// Symbols adapts Language's enclosing-definition facts and source
// spans to Harness' optional symbol_context hook. FileReader and Analyzer use
// the same reviewed snapshot as the preceding text search.
func (s *CodeSearchLanguageSource) Symbols(ctx context.Context, hits []tool.CodeSearchHit) []tool.CodeSearchSymbol {
	hitsByPath := make(map[string][]int)
	var paths []string
	for _, hit := range hits {
		if hit.Path == "" || hit.Line < 1 {
			continue
		}
		if _, exists := hitsByPath[hit.Path]; !exists {
			paths = append(paths, hit.Path)
		}
		hitsByPath[hit.Path] = append(hitsByPath[hit.Path], hit.Line)
	}

	var symbols []tool.CodeSearchSymbol
	for _, path := range paths {
		if ctx.Err() != nil {
			break
		}
		content, err := s.reader.Read(ctx, path)
		if err != nil || len(content) > codeSearchAnalysisMaxBytes {
			continue
		}
		source := language.Source{Path: path, Content: content}
		fileLines := strings.Split(content, "\n")
		byDefinition := make(map[string]int)
		pathHits := hitsByPath[path]
		sort.Ints(pathHits)
		previousLine := 0
		for _, line := range pathHits {
			if line == previousLine {
				continue
			}
			previousLine = line
			definition, ok := s.analyzer.SymbolAt(ctx, source, line)
			if !ok || definition.Span.Start < 1 || definition.Span.End > len(fileLines) {
				continue
			}
			key := fmt.Sprintf("%s\x00%d\x00%d", definition.SymbolID, definition.Span.Start, definition.Span.End)
			index, exists := byDefinition[key]
			if !exists {
				symbols = append(symbols, tool.CodeSearchSymbol{
					SymbolID: definition.SymbolID, Name: definition.Name,
					Kind: string(definition.Kind), Signature: definition.Signature,
					Path: path, StartLine: definition.Span.Start,
					EndLine: definition.Span.End, TotalLines: len(fileLines),
					SourceLines: append([]string(nil), fileLines[definition.Span.Start-1:definition.Span.End]...),
				})
				index = len(symbols) - 1
				byDefinition[key] = index
			}
			symbol := &symbols[index]
			symbol.HitLines = append(symbol.HitLines, line)
		}
	}
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].Path != symbols[j].Path {
			return symbols[i].Path < symbols[j].Path
		}
		if symbols[i].StartLine != symbols[j].StartLine {
			return symbols[i].StartLine < symbols[j].StartLine
		}
		return symbols[i].SymbolID < symbols[j].SymbolID
	})
	return symbols
}

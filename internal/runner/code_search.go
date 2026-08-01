package runner

import (
	"context"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
)

const codeSearchDefinitionMaxBytes = 512 * 1024

// CodeSearchDefinitions adapts Language Knowledge to Harness' optional
// no-match recovery hook. The FileReader keeps facts on the reviewed ref.
func CodeSearchDefinitions(reader *tool.FileReader) tool.CodeSearchDefinitionSource {
	analyzer := language.NewAnalyzer(reader.RepoDir)
	return func(ctx context.Context, paths []string) []tool.CodeSearchDefinition {
		var definitions []tool.CodeSearchDefinition
		for _, path := range paths {
			if ctx.Err() != nil {
				break
			}
			content, err := reader.Read(ctx, path)
			if err != nil || len(content) > codeSearchDefinitionMaxBytes {
				continue
			}
			analysis, err := analyzer.Analyze(ctx, language.Source{Path: path, Content: content})
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
}

package runner

import (
	"context"
	"fmt"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

func (a *Runner) observeGraphBuild(ctx context.Context, analyzer *language.Analyzer, version string) {
	if analyzer == nil || a.session == nil {
		return
	}
	ctx = operation.NewContext(a.session.Context(ctx), operation.Ref{ReviewID: a.session.SessionID, OperationID: operation.ID("operation:" + a.session.SessionID)})
	analyzer.ObserveRepositoryBuild = func() func(*language.RepositoryIndex) {
		stageCtx, finish := recording.Begin(a.session.Context(ctx), "codegraph.build", operation.Attribute{Key: "version", Value: version})
		return func(index *language.RepositoryIndex) {
			var err error
			if len(index.Gaps) > 0 {
				err = fmt.Errorf("partial graph: %d analysis gaps", len(index.Gaps))
			}
			finish(err)
			name := "codegraph"
			if version == "before" {
				name = "codegraph_before"
			}
			a.session.WriteArtifactContext(stageCtx, name, codeGraphArtifact(index))
		}
	}
}

package runner

import (
	"context"
	"fmt"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/language"
)

func (a *Runner) observeGraphBuild(ctx context.Context, analyzer *language.Analyzer, version string) {
	if analyzer == nil || a.session == nil {
		return
	}
	ctx = timeline.NewStageContext(a.session.Context(ctx), timeline.StageRef{TimelineID: a.session.SessionID, StageID: timeline.StageID("operation:" + a.session.SessionID)})
	analyzer.ObserveRepositoryBuild = func() func(*language.RepositoryIndex) {
		stageCtx, finish := session.Begin(a.session.Context(ctx), "codegraph.build", timeline.Field{Key: "version", Value: version})
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

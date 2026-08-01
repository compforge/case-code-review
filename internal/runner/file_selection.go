package runner

import (
	"context"
	"fmt"

	allowedext "github.com/qiankunli/case-code-review/internal/config/allowlist"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/project"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// fileSelection is Runner's projection of stable Component/FileRole facts onto
// the current Unit Review strategy. Target/context are deliberately not stored
// in project.FileRoles: another reviewer may treat the same manifest as its
// primary target in the future.
type fileSelection struct {
	Component    project.Component
	HasComponent bool
	Roles        project.FileRoles
	Target       bool
	Context      bool
	Reason       ExcludeReason
}

func (a *Runner) prepareFileSelections(ctx context.Context) {
	a.fileSelections = make(map[string]fileSelection, len(a.changes))
	a.componentClues = make(map[string][]unit.Clue)
	a.contextFileCount = 0

	reader := &tool.FileReader{
		RepoDir: a.args.RepoDir,
		Mode:    tool.ParseReviewMode(a.args.From, a.args.To, a.args.Commit),
		Runner:  a.args.GitRunner,
	}
	reader.Ref, _ = reader.Mode.RefValue(a.args.To, a.args.Commit)
	repository := project.NewRepository(a.args.RepoDir, func(path string) bool {
		_, err := reader.Read(ctx, path)
		return err == nil
	})

	for i := range a.changes {
		d := &a.changes[i]
		path := effectivePath(*d)
		selection := a.selectFile(*d, repository)
		selection = a.enrichFileSelection(ctx, path, selection, reader)
		a.fileSelections[path] = selection
		if !selection.Context {
			continue
		}
		a.contextFileCount++
		key := componentKey(selection.Component)
		a.componentClues[key] = append(a.componentClues[key], unit.Clue{
			Kind:     unit.ClueProject,
			Relation: unit.RelProject,
			Ref:      path,
			Text: fmt.Sprintf(
				"changed %s `%s` in this %s component; treat it as project context and inspect its diff with file_read_diff only when relevant",
				selection.Roles, path, selection.Component.Kind,
			),
		})
	}
}

func (a *Runner) enrichFileSelection(
	ctx context.Context,
	path string,
	selection fileSelection,
	reader *tool.FileReader,
) fileSelection {
	if !selection.HasComponent || !selection.Roles.Has(project.RoleSource) {
		return selection
	}
	content, err := reader.Read(ctx, path)
	if err != nil {
		return selection
	}
	analysis, err := a.sourceAnalyzer().Analyze(ctx, language.Source{Path: path, Content: content})
	if err != nil {
		return selection
	}
	calls := make([]string, 0, len(analysis.Calls))
	for _, call := range analysis.Calls {
		calls = append(calls, call.Name)
	}
	selection.Roles = project.EnrichFileRoles(
		selection.Component, path, selection.Roles, analysis.Decorators, calls,
	)
	return selection
}

func (a *Runner) selectFile(d change.Change, repository *project.Repository) fileSelection {
	path := effectivePath(d)
	selection := fileSelection{Roles: project.FileRoles{project.RoleUnknown}}
	if resolved, roles, ok := repository.Resolve(path); ok {
		selection.Component = resolved
		selection.HasComponent = true
		selection.Roles = roles
	}

	if d.IsBinary {
		selection.Reason = ExcludeBinary
		return selection
	}
	f := a.args.FileFilter
	if f != nil && f.IsUserExcluded(path) {
		selection.Reason = ExcludeUserRule
		return selection
	}
	if f != nil && f.HasInclude() && f.IsUserIncluded(path) {
		if d.IsDeleted {
			selection.Reason = ExcludeDeleted
			return selection
		}
		selection.Target = true
		return selection
	}
	if selection.HasComponent && (selection.Roles.Has(project.RoleManifest) || selection.Roles.Has(project.RoleLock)) {
		// Project context may bypass the global extension allowlist (.lock,
		// .mod, .sum), but never the default path exclusions.
		if allowedext.IsExcludedPath(path) {
			selection.Reason = ExcludeDefaultPath
			return selection
		}
		selection.Context = true
		return selection
	}
	if reason := a.whyExcluded(d); reason != ExcludeNone {
		selection.Reason = reason
		return selection
	}
	if d.IsDeleted {
		selection.Reason = ExcludeDeleted
		return selection
	}
	selection.Target = true
	return selection
}

func (a *Runner) selectionFor(d change.Change) (fileSelection, bool) {
	selection, ok := a.fileSelections[effectivePath(d)]
	return selection, ok
}

func componentKey(c project.Component) string {
	return string(c.Kind) + "\x00" + c.Root
}

// componentFinder projects changed manifest/lock files onto Units after Unit
// formation, like the other cheap ClueFinders. Classification happens before
// splitting, but context still belongs to the final review scope.
type componentFinder struct {
	selections map[string]fileSelection
	clues      map[string][]unit.Clue
}

func (f componentFinder) Find(u unit.Unit) []unit.Clue {
	seen := make(map[string]bool)
	var clues []unit.Clue
	for _, path := range u.Paths() {
		selection, ok := f.selections[path]
		if !ok || !selection.HasComponent {
			continue
		}
		clues = append(clues, sourceRoleClues(path, selection.Roles)...)
		key := componentKey(selection.Component)
		if seen[key] {
			continue
		}
		seen[key] = true
		clues = append(clues, f.clues[key]...)
	}
	return clues
}

func sourceRoleClues(path string, roles project.FileRoles) []unit.Clue {
	var clues []unit.Clue
	if roles.Has(project.RoleEntrypoint) {
		clues = append(clues, unit.Clue{
			Kind: unit.ClueProject, Relation: unit.RelSelf, Ref: path,
			Text: "component role: executable entrypoint; prioritize startup, dependency wiring, configuration, lifecycle, and externally observable impact",
		})
	}
	if roles.Has(project.RoleHandler) {
		clues = append(clues, unit.Clue{
			Kind: unit.ClueProject, Relation: unit.RelSelf, Ref: path,
			Text: "component role: request handler; prioritize input contracts, authentication, validation, service calls, and response semantics",
		})
	}
	return clues
}

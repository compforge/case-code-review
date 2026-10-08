package runner

import (
	"context"
	"strings"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// reviewInput is preparation for a Unit's execution, shared with dry-run.
// It owns no second review state; all facts are read from the captured input and Unit.
type reviewInput struct {
	changeFiles, specCases, rules, seeAlso, prior, projectContext, usageSites string
	usageCount                                                                int
	documents                                                                 []agentgo.AgentMessage
	own, related                                                              []*msg.File
	initial                                                                   []msg.FileContextEntry
	outcomes                                                                  []string
	outlineAttempts                                                           []session.InitialOutlineAttempt
}

func (a *Runner) prepareReviewInput(ctx context.Context, u unit.Unit) reviewInput {
	newPath := u.Path()
	// Build change-files list excluding this Unit's own file(s) — all member paths
	// for a cross-file call-chain Unit, the single path otherwise.
	changeFilesExcludingCurrent := a.buildChangeFilesExcept(u.Paths()...)

	// Render this unit's found context (clues) into the prompt blocks.
	promptClues, documents := separateClueDocuments(u.Clues)
	specCases, specRules, seeAlso, priorFindings := renderClues(promptClues)
	if len(documents) > 0 {
		specCases += "\nDocstrings are supplied as separate document messages with their source references and relationships."
	}
	projectContext := renderProjectContext(u.Clues)
	// Pre-grep where else the repo references the changed symbols ({{usage_sites}}).
	usageSites, usageCount, usagePaths := a.renderUsageSites(u)
	// Per-function @rule (from clues) augments the path-glob rule.json criteria;
	// both flow into {{system_rule}} (plan + main).
	rule := a.resolveSystemRule(strings.ToLower(newPath))
	if specRules != "" {
		if rule != "" {
			rule += "\n"
		}
		rule += specRules
	}

	own, related, outcomes := a.preloadReviewFiles(ctx, u)
	initial, attempts := a.initialFileContext(ctx, u, usagePaths, own, related)
	return reviewInput{changeFiles: changeFilesExcludingCurrent, specCases: specCases, rules: rule, seeAlso: seeAlso, prior: priorFindings, projectContext: projectContext, usageSites: usageSites, usageCount: usageCount, documents: documents, own: own, related: related, initial: initial, outcomes: outcomes, outlineAttempts: attempts}
}

func (a *Runner) reviewMessages(u unit.Unit, input reviewInput, planResult string) []agentgo.AgentMessage {
	rawMsgs := a.args.Template.MainTask.Messages
	buildMessages := func(unitSource, relatedSource string) []llm.Message {
		messages := make([]llm.Message, 0, len(rawMsgs))
		for _, m := range rawMsgs {
			content := m.Content
			content = strings.ReplaceAll(content, "{{current_system_date_time}}", a.currentDate)
			content = strings.ReplaceAll(content, "{{current_file_path}}", u.Path())
			content = strings.ReplaceAll(content, "{{system_rule}}", input.rules)
			content = strings.ReplaceAll(content, "{{change_files}}", input.changeFiles)
			content = strings.ReplaceAll(content, "{{diff}}", "(provided as a separate review diff message)")
			// High-confidence source already implied by the Unit is appended as
			// separate File messages so early rounds do not fetch it again.
			content = strings.ReplaceAll(content, "{{unit_source}}", unitSource)
			content = strings.ReplaceAll(content, "{{related_source}}", relatedSource)
			// Pre-grepped blast-radius map of the changed symbols.
			content = strings.ReplaceAll(content, "{{usage_sites}}", input.usageSites)
			content = strings.ReplaceAll(content, "{{requirement_background}}", a.args.Background)
			content = strings.ReplaceAll(content, "{{spec_cases}}", input.specCases)
			content = strings.ReplaceAll(content, "{{project_context}}", input.projectContext)
			// Curated see-also pointers; the reviewer fetches content on demand.
			content = strings.ReplaceAll(content, "{{see_also}}", input.seeAlso)
			// Run-level ranked symbol map (real names, anti-guessing).
			content = strings.ReplaceAll(content, "{{repo_map}}", a.repoMap)
			// A previous review's findings on this unit, to reconcile against the change.
			content = strings.ReplaceAll(content, "{{prior_findings}}", input.prior)
			// Always substitute the {{plan_guidance}} token so the literal placeholder
			// never leaks into the rendered prompt. When the plan phase produced no
			// output, strip the surrounding "### Review Plan (Optional)\n…\n\n" wrapper
			// (any language variant) so the LLM does not see a dangling section header.
			// Strip MUST run before ReplaceAll: the regex requires the literal
			// {{plan_guidance}} token to be present; if we replace first, the token
			// is gone and the wrapper can't be matched.
			if planResult == "" {
				content = stripEmptyPlanBlock(content)
			}
			content = strings.ReplaceAll(content, "{{plan_guidance}}", planResult)
			messages = append(messages, llm.NewTextMessage(m.Role, content))
		}
		return messages
	}

	domain := a.assembleReviewMessages(buildMessages, input.own, input.related, input.initial, reviewDiffMessage(u))
	return append(domain, input.documents...)
}

func reviewDiffMessage(u unit.Unit) *msg.Diff {
	var sources []msg.SourceArtifact
	for _, fragment := range u.Fragments {
		before := msg.SourceArtifact{Path: fragment.OldPath, Snapshot: msg.SnapshotBaseline}
		if before.Path == "" {
			before.Path = fragment.Path
		}
		after := msg.SourceArtifact{Path: fragment.Path, Snapshot: msg.SnapshotCurrent}
		for _, hunk := range change.ParseHunks(fragment.Diff) {
			oldLine, newLine := hunk.OldStart, hunk.NewStart
			for _, line := range hunk.Lines {
				if line.Type != change.HunkAdded {
					before.Lines = append(before.Lines, msg.SourceArtifactLine{Number: oldLine, Text: line.Content})
					oldLine++
				}
				if line.Type != change.HunkDeleted {
					after.Lines = append(after.Lines, msg.SourceArtifactLine{Number: newLine, Text: line.Content})
					newLine++
				}
			}
		}
		if len(before.Lines) > 0 {
			sources = append(sources, before)
		}
		if len(after.Lines) > 0 {
			sources = append(sources, after)
		}
	}
	return msg.NewDiff(u.Paths(), u.Diff()).ConfigurePresentation("Review diff ("+u.ID+")", 30).ConfigureReviewSource(sources)
}

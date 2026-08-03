package main

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

type jsonlEvent struct {
	Type        string           `json:"type"`
	Sequence    int64            `json:"sequence"`
	Version     string           `json:"version,omitempty"`
	SessionID   string           `json:"session_id,omitempty"`
	SessionPath string           `json:"session_path,omitempty"`
	Finding     *finding.Finding `json:"finding,omitempty"`
	Warning     *runner.Warning  `json:"warning,omitempty"`
	Result      *jsonOutput      `json:"result,omitempty"`
}

// jsonlEmitter serializes callback events from concurrent Review 2 lanes into
// complete stdout lines. Encoder failures are remembered and surfaced after
// the run instead of interrupting Trial or session persistence.
type jsonlEmitter struct {
	mu       sync.Mutex
	encoder  *json.Encoder
	sequence int64
	err      error
}

func newJSONLEmitter(w io.Writer) *jsonlEmitter {
	return &jsonlEmitter{encoder: json.NewEncoder(w)}
}

func (e *jsonlEmitter) emit(event jsonlEvent) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return
	}
	e.sequence++
	event.Sequence = e.sequence
	e.err = e.encoder.Encode(event)
}

func (e *jsonlEmitter) start(sh *session.SessionHistory) {
	event := jsonlEvent{Type: "run_started", Version: Version}
	if sh != nil {
		event.SessionID = sh.SessionID
		if path, err := sh.TranscriptPath(); err == nil {
			event.SessionPath = path
		}
	}
	e.emit(event)
}

func (e *jsonlEmitter) finding(value finding.Finding) {
	e.emit(jsonlEvent{Type: "finding", Finding: &value})
}

func (e *jsonlEmitter) finish(result jsonOutput) {
	for i := range result.Warnings {
		warning := result.Warnings[i]
		e.emit(jsonlEvent{Type: "warning", Warning: &warning})
	}
	e.emit(jsonlEvent{Type: "run_finished", Result: &result})
}

func (e *jsonlEmitter) Error() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func emitJSONLRunResult(
	ctx context.Context,
	ag ResultProvider,
	comments []finding.Finding,
	startTime time.Time,
	emitter *jsonlEmitter,
) error {
	comments = finding.ResolveLineNumbers(comments, ag.Changes())
	duration := time.Since(startTime)
	telemetry.RecordReviewDuration(ctx, duration)
	if len(comments) > 0 {
		telemetry.RecordCommentsGenerated(ctx, int64(len(comments)))
	}

	if len(comments) == 0 && ag.FilesReviewed() == 0 {
		emitter.finish(buildJSONNoFiles(ag.Session()))
		return emitter.Error()
	}
	result := buildJSONWithWarnings(comments, ag.Warnings(), ag.FilesReviewed(),
		ag.TotalInputTokens(), ag.TotalOutputTokens(), ag.TotalTokensUsed(),
		ag.TotalCacheReadTokens(), ag.TotalCacheWriteTokens(), duration,
		ag.ProjectSummary(), ag.ToolCalls(), ag.ModelsUsed(), ag.Session())
	emitter.finish(result)
	return emitter.Error()
}

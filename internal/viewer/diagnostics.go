package viewer

import "fmt"

// SessionDiagnostics is a viewer-only projection of the review pipeline. It
// makes partial runs explicit without adding another persisted domain model.
type SessionDiagnostics struct {
	Status      string
	StatusLabel string
	Summary     string

	HasSessionEnd     bool
	DiffFiles         int
	ReviewFiles       int
	Review1Units      int
	Review1Executions int
	Review1Done       int
	Review1Missed     int
	Review2Lanes      int
	Review2Executions int
	Review2Done       int
	Review2Missed     int
	Hypotheses        int
	Assessments       int
	Unassessed        int
	TrialPassed       int
	TrialBlocked      int
	Findings          int
	CoveredReads      int
	RepeatedReads     int

	Alerts []DiagnosticAlert
}

type DiagnosticAlert struct {
	Level  string
	Title  string
	Detail string
}

type sessionSignals struct {
	hasSessionEnd bool
	hypotheses    map[string]bool
	assessments   map[string]bool
	trials        map[string]bool
	findings      int
}

func newSessionSignals() sessionSignals {
	return sessionSignals{
		hypotheses:  make(map[string]bool),
		assessments: make(map[string]bool),
		trials:      make(map[string]bool),
	}
}

func (s *sessionSignals) observeArtifact(kind string, payload map[string]any) {
	hypothesisID, _ := payload["hypothesis_id"].(string)
	switch kind {
	case "review_hypothesis":
		id, _ := payload["id"].(string)
		if id != "" {
			s.hypotheses[id] = true
		}
	case "review_assessment":
		if hypothesisID != "" {
			s.assessments[hypothesisID] = true
		}
	case "trial_decision":
		if hypothesisID != "" {
			passed, _ := payload["passed_trial"].(bool)
			s.trials[hypothesisID] = passed
		}
	}
}

func buildSessionDiagnostics(vs *ViewSession, signals sessionSignals) SessionDiagnostics {
	d := SessionDiagnostics{
		HasSessionEnd: signals.hasSessionEnd,
		DiffFiles:     vs.Summary.DiffFileCount,
		ReviewFiles:   vs.Summary.FileCount,
		Hypotheses:    len(signals.hypotheses),
		Assessments:   len(signals.assessments),
		Findings:      signals.findings,
	}
	for id := range signals.hypotheses {
		if !signals.assessments[id] {
			d.Unassessed++
		}
	}
	for _, passed := range signals.trials {
		if passed {
			d.TrialPassed++
		} else {
			d.TrialBlocked++
		}
	}
	for _, review := range vs.Reviews {
		switch review.Stage {
		case Review1Stage:
			d.Review1Units++
			d.Review1Executions += len(review.Executions)
			d.Review1Done += review.ExecutionDone
			d.Review1Missed += review.ExecutionMissed
			d.CoveredReads += review.FileReads.CoveredRequests
			d.RepeatedReads += review.FileReads.SamePathRepeats
		case Review2Stage:
			d.Review2Lanes++
			d.Review2Executions += len(review.Executions)
			d.Review2Done += review.ExecutionDone
			d.Review2Missed += review.ExecutionMissed
		}
	}

	switch {
	case !d.HasSessionEnd:
		d.Status, d.StatusLabel = "error", "No session_end"
		d.Summary = "The run may still be active or was interrupted; do not interpret its Findings as a complete verdict."
		d.Alerts = append(d.Alerts, DiagnosticAlert{
			Level: "error", Title: "Session did not close",
			Detail: "The JSONL has no session_end record, so totals and downstream decisions may be incomplete.",
		})
	case d.Review1Missed+d.Review2Missed+d.Unassessed > 0:
		d.Status, d.StatusLabel = "warning", "Partial"
		d.Summary = "Some review work did not converge; zero Findings must not be read as clean."
	default:
		d.Status, d.StatusLabel = "success", "Complete"
		switch {
		case d.Findings > 0:
			d.Summary = fmt.Sprintf("Review completed and delivered %d Finding(s).", d.Findings)
		case d.TrialBlocked > 0:
			d.Summary = fmt.Sprintf("Review completed; Trial blocked %d assessed Hypothesis(es).", d.TrialBlocked)
		default:
			d.Summary = "Review completed without a delivered Finding."
		}
	}

	if d.Review1Missed > 0 {
		d.Alerts = append(d.Alerts, DiagnosticAlert{
			Level: "warning", Title: fmt.Sprintf("%d Review 1 Execution(s) incomplete", d.Review1Missed),
			Detail: "Inspect the highlighted Unit rows for timeout, truncation, or execution errors.",
		})
	}
	if d.Review2Missed > 0 {
		d.Alerts = append(d.Alerts, DiagnosticAlert{
			Level: "warning", Title: fmt.Sprintf("%d Review 2 Execution(s) incomplete", d.Review2Missed),
			Detail: "Inspect the owning Lane; completed sibling Executions and their Assessments remain valid.",
		})
	}
	if d.Unassessed > 0 {
		d.Alerts = append(d.Alerts, DiagnosticAlert{
			Level: "warning", Title: fmt.Sprintf("%d Hypothesis(es) lack Assessment", d.Unassessed),
			Detail: "These Hypotheses cannot pass Trial and are dropped from the delivered result.",
		})
	}
	if d.CoveredReads+d.RepeatedReads > 0 {
		d.Alerts = append(d.Alerts, DiagnosticAlert{
			Level: "info", Title: "Avoidable file reads detected",
			Detail: fmt.Sprintf("%d read(s) were already covered by context; %d repeated a path within the same Unit.", d.CoveredReads, d.RepeatedReads),
		})
	}
	return d
}

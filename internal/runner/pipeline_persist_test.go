package runner

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestPipelineArtifactsCarryTimingAndJoinKeys(t *testing.T) {
	session.UseTestSessions()
	home := t.TempDir()
	t.Setenv("HOME", home)

	history := session.New(filepath.Join(home, "repo"), "main", "model", session.SessionOptions{})
	runner := &Runner{session: history}
	reviewUnit := unit.UnitOf(unit.Fragment{
		Path: "a.go", Symbols: []string{"a.go::F"}, Insertions: 3, Deletions: 1,
	})
	hypothesis := unitreview.Hypothesis{
		ID: "h-1", OriginUnit: reviewUnit.ID, Path: "a.go", Content: "bug",
	}
	reviewUnit.AddHypothesis(hypothesis)
	input := hypothesisreview.ReviewInput{
		LaneID: "l-1", Unit: reviewUnit, Hypothesis: hypothesis,
	}
	assessment := hypothesisreview.Assessment{
		HypothesisID: "h-1", LaneID: "l-1", SubmissionIndex: 1,
		Support: unit.Supported, Attribution: unit.Caused,
		Value: unit.Actionable, Novelty: unit.Novel, Reason: "confirmed",
	}
	reviewUnit.AddAssessment(assessment)

	runner.persistFormedUnits([]unit.Unit{reviewUnit}, 12*time.Millisecond)
	runner.persistUnitReviewStart(reviewUnit)
	runner.persistHypothesis(hypothesis)
	runner.persistLaneAssignment(input, "lane_assigned")
	runner.persistHypothesisReviewStart(input)
	runner.persistAssessmentSubmission(input, hypothesisreview.AssessmentSubmission{Assessment: assessment})
	runner.persistHypothesisReviewExecution(input, harness.ExecutionResult{
		ID: "exec-2", State: harness.OutcomeCompleted, Duration: 25 * time.Millisecond,
	})
	runner.persistTrialDecisions([]unit.Unit{reviewUnit}, []unit.TrialDecision{{
		HypothesisID: "h-1", Passed: true, Delivered: true,
	}})
	runner.persistFindings([]finding.Finding{{HypothesisID: "h-1", Path: "a.go", Content: "bug"}}, []unit.Unit{reviewUnit})
	history.Finalize()

	paths, err := filepath.Glob(filepath.Join(home, ".casecodereview", "test-sessions", "*", history.SessionID+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("session files = %v err=%v", paths, err)
	}
	file, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	artifacts := map[string]map[string]any{}
	var delivered map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if _, ok := record["elapsed_ms"].(float64); !ok {
			t.Fatalf("record lacks elapsed_ms: %+v", record)
		}
		if record["type"] == "artifact" {
			artifacts[record["artifact_kind"].(string)] = record["data"].(map[string]any)
		}
		if record["type"] == "finding" {
			delivered = record
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	if formation := artifacts["unit_formation"]; formation["duration_ms"] != float64(12) || formation["unit_count"] != float64(1) {
		t.Fatalf("unit_formation = %+v", formation)
	}
	if unitData := artifacts["review_unit"]; unitData["unit_id"] != reviewUnit.ID || unitData["insertions"] != float64(3) {
		t.Fatalf("review_unit = %+v", unitData)
	}
	if reviewStart := artifacts["unit_review_start"]; reviewStart["unit_id"] != reviewUnit.ID || reviewStart["scope"] != string(unit.ScopeFunc) {
		t.Fatalf("unit_review_start = %+v", reviewStart)
	}
	if assignment := artifacts["review_lane_assignment"]; assignment["origin_unit"] != reviewUnit.ID || assignment["hypothesis_id"] != "h-1" {
		t.Fatalf("review_lane_assignment = %+v", assignment)
	}
	if reviewStart := artifacts["hypothesis_review_start"]; reviewStart["origin_unit"] != reviewUnit.ID || reviewStart["lane_id"] != "l-1" {
		t.Fatalf("hypothesis_review_start = %+v", reviewStart)
	}
	if submitted := artifacts["review_assessment"]; submitted["origin_unit"] != reviewUnit.ID || submitted["lane_id"] != "l-1" {
		t.Fatalf("review_assessment = %+v", submitted)
	}
	if execution := artifacts["hypothesis_review_execution"]; execution["execution_id"] != "exec-2" || execution["duration_ms"] != float64(25) {
		t.Fatalf("hypothesis_review_execution = %+v", execution)
	}
	if decision := artifacts["trial_decision"]; decision["origin_unit"] != reviewUnit.ID || decision["assessment_submission_index"] != float64(1) {
		t.Fatalf("trial_decision = %+v", decision)
	}
	if delivered["origin_unit"] != reviewUnit.ID || delivered["lane_id"] != "l-1" || delivered["hypothesis_id"] != "h-1" {
		t.Fatalf("finding = %+v", delivered)
	}
}

func TestDeliverFindingPublishesPreparedFindingOnce(t *testing.T) {
	session.UseTestSessions()
	home := t.TempDir()
	t.Setenv("HOME", home)
	history := session.New(filepath.Join(home, "repo"), "main", "model", session.SessionOptions{})

	var observed []finding.Finding
	var transcriptAtCallback string
	runner := &Runner{
		args: Args{
			RepoDir: filepath.Join(home, "repo"), Findings: finding.NewCollector(),
			OnFinding: func(comment finding.Finding) {
				observed = append(observed, comment)
				path, _ := history.TranscriptPath()
				data, _ := os.ReadFile(path)
				transcriptAtCallback = string(data)
			},
		},
		session: history,
	}
	reviewUnit := unit.UnitOf(unit.Fragment{Path: "a.go", Symbols: []string{"a.go::F"}})
	hypothesis := unitreview.Hypothesis{ID: "h-1", OriginUnit: reviewUnit.ID, Path: "a.go", Content: "bug"}
	reviewUnit.AddHypothesis(hypothesis)
	reviewUnit.AddAssessment(hypothesisreview.Assessment{HypothesisID: "h-1", LaneID: "lane-1"})

	got := runner.deliverFinding(finding.Finding{HypothesisID: "h-1", Path: "a.go", Content: "bug"}, []unit.Unit{reviewUnit})
	if got.Fingerprint == "" {
		t.Fatal("delivered finding lacks fingerprint")
	}
	if len(observed) != 1 || observed[0] != got {
		t.Fatalf("observed = %+v, got %+v", observed, got)
	}
	if comments := runner.args.Findings.Comments(); len(comments) != 1 || comments[0] != got {
		t.Fatalf("collector = %+v", comments)
	}
	if !strings.Contains(transcriptAtCallback, `"type":"finding"`) {
		t.Fatalf("finding was not visible in transcript before callback: %q", transcriptAtCallback)
	}
}

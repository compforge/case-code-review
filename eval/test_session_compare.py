import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from session_compare import compare, markdown, read_session


def artifact(kind, **data):
    return {"type": "artifact", "artifact_kind": kind, "data": data}


def execution(outcome):
    return {"type": "timeline_update", "timeline_id": "run", "update": {"Stages": [{
        "id": "exec", "name": "execution", "revision": 2,
        "started_at": "2026-10-01T00:00:00Z", "finished_at": "2026-10-01T00:00:01Z",
        "attributes": {"outcome": outcome},
    }]}}


def target(id="f", path="a.go", start=10, end=12, side="after", old_path=""):
    return {"id": id, "path": path, "old_path": old_path,
            f"{side}_edits": [{"Start": start, "End": end}]}


def unit(id="u", targets=None, outcome="completed"):
    events = [artifact("review_unit", unit_id=id, targets=targets or [target()])]
    if outcome is not None:
        events.append({"type": "debrief", "kind": "unit", "scope_id": id, "outcome": outcome,
                       "tokens": {"prompt_tokens": 10}, "rounds": {"main_task": 1}})
    return events


def finding(**kw):
    return {"type": "finding", "path": "a.go", "start_line": 10, "end_line": 10,
            "side": "new", "category": "bug", "origin_unit": "u", "hypothesis_id": "h",
            "existing_code": "return *ptr", "content": "nil dereference", **kw}


def pipeline(support="contradicted", assessed=True, passed=False, delivered=False):
    f = finding()
    events = [artifact("review_hypothesis", id="h", **{k: v for k, v in f.items() if k not in ("type", "hypothesis_id")})]
    decision = {"hypothesis_id": "h", "origin_unit": "u", "passed_trial": passed, "delivered": delivered}
    if assessed:
        events.append(artifact("review_assessment", hypothesis_id="h", lane_id="l", submission_index=1,
                               support=support, attribution="caused", value="actionable", novelty="new", reason="evidence"))
        decision.update(lane_id="l", assessment_submission_index=1)
    events.append(artifact("trial_decision", **decision))
    return events


class SessionComparisonTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.count = 0

    def session(self, events=(), digest="same", closed=True, legacy=False, tail=""):
        self.count += 1
        path = Path(self.temp.name) / f"{self.count}.jsonl"
        records = [{"type": "session_start", "model": "test"}]
        if not legacy:
            records.append(artifact("review_input", version=1, repository="repo", change_digest=digest))
        records.extend(events)
        if closed:
            records.append({"type": "session_end", "duration_seconds": 1.5})
        path.write_text("".join(json.dumps(r) + "\n" for r in records) + tail, encoding="utf-8")
        return read_session(path)

    def test_wording_and_line_drift_match_snippet_but_not_same_symbol(self):
        before = self.session([finding(symbol_id="F")])
        after = self.session([finding(content="crash here", existing_code=" return  *ptr\n", start_line=22, symbol_id="F"),
                              finding(existing_code="return nil", content="wrong default", symbol_id="F", hypothesis_id="h2")])
        result = compare(before, after)
        self.assertEqual(len(result["persisting"]), 1)
        self.assertEqual(result["persisting"][0]["matched_by"], "evidence")
        self.assertEqual(len(result["new"]), 1)

    def test_multiset_and_categories_preserved(self):
        before = self.session([finding(), finding()])
        after = self.session([finding(), finding(category="security")])
        result = compare(before, after)
        self.assertEqual(len(result["persisting"]), 1)
        self.assertEqual(len(result["absent"]), 1)
        self.assertEqual(len(result["new"]), 1)

    def test_one_completed_target_does_not_cover_timed_out_neighbor(self):
        before = self.session([*unit(), *unit("v", [target("g", start=30, end=31)]),
                               finding(), finding(start_line=30, origin_unit="v", hypothesis_id="h2", existing_code="panic()")])
        after = self.session([*unit(), *unit("v", [target("g", start=30, end=31)], "timeout")])
        result = compare(before, after)
        self.assertEqual([i["status"] for i in result["absent"]], ["not_reported", "not_reviewed"])
        self.assertEqual([i["coverage"] for i in result["absent"]], ["completed", "incomplete"])

    def test_regrouping_and_splitting_use_edit_range_union(self):
        before = self.session([*unit(), finding()])
        after = self.session([*unit("new-unit", [target("split-1", start=10, end=10), target("split-2", start=11, end=12)])])
        self.assertEqual(compare(before, after)["absent"][0]["coverage"], "completed")
        partial = self.session(unit("new-unit", [target("split-1", start=10, end=11)]))
        self.assertEqual(compare(before, partial)["absent"][0]["coverage"], "outside_scope")

    def test_selection_and_context_reads_are_not_completion(self):
        before = self.session([*unit(), finding()])
        selected = self.session(unit(outcome=None))
        self.assertEqual(compare(before, selected)["absent"][0]["coverage"], "incomplete")
        context = self.session([{"type": "tool_result", "path": "a.go", "content": "read all"}])
        self.assertEqual(compare(before, context)["absent"][0]["coverage"], "outside_scope")

    def test_old_side_and_rename_source_coordinates(self):
        old = target(path="new.go", old_path="old.go", side="before")
        f = finding(path="new.go", old_path="old.go", side="old")
        before = self.session([*unit(targets=[old]), f])
        after = self.session(unit(targets=[target(path="new.go")]))
        self.assertEqual(compare(before, after)["absent"][0]["coverage"], "outside_scope")
        after = self.session(unit("different", [old]))
        self.assertEqual(compare(before, after)["absent"][0]["coverage"], "completed")
        after = self.session([finding(path="new.go", side="new")])
        self.assertEqual(len(compare(before, after)["persisting"]), 0)

    def test_legacy_different_input_or_corruption_cannot_prove_absence(self):
        before = self.session([*unit(), finding()])
        for options in ({"legacy": True}, {"digest": "changed"}, {"tail": '{"type":'}):
            with self.subTest(options=options):
                after = self.session(unit(), **options)
                result = compare(before, after)
                self.assertEqual(result["absent"][0]["coverage"], "unknown")
                self.assertEqual(result["absent"][0]["status"], "not_reviewed")

    def test_missing_ranges_and_ambiguous_issue_target_are_unknown(self):
        before = self.session([*unit(targets=[{"id": "legacy", "path": "a.go"}]), finding()])
        after = self.session(unit())
        self.assertEqual(compare(before, after)["absent"][0]["coverage"], "unknown")
        before = self.session([*unit(targets=[target(), target("g", start=30, end=31)]), finding(start_line=20)])
        self.assertEqual(compare(before, after)["absent"][0]["coverage"], "unknown")

    def test_trial_uses_linked_assessment_not_last_submission(self):
        before = self.session([*unit(), finding()])
        after = self.session([*unit(), *pipeline(),
                              artifact("review_assessment", hypothesis_id="h", lane_id="l", submission_index=2, support="supported")])
        item = compare(before, after)["absent"][0]
        self.assertEqual(item["status"], "not_reported")
        self.assertEqual(item["stage"]["assessment"]["support"], "contradicted")
        self.assertEqual(item["stage"]["submission_index"], 1)

    def test_false_trial_without_assessment_is_incomplete(self):
        before = self.session([*unit(), finding()])
        after = self.session([*unit(), *pipeline(assessed=False),
                              artifact("hypothesis_review_execution", hypothesis_id="h", execution_id="exec"), execution("timeout")])
        item = compare(before, after)["absent"][0]
        self.assertEqual(item["status"], "not_reviewed")
        self.assertEqual(item["stage"]["state"], "incomplete")
        self.assertEqual(item["stage"]["execution_outcome"], "timeout")

    def test_duplicate_and_not_proposed_explained_separately(self):
        before = self.session([*unit(), finding()])
        after = self.session([*unit(), *pipeline(passed=True)])
        self.assertEqual(compare(before, after)["absent"][0]["stage"]["state"], "duplicate")
        after = self.session(unit())
        self.assertEqual(compare(before, after)["absent"][0]["stage"]["state"], "no_matching_hypothesis")

    def test_hypothesis_cannot_explain_multiple_missing_occurrences(self):
        before = self.session([*unit(), finding(), finding()])
        after = self.session([*unit(), *pipeline()])
        stages = [i["stage"]["state"] for i in compare(before, after)["absent"]]
        self.assertEqual(stages, ["filtered", "no_matching_hypothesis"])

    def test_new_reports_explain_baseline_coverage(self):
        before = self.session(unit(outcome="timeout"))
        after = self.session([*unit(), finding()])
        self.assertEqual(compare(before, after)["new"][0]["baseline_coverage"], "incomplete")

    def test_unclosed_session_keeps_positive_evidence_and_partial_cost(self):
        before = self.session([*unit(), finding()])
        after = self.session([*unit(), finding()], closed=False)
        result = compare(before, after)
        self.assertEqual(len(result["persisting"]), 1)
        self.assertEqual(result["target_coverage"][0]["status"], "incomplete")
        self.assertIsNone(result["costs"]["duration_s"]["delta"])

    def test_costs_include_lane_and_unclosed_calls_without_debrief(self):
        response = {"type": "llm_response", "uuid": "r", "stage_id": "r1", "scope_id": "l",
                    "usage": {"prompt_tokens": 20, "completion_tokens": 5, "cache_write_tokens": 2}}
        session = self.session([*unit(), {"type": "llm_request", "stage_id": "r1"}, response, response,
                                {"type": "llm_request", "stage_id": "unfinished"},
                                {"type": "llm_error", "stage_id": "failed"}], closed=False)
        summary = session.summary()
        self.assertEqual(summary["units"], 1)
        self.assertEqual(summary["prompt_tokens"], 20)
        self.assertEqual(summary["completion_tokens"], 5)
        self.assertEqual(summary["rounds"], 3)
        self.assertEqual(summary["cache_write"], 2)
        self.assertEqual(summary["unknown_usage"], 2)
        self.assertEqual(summary["pending_calls"], 1)

    def test_system_timeout_fallback_is_not_a_completed_assessment(self):
        before = self.session([*unit(), finding()])
        events = pipeline(support="insufficient")
        for event in events:
            if event.get("artifact_kind") == "review_assessment":
                event["data"]["reviewer_alias"] = "system"
        after = self.session([*unit(), *events,
                              artifact("hypothesis_review_execution", hypothesis_id="h", execution_id="exec"),
                              execution("timeout")])
        absent = compare(before, after)["absent"][0]
        self.assertEqual(absent["coverage"], "incomplete")
        self.assertEqual(absent["status"], "not_reviewed")
        self.assertEqual(absent["stage"]["execution_outcome"], "timeout")

    def test_legacy_findings_recover_snippet_from_hypothesis(self):
        f = finding()
        del f["existing_code"]
        session = self.session([f, *pipeline()])
        self.assertEqual(session.findings[0].existing_code, "return *ptr")

    def test_delivered_decision_without_finding_is_an_evidence_gap(self):
        before = self.session([*unit(), finding()])
        after = self.session([*unit(), *pipeline(passed=True, delivered=True)])
        item = compare(before, after)["absent"][0]
        self.assertEqual(item["status"], "not_reviewed")
        self.assertEqual(item["stage"]["state"], "incomplete")

    def test_unrelated_repositories_do_not_match_identical_source(self):
        before = self.session([finding()])
        after = self.session([finding(), artifact("review_input", version=1, repository="other", change_digest="same")])
        self.assertFalse(compare(before, after)["persisting"])

    def test_bad_issue_record_does_not_discard_other_positive_evidence(self):
        session = self.session([finding(), {"type": "finding", "path": None}])
        self.assertEqual(len(session.findings), 1)
        self.assertIn("invalid finding", session.gaps)

    def test_cli_writes_json_and_markdown_without_models(self):
        before = self.session([*unit(), finding()])
        after = self.session(unit())
        out = Path(self.temp.name) / "report"
        subprocess.run([sys.executable, str(Path(__file__).with_name("session_compare.py")), before.path, after.path,
                        "--out", str(out)], check=True, capture_output=True)
        report = json.loads((out / "comparison.json").read_text())
        self.assertEqual(report["absent"][0]["status"], "not_reported")
        self.assertEqual((out / "REPORT.md").read_text(), markdown(report))


if __name__ == "__main__":
    unittest.main()

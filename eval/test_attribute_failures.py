import unittest

from attribute_failures import (
    DELIVERED,
    EXECUTION,
    FINDING,
    FORMATION,
    HYPOTHESIS_REVIEW,
    TRIAL,
    UNIT_REVIEW,
    attribute_issue,
    select_expected_issues,
)


def execution_stage(fields: dict) -> dict:
    return {"type": "timeline_update", "timeline_id": "run", "update": {"Stages": [{
        "id": fields["scope_id"], "revision": 2, "name": "execution", "parent_id": "operation:run",
        "started_at": "2026-10-01T00:00:00Z", "finished_at": "2026-10-01T00:00:01Z",
        "status": "failed", "fields": fields,
    }]}}


def issue() -> dict:
    return {"id": "known-1", "path": "a.go", "line": 12}


def session(*records: dict) -> list[dict]:
    return [{"type": "session_start"}, *records, {"type": "session_end"}]


def unit(outcome: str = "completed") -> dict:
    return {
        "type": "debrief",
        "kind": "unit",
        "scope_id": "unit-a",
        "paths": ["a.go"],
        "outcome": outcome,
    }


def hypothesis() -> dict:
    return {
        "type": "artifact",
        "artifact_kind": "review_hypothesis",
        "data": {
            "id": "h-1",
            "fingerprint": "hf-1",
            "origin_unit": "unit-a",
            "path": "a.go",
            "start_line": 10,
            "end_line": 14,
        },
    }


def assessment(**overrides) -> dict:
    data = {
        "hypothesis_id": "h-1",
        "submission_index": 1,
        "support": "supported",
        "attribution": "caused",
        "value": "actionable",
        "novelty": "new",
    }
    data.update(overrides)
    return {
        "type": "artifact",
        "artifact_kind": "review_assessment",
        "data": data,
    }


def trial(passed: bool, delivered: bool) -> dict:
    return {
        "type": "artifact",
        "artifact_kind": "trial_decision",
        "data": {
            "hypothesis_id": "h-1",
            "passed_trial": passed,
            "delivered": delivered,
        },
    }


class AttributeFailuresTest(unittest.TestCase):
    def test_selects_only_expected_label_dataset_rows(self):
        selected = select_expected_issues(
            [
                {"id": "important", "label": "important"},
                {"id": "missed", "label": "missed"},
                {"id": "wrong", "label": "wrong"},
                {"id": "explicit", "expected_delivery": True},
                {"id": "negative", "expected_delivery": False},
            ]
        )
        self.assertEqual(
            [record["id"] for record in selected],
            ["important", "missed", "explicit"],
        )

    def test_attributes_missing_unit_to_formation(self):
        result = attribute_issue(issue(), session())
        self.assertEqual(result["stage"], FORMATION)

    def test_attributes_interrupted_unit_to_execution(self):
        records = [
            {"type": "session_start"},
            execution_stage({
                "kind": "unit",
                "scope_id": "unit-a",
                "paths": ["a.go"],
                "outcome": "truncated",
            }),
        ]
        result = attribute_issue(issue(), records)
        self.assertEqual(result["stage"], EXECUTION)

    def test_attributes_completed_unit_without_hypothesis_to_unit_review(self):
        result = attribute_issue(issue(), session(unit()))
        self.assertEqual(result["stage"], UNIT_REVIEW)

    def test_attributes_missing_assessment_to_hypothesis_review(self):
        result = attribute_issue(issue(), session(unit(), hypothesis()))
        self.assertEqual(result["stage"], HYPOTHESIS_REVIEW)

    def test_attributes_incomplete_lane_to_execution(self):
        records = session(
            unit(),
            hypothesis(),
            {
                "type": "artifact",
                "artifact_kind": "review_lane_assignment",
                "data": {"hypothesis_id": "h-1", "lane_id": "lane-1"},
            },
            execution_stage({
                "kind": "lane",
                "scope_id": "hypothesis_review:lane-1",
                "outcome": "timed_out",
            }),
        )
        result = attribute_issue(issue(), records)
        self.assertEqual(result["stage"], EXECUTION)

    def test_attributes_lane_setup_failure_to_execution(self):
        records = session(
            unit(),
            hypothesis(),
            {
                "type": "artifact",
                "artifact_kind": "review_lane_assignment",
                "data": {"hypothesis_id": "h-1", "lane_id": "lane-1"},
            },
        )
        result = attribute_issue(issue(), records)
        self.assertEqual(result["stage"], EXECUTION)

    def test_attributes_rejected_assessment_to_hypothesis_review(self):
        result = attribute_issue(
            issue(), session(unit(), hypothesis(), assessment(support="unsupported"))
        )
        self.assertEqual(result["stage"], HYPOTHESIS_REVIEW)

    def test_attributes_rejected_decision_to_trial(self):
        result = attribute_issue(
            issue(), session(unit(), hypothesis(), assessment(), trial(False, False))
        )
        self.assertEqual(result["stage"], TRIAL)

    def test_attributes_missing_persisted_finding_to_finding(self):
        result = attribute_issue(
            issue(), session(unit(), hypothesis(), assessment(), trial(True, True))
        )
        self.assertEqual(result["stage"], FINDING)

    def test_reports_delivered_finding(self):
        result = attribute_issue(
            issue(),
            session(
                unit(),
                hypothesis(),
                assessment(),
                trial(True, True),
                {
                    "type": "finding",
                    "hypothesis_id": "h-1",
                    "path": "a.go",
                    "start_line": 11,
                    "end_line": 12,
                    "fingerprint": "finding-1",
                },
            ),
        )
        self.assertEqual(result["stage"], DELIVERED)

    def test_prefers_stable_hypothesis_identity_over_location(self):
        known = {
            "id": "known-1",
            "path": "a.go",
            "line": 90,
            "engine": {"hypothesis": {"fingerprint": "hf-1"}},
        }
        result = attribute_issue(
            known, session(unit(), hypothesis(), assessment(support="unsupported"))
        )
        self.assertEqual(result["stage"], HYPOTHESIS_REVIEW)
        self.assertEqual(
            result["evidence"]["hypothesis_matches"],
            {"h-1": "hypothesis_fingerprint"},
        )

    def test_identity_without_line_does_not_match_unrelated_hypothesis_by_path(self):
        known = {
            "id": "known-1",
            "path": "a.go",
            "hypothesis_id": "h-other",
        }
        result = attribute_issue(known, session(unit(), hypothesis()))
        self.assertEqual(result["stage"], UNIT_REVIEW)

    def test_identity_without_line_does_not_deliver_unrelated_finding_by_path(self):
        known = {
            "id": "known-1",
            "path": "a.go",
            "hypothesis_id": "h-1",
        }
        result = attribute_issue(
            known,
            session(
                unit(),
                hypothesis(),
                {
                    "type": "finding",
                    "hypothesis_id": "h-other",
                    "path": "a.go",
                    "start_line": 10,
                    "end_line": 14,
                },
            ),
        )
        self.assertEqual(result["stage"], HYPOTHESIS_REVIEW)


if __name__ == "__main__":
    unittest.main()

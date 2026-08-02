import json
import unittest

from ccr_trajectory import (
    ATIFTrajectoryLoader,
    AssessmentCompletionEvaluator,
    EmptySearchEvaluator,
    REVIEW1,
    REVIEW2,
    ToolFailureEvaluator,
    UnitCompletionEvaluator,
    repeated_file_reads,
    review_stage,
)
from trajectory_harness import RepeatedToolCallEvaluator, evaluate
from trajectory_judge import objective_signals


class CCRTrajectoryTest(unittest.TestCase):
    def setUp(self):
        root = {
            "session_id": "s1",
            "extra": {"repo": "/repo", "branch": "feature"},
            "subagent_trajectories": [
                {
                    "trajectory_id": "unit-1",
                    "extra": {"file_path": "a.go", "scope_kind": "unit"},
                    "steps": [
                        {"step_id": 1, "source": "system", "message": "review"},
                        {
                            "step_id": 2,
                            "source": "agent",
                            "timestamp": "2026-08-02T10:00:00Z",
                            "metrics": {"extra": {"duration_ms": 1200}},
                            "tool_calls": [
                                {
                                    "tool_call_id": "c1",
                                    "function_name": "file_read",
                                    "arguments": {"file_path": "a.go"},
                                },
                                {
                                    "tool_call_id": "c2",
                                    "function_name": "file_read",
                                    "arguments": {"file_path": "a.go"},
                                },
                                {
                                    "tool_call_id": "c3",
                                    "function_name": "code_search",
                                    "arguments": {"search_text": "Thing"},
                                },
                            ],
                            "observation": {
                                "results": [
                                    {"source_call_id": "c1", "content": "source" * 8},
                                    {"source_call_id": "c2", "content": "source" * 8},
                                    {
                                        "source_call_id": "c3",
                                        "content": "",
                                        "extra": {"ok": False},
                                    },
                                ]
                            },
                        },
                    ],
                }
            ],
        }
        self.trajectory = ATIFTrajectoryLoader().loads(json.dumps(root))[0]

    def test_projects_atif_and_runs_harness_evaluators(self):
        report = evaluate(
            self.trajectory,
            [
                RepeatedToolCallEvaluator(),
                ToolFailureEvaluator(),
                EmptySearchEvaluator(),
                UnitCompletionEvaluator(),
            ],
        )

        self.assertEqual(self.trajectory.metadata["session_id"], "s1")
        self.assertEqual(self.trajectory.metadata["file_path"], "a.go")
        self.assertEqual(review_stage(self.trajectory), REVIEW1)
        self.assertEqual([step.operation for step in self.trajectory.steps].count("execute_tool"), 3)
        self.assertEqual(report.evaluations[0].label, "fail")
        self.assertEqual(report.evaluations[1].score, 0.667)
        self.assertEqual(report.evaluations[2].score, 0)
        self.assertEqual(report.evaluations[3].score, 0)
        self.assertEqual(repeated_file_reads(self.trajectory), {"a.go": 2})
        self.assertEqual(
            [item["name"] for item in objective_signals(self.trajectory)["evaluations"]],
            ["repeated_tool_call", "tool_success", "non_empty_search", "unit_completion"],
        )

    def test_review2_uses_assessment_completion_instead_of_task_done(self):
        root = {
            "session_id": "s2",
            "subagent_trajectories": [
                {
                    "trajectory_id": "hypothesis_review:lane-1",
                    "extra": {"scope_kind": "lane"},
                    "steps": [
                        {
                            "step_id": 1,
                            "source": "agent",
                            "tool_calls": [
                                {
                                    "tool_call_id": "a1",
                                    "function_name": "submit_assessments",
                                    "arguments": {"assessments": []},
                                }
                            ],
                            "observation": {
                                "results": [
                                    {
                                        "source_call_id": "a1",
                                        "content": json.dumps(
                                            {"accepted": ["h-1"], "remaining": []}
                                        ),
                                    }
                                ]
                            },
                        }
                    ],
                }
            ],
        }
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(root))[0]

        self.assertEqual(review_stage(trajectory), REVIEW2)
        self.assertEqual(AssessmentCompletionEvaluator().evaluate(trajectory).score, 1)
        self.assertEqual(UnitCompletionEvaluator().evaluate(trajectory).score, 0)
        self.assertEqual(
            [item["name"] for item in objective_signals(trajectory)["evaluations"]],
            [
                "repeated_tool_call",
                "tool_success",
                "non_empty_search",
                "assessment_submission",
            ],
        )


if __name__ == "__main__":
    unittest.main()

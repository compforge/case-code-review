import json
import unittest

from ccr_trajectory import (
    ATIFTrajectoryLoader,
    EmptySearchEvaluator,
    ToolFailureEvaluator,
    UnitCompletionEvaluator,
    repeated_file_reads,
)
from trajectory_harness import RepeatedToolCallEvaluator, evaluate


class CCRTrajectoryTest(unittest.TestCase):
    def setUp(self):
        root = {
            "session_id": "s1",
            "extra": {"repo": "/repo", "branch": "feature"},
            "subagent_trajectories": [
                {
                    "trajectory_id": "unit-1",
                    "extra": {"file_path": "a.go"},
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
        self.assertEqual([step.operation for step in self.trajectory.steps].count("execute_tool"), 3)
        self.assertEqual(report.evaluations[0].label, "fail")
        self.assertEqual(report.evaluations[1].score, 0.667)
        self.assertEqual(report.evaluations[2].score, 0)
        self.assertEqual(report.evaluations[3].score, 0)
        self.assertEqual(repeated_file_reads(self.trajectory), {"a.go": 2})


if __name__ == "__main__":
    unittest.main()

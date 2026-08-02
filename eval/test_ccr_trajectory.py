import json
import unittest

from ccr_trajectory import (
    ATIFTrajectoryLoader,
    AssessmentCompletionEvaluator,
    DurationEfficiencyEvaluator,
    EmptySearchEvaluator,
    FileReadCoverageEvaluator,
    FileReadFragmentationEvaluator,
    HypothesisCompletionEvaluator,
    PromptFileCoverageEvaluator,
    REVIEW1,
    REVIEW2,
    RoundEfficiencyEvaluator,
    ToolFailureEvaluator,
    code_search_stats,
    file_read_fragmentation,
    file_read_stats,
    prompt_file_read_overlap,
    repeated_file_reads,
    review_stage,
)
from trajectory_harness import RepeatedToolCallEvaluator, evaluate
from trajectory_judge import main_deductions, objective_signals


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
                            "source": "user",
                            "message": "File: a.go (Total lines: 100)\nLINE_RANGE: 1-80\n1|source\n",
                        },
                        {
                            "step_id": 3,
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
                                {
                                    "tool_call_id": "c4",
                                    "function_name": "submit_hypotheses",
                                    "arguments": {"hypotheses": []},
                                },
                            ],
                            "observation": {
                                "results": [
                                    {
                                        "source_call_id": "c1",
                                        "content": "File: a.go (Total lines: 100)\nLINE_RANGE: 1-100\n",
                                    },
                                    {
                                        "source_call_id": "c2",
                                        "content": "File: a.go (Total lines: 100)\nLINE_RANGE: 1-100\n",
                                    },
                                    {
                                        "source_call_id": "c3",
                                        "content": "",
                                        "extra": {"ok": False},
                                    },
                                    {
                                        "source_call_id": "c4",
                                        "content": "Unit review completed; hypotheses submitted for independent review.",
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
                FileReadCoverageEvaluator(),
                PromptFileCoverageEvaluator(),
                FileReadFragmentationEvaluator(),
                RoundEfficiencyEvaluator(),
                DurationEfficiencyEvaluator(),
                HypothesisCompletionEvaluator(),
            ],
        )

        self.assertEqual(self.trajectory.metadata["session_id"], "s1")
        self.assertEqual(self.trajectory.metadata["file_path"], "a.go")
        self.assertEqual(review_stage(self.trajectory), REVIEW1)
        self.assertEqual([step.operation for step in self.trajectory.steps].count("execute_tool"), 4)
        self.assertEqual(report.evaluations[0].label, "fail")
        self.assertEqual(report.evaluations[1].score, 0.75)
        self.assertEqual(report.evaluations[2].score, 0)
        self.assertEqual(report.evaluations[3].score, 0.5)
        self.assertEqual(report.evaluations[4].score, 0.2)
        self.assertEqual(report.evaluations[5].score, 0.5)
        self.assertEqual(report.evaluations[6].score, 1)
        self.assertEqual(report.evaluations[7].score, 1)
        self.assertEqual(report.evaluations[8].score, 1)
        self.assertEqual(repeated_file_reads(self.trajectory), {"a.go": 2})
        self.assertEqual(
            file_read_stats(self.trajectory),
            {
                "calls": 2,
                "requests": 2,
                "rounds": 1,
                "average_batch": 1.0,
                "max_batch": 1,
                "calls_per_round": 2.0,
            },
        )
        self.assertEqual(
            prompt_file_read_overlap(self.trajectory),
            {
                "calls": 2,
                "fully_covered": 0,
                "partially_covered": 2,
                "new_context": 0,
                "runtime_covered": 0,
                "blocked": 0,
                "failed": 0,
                "unmeasured": 0,
                "covered_lines": 160,
                "total_lines": 200,
                "overlap_rate": 0.8,
                "overlapping_steps": ["3:tool:1", "3:tool:2"],
            },
        )
        self.assertEqual(
            [item["name"] for item in objective_signals(self.trajectory)["evaluations"]],
            [
                "repeated_tool_call",
                "tool_success",
                "non_empty_search",
                "file_read_coverage",
                "file_read_prompt_novelty",
                "file_read_fragmentation",
                "round_efficiency",
                "duration_efficiency",
                "hypothesis_submission",
            ],
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
        self.assertEqual(HypothesisCompletionEvaluator().evaluate(trajectory).score, 0)
        self.assertEqual(
            [item["name"] for item in objective_signals(trajectory)["evaluations"]],
            [
                "repeated_tool_call",
                "tool_success",
                "non_empty_search",
                "file_read_coverage",
                "file_read_prompt_novelty",
                "file_read_fragmentation",
                "round_efficiency",
                "duration_efficiency",
                "assessment_submission",
            ],
        )

    def test_lane_efficiency_is_normalized_by_accepted_assessments(self):
        steps = []
        for index in range(30):
            raw = {
                "step_id": index + 1,
                "source": "agent",
                "metrics": {"extra": {"duration_ms": 10_000}},
            }
            if index in (10, 29):
                call_id = f"assessment-{index}"
                raw["tool_calls"] = [
                    {
                        "tool_call_id": call_id,
                        "function_name": "submit_assessments",
                        "arguments": {"assessments": []},
                    }
                ]
                raw["observation"] = {
                    "results": [
                        {
                            "source_call_id": call_id,
                            "content": json.dumps(
                                {"accepted": [f"h-{index}"], "remaining": []}
                            ),
                        }
                    ]
                }
            steps.append(raw)
        root = {
            "session_id": "lane-efficiency",
            "subagent_trajectories": [
                {
                    "trajectory_id": "lane-1",
                    "extra": {"scope_kind": "lane"},
                    "steps": steps,
                }
            ],
        }
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(root))[0]

        rounds = RoundEfficiencyEvaluator().evaluate(trajectory)
        duration = DurationEfficiencyEvaluator().evaluate(trajectory)

        self.assertEqual(rounds.score, 0.8)
        self.assertIn("2 review item(s)", rounds.explanation)
        self.assertEqual(duration.score, 0.8)

    def test_main_deductions_rank_total_score_loss(self):
        deductions = main_deductions(
            [
                {
                    "evaluations": [
                        {"name": "duration", "label": "fail", "score": 0.4},
                        {"name": "search", "label": "fail", "score": 0.7},
                    ]
                },
                {
                    "evaluations": [
                        {"name": "duration", "label": "fail", "score": 0.5},
                        {"name": "completion", "label": "pass", "score": 1.0},
                    ]
                },
            ]
        )

        self.assertEqual(deductions[0]["name"], "duration")
        self.assertEqual(deductions[0]["count"], 2)
        self.assertEqual(deductions[0]["average_score"], 0.45)

    def test_file_read_coverage_scores_overlapping_ranges(self):
        root = {
            "session_id": "s3",
            "subagent_trajectories": [
                {
                    "trajectory_id": "unit-3",
                    "extra": {"scope_kind": "unit"},
                    "steps": [
                        {
                            "step_id": 1,
                            "source": "agent",
                            "tool_calls": [
                                {
                                    "tool_call_id": "r1",
                                    "function_name": "file_read",
                                    "arguments": {
                                        "file_path": "a.go",
                                        "start_line": 1,
                                        "end_line": 100,
                                    },
                                },
                                {
                                    "tool_call_id": "r2",
                                    "function_name": "file_read",
                                    "arguments": {
                                        "file_path": "a.go",
                                        "start_line": 50,
                                        "end_line": 150,
                                    },
                                },
                            ],
                            "observation": {
                                "results": [
                                    {
                                        "source_call_id": "r1",
                                        "content": "File: a.go (Total lines: 200)\nLINE_RANGE: 1-100\n",
                                    },
                                    {
                                        "source_call_id": "r2",
                                        "content": "File: a.go (Total lines: 200)\nLINE_RANGE: 50-150\n",
                                    },
                                ]
                            },
                        }
                    ],
                }
            ],
        }
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(root))[0]

        result = FileReadCoverageEvaluator().evaluate(trajectory)

        self.assertEqual(result.score, 0.746)
        self.assertEqual(result.step_ids, ("1:tool:2",))
        self.assertEqual(
            file_read_fragmentation(trajectory),
            {
                "calls": 2,
                "minimal_ranges": 1,
                "mergeable_reads": 1,
                "fragmented_steps": ["1:tool:2"],
            },
        )

    def test_prompt_overlap_classifies_context_short_circuits(self):
        root = {
            "session_id": "s4",
            "subagent_trajectories": [
                {
                    "trajectory_id": "unit-4",
                    "extra": {"scope_kind": "unit"},
                    "steps": [
                        {
                            "step_id": 1,
                            "source": "agent",
                            "tool_calls": [
                                {
                                    "tool_call_id": "initial",
                                    "function_name": "file_read",
                                    "arguments": {"file_path": "a.go"},
                                },
                                {
                                    "tool_call_id": "runtime",
                                    "function_name": "file_read",
                                    "arguments": {"file_path": "b.go"},
                                },
                            ],
                            "observation": {
                                "results": [
                                    {
                                        "source_call_id": "initial",
                                        "content": "Already available in the current context from the initial source context: a.go lines 10-20. Reuse that content; call file_read only for a range not shown there.",
                                    },
                                    {
                                        "source_call_id": "runtime",
                                        "content": "Already available in the current context from an earlier file_read result: b.go lines 30-40. Reuse that content; call file_read only for a range not shown there.",
                                    },
                                ]
                            },
                        }
                    ],
                }
            ],
        }
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(root))[0]

        overlap = prompt_file_read_overlap(trajectory)

        self.assertEqual(overlap["fully_covered"], 1)
        self.assertEqual(overlap["runtime_covered"], 1)
        self.assertEqual(overlap["covered_lines"], 11)
        self.assertEqual(overlap["total_lines"], 11)

    def test_batch_file_read_counts_requests_and_preserves_item_ranges(self):
        result = (
            "===== FILE_READ RESULT 1/2 =====\n"
            "File: a.go (Total lines: 20)\nLINE_RANGE: 1-10\n1|a\n"
            "===== FILE_READ RESULT 2/2 =====\n"
            "File: b.go (Total lines: 30)\nLINE_RANGE: 11-20\n11|b\n"
        )
        root = {
            "session_id": "batch",
            "subagent_trajectories": [
                {
                    "trajectory_id": "unit-batch",
                    "extra": {"scope_kind": "unit"},
                    "steps": [
                        {
                            "step_id": 1,
                            "source": "agent",
                            "tool_calls": [
                                {
                                    "tool_call_id": "batch-1",
                                    "function_name": "file_read",
                                    "arguments": {
                                        "reads": [
                                            {"file_path": "a.go", "start_line": 1, "end_line": 10},
                                            {"file_path": "b.go", "start_line": 11, "end_line": 20},
                                        ]
                                    },
                                }
                            ],
                            "observation": {
                                "results": [
                                    {"source_call_id": "batch-1", "content": result}
                                ]
                            },
                        }
                    ],
                }
            ],
        }
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(root))[0]

        self.assertEqual(repeated_file_reads(trajectory), {})
        self.assertEqual(
            file_read_stats(trajectory),
            {
                "calls": 1,
                "requests": 2,
                "rounds": 1,
                "average_batch": 2.0,
                "max_batch": 2,
                "calls_per_round": 1.0,
            },
        )
        self.assertEqual(file_read_fragmentation(trajectory)["calls"], 2)
        self.assertEqual(prompt_file_read_overlap(trajectory)["new_context"], 2)

    def test_batch_code_search_counts_queries_and_empty_members(self):
        result = (
            "===== CODE_SEARCH RESULT 1/2 =====\n"
            "File: a.go\nMatch lines: 1\n10|func Alpha()\n"
            "===== CODE_SEARCH RESULT 2/2 =====\n"
            "No matches found\n"
        )
        root = {
            "session_id": "search-batch",
            "subagent_trajectories": [
                {
                    "trajectory_id": "unit-search-batch",
                    "extra": {"scope_kind": "unit"},
                    "steps": [
                        {
                            "step_id": 1,
                            "source": "agent",
                            "tool_calls": [
                                {
                                    "tool_call_id": "search-1",
                                    "function_name": "code_search",
                                    "arguments": {
                                        "searches": [
                                            {"search_text": "Alpha"},
                                            {"search_text": "Missing"},
                                        ]
                                    },
                                }
                            ],
                            "observation": {
                                "results": [
                                    {"source_call_id": "search-1", "content": result}
                                ]
                            },
                        }
                    ],
                }
            ],
        }
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(root))[0]

        self.assertEqual(
            code_search_stats(trajectory),
            {
                "calls": 1,
                "requests": 2,
                "rounds": 1,
                "average_batch": 2.0,
                "max_batch": 2,
                "calls_per_round": 1.0,
            },
        )
        evaluation = EmptySearchEvaluator().evaluate(trajectory)
        self.assertEqual(evaluation.score, 0.5)
        self.assertEqual(evaluation.step_ids, ("1:tool:1:2",))


if __name__ == "__main__":
    unittest.main()

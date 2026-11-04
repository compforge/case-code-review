"""Cross-language contract: recorded R1/R2 Sessions -> Go exporter -> ATIF -> views."""

from __future__ import annotations

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from atif import Trajectory
from agent_ledger.observation import ModelCall, ToolCall
from trajectory_harness import TrajectoryDataset
from trajectory_harness.session_view import session_views

from eval.trajectory.atif import (
    ATIFTrajectoryLoader,
    CCRToolUsageMeasurer,
    content_text,
    tool_observations,
)
from eval.trajectory.ccr_trajectory import review_stage, ToolFailureVerifier
from eval.trajectory.trajectory_judge import objective_analysis


class ATIFContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.repo = Path(__file__).resolve().parents[2]
        result = subprocess.run(
            ["go", "run", "./cmd/ccr", "export", "testdata/trajectory/review.jsonl"],
            cwd=cls.repo,
            capture_output=True,
            text=True,
            check=True,
            timeout=120,
        )
        cls.documents = [json.loads(line) for line in result.stdout.splitlines()]
        cls.trajectories = ATIFTrajectoryLoader().loads(
            result.stdout, source="fixture://r1-r2"
        )

    # +case=One R1 Session and a two-execution R2 Lane produce exactly two canonical trajectories
    def test_session_grain_and_native_calls(self):
        self.assertEqual(len(self.documents), 2)
        by_id = {item.trajectory_id: item for item in self.trajectories}
        self.assertEqual(set(by_id), {"session-1", "r1-session"})
        lane, unit = by_id["session-1"], by_id["r1-session"]
        self.assertEqual(
            (review_stage(unit), review_stage(lane)), ("review1", "review2")
        )
        self.assertEqual(lane.session_id, "session-1")
        self.assertEqual(len(lane.extra["executions"]), 2)
        self.assertEqual(
            [item["execution_id"] for item in lane.extra["executions"]],
            ["execution-1", "execution-2"],
        )
        self.assertEqual(
            [
                content_text(step.message)
                for step in lane.steps
                if step.source == "user"
            ],
            ["inspect caller", "review next hypothesis"],
        )
        self.assertEqual(sum(step.llm_call_count or 0 for step in lane.steps), 3)
        self.assertEqual(len(tool_observations(lane)), 2)
        self.assertEqual(
            [call.text for call in tool_observations(lane)],
            ["evidence 1", "evidence 2"],
        )
        for raw, trajectory in zip(self.documents, self.trajectories):
            self.assertNotIn("subagent_trajectories", raw)
            self.assertEqual(raw["session_id"], raw["trajectory_id"])
            self.assertEqual(
                [step.step_id for step in trajectory.steps],
                list(range(1, len(trajectory.steps) + 1)),
            )
            validated = Trajectory.model_validate(raw)
            for before, after in zip(validated.steps, trajectory.steps):
                self.assertEqual(
                    before.model_dump(exclude={"extra"}),
                    after.model_dump(exclude={"extra"}),
                )

    def test_partial_usage_and_media_survive_roundtrip(self):
        unit = next(
            item for item in self.trajectories if item.session_id == "r1-session"
        )
        self.assertEqual(unit.final_metrics.total_prompt_tokens, 107)
        self.assertEqual(unit.final_metrics.total_completion_tokens, 12)
        self.assertEqual(unit.final_metrics.total_cached_tokens, 32)
        self.assertEqual(
            unit.steps[0].message[1].source.path, "https://example.test/diagram.png"
        )
        partial = next(step for step in unit.steps if (step.extra or {}).get("partial"))
        self.assertEqual(content_text(partial.message), "partial answer")
        self.assertEqual(partial.reasoning_content, "partial reasoning")
        self.assertEqual(partial.metrics.prompt_tokens, 7)
        dataset = TrajectoryDataset(
            dataset_id="r1-r2", version="test", trajectories=tuple(self.trajectories)
        )
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "dataset.json"
            path.write_text(json.dumps(dataset.to_dict()))
            restored = TrajectoryDataset.from_dict(json.loads(path.read_text()))
        self.assertEqual(restored.to_dict(), dataset.to_dict())
        views = session_views(restored.trajectories)
        self.assertEqual(
            {view.session.id for view in views}, {"session-1", "r1-session"}
        )
        self.assertEqual(
            sum(isinstance(node, ModelCall) for view in views for node in view.nodes), 5
        )
        self.assertEqual(
            sum(isinstance(node, ToolCall) for view in views for node in view.nodes), 3
        )
        for view in views:
            for node in view.nodes:
                if isinstance(node, ModelCall):
                    self.assertIsNotNone(node.request)
                self.assertTrue(node.evidence)
        for trajectory in restored.trajectories:
            result = objective_analysis(trajectory)
            self.assertTrue(
                all(item["status"] != "error" for item in result["measurements"])
            )

    def test_media_and_tools_are_standard_fields(self):
        raw = {
            "schema_version": "ATIF-v1.8",
            "session_id": "audio",
            "trajectory_id": "audio",
            "agent": {"name": "ccr", "version": "test"},
            "steps": [
                {
                    "step_id": 1,
                    "source": "agent",
                    "llm_call_count": 1,
                    "message": [
                        {
                            "type": "audio",
                            "source": {
                                "media_type": "audio/wav",
                                "path": "answer.wav",
                                "duration_sec": 1.5,
                            },
                        }
                    ],
                    "tool_calls": [
                        {
                            "tool_call_id": "one",
                            "function_name": "read",
                            "arguments": {},
                        },
                        {
                            "tool_call_id": "two",
                            "function_name": "read",
                            "arguments": {},
                        },
                    ],
                    "observation": {
                        "results": [
                            {
                                "source_call_id": "two",
                                "content": "failure",
                                "extra": {"ok": False},
                            },
                            {
                                "source_call_id": "one",
                                "content": [
                                    {
                                        "type": "image",
                                        "source": {
                                            "media_type": "image/png",
                                            "path": "result.png",
                                        },
                                    }
                                ],
                                "extra": {"ok": True},
                            },
                        ]
                    },
                }
            ],
        }
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(raw))[0]
        self.assertEqual(trajectory.steps[0].message[0].source.duration_sec, 1.5)
        self.assertEqual(
            [item.call.tool_call_id for item in tool_observations(trajectory)],
            ["one", "two"],
        )
        self.assertEqual(
            CCRToolUsageMeasurer().measure(trajectory).measurements["tool_call_count"],
            2,
        )
        self.assertEqual(
            CCRToolUsageMeasurer()
            .measure(trajectory)
            .measurements["failed_tool_call_count"],
            1,
        )
        for result in trajectory.steps[0].observation.results:
            result.extra = {}
        self.assertEqual(
            ToolFailureVerifier().verify(trajectory).status, "not_applicable"
        )
        raw["schema_version"] = "ATIF-v1.7"
        with self.assertRaises(ValueError):
            ATIFTrajectoryLoader().loads(json.dumps(raw))
        with self.assertRaises(ValueError):
            ATIFTrajectoryLoader().loads('{"subagent_trajectories":[]}')


if __name__ == "__main__":
    unittest.main()

"""Boundary regressions for timing gaps and content-only tool measurements."""

import json
import unittest

from trajectory_harness.model import trajectory_execution, trajectory_metadata
from eval.trajectory.atif import ATIFTrajectoryLoader, CCRToolUsageMeasurer


def load(extra, steps):
    return ATIFTrajectoryLoader().loads(
        json.dumps(
            {
                "schema_version": "ATIF-v1.8",
                "trajectory_id": "session",
                "agent": {"name": "ccr", "version": "test"},
                "steps": steps,
                "extra": extra,
            }
        )
    )[0]


def model_step(number, duration, execution_id=None):
    return {
        "step_id": number,
        "source": "agent",
        "message": "answer",
        "extra": {"duration_ms": duration, "execution_id": execution_id},
    }


class ExecutionDurationTest(unittest.TestCase):
    # +case=Missing duration retains observed timing; authoritative zero stays zero and completed executions are not counted twice
    def test_execution_timing(self):
        cases = [
            (
                {"executions": [{"execution_id": "e1"}]},
                [model_step(1, 5000, "e1")],
                5000,
                True,
            ),
            (
                {
                    "executions": [
                        {
                            "execution_id": "e1",
                            "duration_ms": 2000,
                            "outcome": "completed",
                        },
                        {"execution_id": "e2"},
                    ]
                },
                [model_step(1, 1000, "e1"), model_step(2, 5000, "e2")],
                7000,
                True,
            ),
            (
                {
                    "executions": [
                        {"execution_id": "e1", "duration_ms": 0, "outcome": "completed"}
                    ]
                },
                [model_step(1, 5000, "e1")],
                0,
                False,
            ),
            (
                {"execution_outcome": "completed", "execution_duration_ms": 0},
                [model_step(1, 5000)],
                0,
                False,
            ),
            ({"executions": [{}]}, [model_step(1, 5000)], 5000, True),
            (
                {"executions": [{"duration_ms": 2000}, {}]},
                [model_step(1, 5000)],
                5000,
                True,
            ),
        ]
        for metadata, steps, duration, incomplete in cases:
            with self.subTest(metadata=metadata):
                trajectory = load(metadata, steps)
                for item in (
                    trajectory,
                    ATIFTrajectoryLoader().loads(trajectory.model_dump_json())[0],
                ):
                    self.assertEqual(trajectory_execution(item).duration_ms, duration)
                    self.assertEqual(
                        bool(
                            trajectory_metadata(item).get(
                                "execution_duration_incomplete"
                            )
                        ),
                        incomplete,
                    )
        self.assertEqual(
            trajectory_execution(
                load({"executions": [{}]}, [model_step(1, 5000)])
            ).outcome,
            "unknown",
        )


class ResultBytesTest(unittest.TestCase):
    def measure(self, content, metadata_size):
        call_id = "call" + "x" * metadata_size
        step = {
            "step_id": 1,
            "source": "agent",
            "message": "inspect",
            "tool_calls": [
                {
                    "tool_call_id": call_id,
                    "function_name": "inspect",
                    "arguments": {"input": "x" * metadata_size},
                }
            ],
            "observation": {
                "results": [
                    {
                        "source_call_id": call_id,
                        "content": content,
                        "extra": {
                            "ok": True,
                            "arguments": "x" * metadata_size,
                            "provenance": "x" * metadata_size,
                        },
                    }
                ]
            },
        }
        return (
            CCRToolUsageMeasurer()
            .measure(load({}, [step]))
            .measurements["result_bytes"]
        )

    # +case=Tool result bytes depend only on recorded content, independent of call IDs, arguments and metadata
    def test_text_and_empty_content(self):
        for content, expected in (("ok", 2), ("你好", 6), ("", 0), (None, 0)):
            for size in (0, 10000):
                with self.subTest(content=content, size=size):
                    self.assertEqual(self.measure(content, size), expected)

    def test_multimodal_content(self):
        content = [
            {"type": "text", "text": "你好"},
            {
                "type": "image",
                "source": {
                    "path": "https://example.test/image.png",
                    "media_type": "image/png",
                },
            },
        ]
        expected = len(
            json.dumps(content, ensure_ascii=False, separators=(",", ":")).encode(
                "utf-8"
            )
        )
        self.assertEqual(self.measure(content, 0), expected)
        self.assertEqual(self.measure(content, 10000), expected)

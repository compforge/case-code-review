from __future__ import annotations

import json
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch

from ccr_trajectory_report import CCRTrajectoryAnalysisRunner
from trajectory_diagnostics import (
    MAX_DIAGNOSTIC_DIGEST_BYTES,
    plan_diagnostics,
    run_diagnostics,
)
from trajectory_harness import (
    DatasetBuildResult,
    DatasetBuildSummary,
    ExecutionResult,
    Failure,
    Step,
    TrajectoryDataset,
    TrajectoryRunArtifact,
)
from trajectory_harness.model import make_atif_step, make_atif_trajectory
from trajectory_judge import JudgeResult, judge_chain_with_usage


class TrajectoryDiagnosticsTest(unittest.TestCase):
    def test_judge_chain_reports_provider_usage(self) -> None:
        response = _HTTPResponse(
            {
                "choices": [
                    {
                        "message": {
                            "content": '{"categories": [], "summary": "ok"}'
                        }
                    }
                ],
                "usage": {
                    "prompt_tokens": 80,
                    "completion_tokens": 10,
                    "total_tokens": 90,
                    "prompt_tokens_details": {"cached_tokens": 20},
                },
            }
        )
        with patch("trajectory_judge.urllib.request.urlopen", return_value=response):
            result = judge_chain_with_usage(
                "https://example.test/v1/chat/completions",
                "secret",
                "judge-model",
                "digest",
            )

        self.assertEqual(result.diagnostic["summary"], "ok")
        self.assertEqual(
            result.usage,
            {
                "input_tokens": 80,
                "cached_input_tokens": 20,
                "output_tokens": 10,
                "total_tokens": 90,
            },
        )

    def test_prioritizes_failures_and_reuses_cached_facets(self) -> None:
        artifact = _artifact()
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            with (
                patch(
                    "trajectory_judge.detect",
                    side_effect=AssertionError("detectors must not run again"),
                ),
                patch(
                    "trajectory_judge.verify",
                    side_effect=AssertionError("verifiers must not run again"),
                ),
                patch(
                    "trajectory_judge.measure",
                    side_effect=AssertionError("measurers must not run again"),
                ),
            ):
                plan = plan_diagnostics(
                    artifact,
                    cache_dir=root / "cache",
                    model="judge-model",
                    uncached_limit=1,
                )

            self.assertEqual(
                [item.trajectory.trajectory_id for item in plan.candidates],
                ["timeout", "tool-error"],
            )
            self.assertEqual(len(plan.jobs), 1)
            self.assertEqual(plan.jobs[0].candidate.trajectory.trajectory_id, "timeout")
            self.assertEqual(plan.skipped_uncached, 1)
            self.assertLessEqual(
                plan.jobs[0].prompt.digest_bytes,
                MAX_DIAGNOSTIC_DIGEST_BYTES,
            )
            self.assertGreater(plan.jobs[0].prompt.truncated_bytes, 0)
            self.assertLess(
                plan.jobs[0].prompt.expanded_steps,
                plan.jobs[0].prompt.total_steps,
            )
            self.assertIn("step outline:", plan.jobs[0].prompt.digest)
            self.assertIn("middle evidence omitted", plan.jobs[0].prompt.digest)
            self.assertEqual(plan.summary()["input_estimate"]["calls"], 1)

            calls = []

            def diagnose(digest: str) -> JudgeResult:
                calls.append(digest)
                return JudgeResult(
                    diagnostic={
                        "categories": [
                            {
                                "type": "model_limitation",
                                "evidence": "routing timeout",
                                "suggestion": "compare a stable endpoint",
                                "confidence": 0.9,
                            }
                        ],
                        "summary": "The request exhausted its routing budget.",
                    },
                    usage={
                        "input_tokens": 100,
                        "cached_input_tokens": 25,
                        "output_tokens": 20,
                        "total_tokens": 120,
                    },
                )

            manifest = run_diagnostics(
                plan,
                output_dir=root / "out",
                source_run_dir=root / "run",
                diagnose=diagnose,
            )

            self.assertEqual(len(calls), 1)
            self.assertIn("objective analysis:", calls[0])
            self.assertEqual(
                manifest["coverage"],
                {
                    "facets": 1,
                    "cache_hits": 0,
                    "generated": 1,
                    "errors": 0,
                    "skipped_uncached": 1,
                    "diagnostic_coverage": 0.5,
                },
            )
            self.assertEqual(
                manifest["usage"],
                {
                    "model_call_count": 1,
                    "usage_reported_call_count": 1,
                    "input_tokens": 100,
                    "cached_input_tokens": 25,
                    "output_tokens": 20,
                    "total_tokens": 120,
                    "usage_coverage_ratio": 1.0,
                    "uncached_input_tokens": 75,
                },
            )
            records = [
                json.loads(line)
                for line in (root / "out" / "diagnostic-facets.jsonl")
                .read_text(encoding="utf-8")
                .splitlines()
            ]
            self.assertEqual(records[0]["cache_status"], "generated")
            self.assertEqual(records[0]["trajectory_id"], "timeout")
            self.assertEqual(
                records[0]["prompt"]["digest_bytes"],
                MAX_DIAGNOSTIC_DIGEST_BYTES,
            )
            self.assertNotIn("verdict", records[0])

            cached_plan = plan_diagnostics(
                artifact,
                cache_dir=root / "cache",
                model="judge-model",
                uncached_limit=0,
            )
            self.assertEqual(cached_plan.cache_hits, 1)
            self.assertEqual(cached_plan.uncached_jobs, 0)
            self.assertEqual(cached_plan.skipped_uncached, 1)
            self.assertEqual(cached_plan.summary()["input_estimate"]["calls"], 0)
            self.assertEqual(
                cached_plan.summary()["input_estimate"]["input_tokens"], 0
            )
            cached_manifest = run_diagnostics(
                cached_plan,
                output_dir=root / "cached-out",
                diagnose=lambda _: self.fail("cached facet called the judge"),
            )
            self.assertEqual(cached_manifest["coverage"]["cache_hits"], 1)
            self.assertEqual(cached_manifest["coverage"]["generated"], 0)
            self.assertEqual(cached_manifest["usage"]["model_call_count"], 0)
            self.assertIsNone(cached_manifest["usage"]["usage_coverage_ratio"])

            other_model_plan = plan_diagnostics(
                artifact,
                cache_dir=root / "cache",
                model="other-model",
                uncached_limit=1,
            )
            self.assertEqual(other_model_plan.cache_hits, 0)
            run_diagnostics(
                other_model_plan,
                output_dir=root / "other-model-out",
                diagnose=diagnose,
            )

            original_model_plan = plan_diagnostics(
                artifact,
                cache_dir=root / "cache",
                model="judge-model",
                uncached_limit=0,
            )
            self.assertEqual(original_model_plan.cache_hits, 1)
            self.assertEqual(original_model_plan.uncached_jobs, 0)


def _artifact() -> TrajectoryRunArtifact:
    timeout_failure = Failure(
        kind="llm",
        phase="routing",
        error_type="timeout",
        code="routing_budget_exhausted",
    )
    tool_failure = Failure(
        kind="tool",
        phase="execute",
        error_type="invalid_arguments",
        code="invalid_arguments",
    )
    trajectories = (
        make_atif_trajectory(
            trajectory_id="timeout",
            recording_id="session-1",
            steps=(
                *_tool_steps("timeout", 6),
                *_context_steps("timeout", 60),
                _inference_step("timeout:model", 12_000),
                make_atif_step(
                    step_id="timeout:failure",
                    parent_step_id=None,
                    operation="inference",
                    name="model",
                    start_ms=1,
                    duration_ms=8_000,
                    status="error",
                    failure=timeout_failure,
                ),
            ),
            execution=ExecutionResult(
                outcome="timeout",
                duration_ms=20_000,
                failure=timeout_failure,
            ),
            generation={"agent_revision": "revision-1"},
            metadata={"scope_kind": "unit"},
        ),
        make_atif_trajectory(
            trajectory_id="tool-error",
            recording_id="session-2",
            steps=(
                _inference_step("tool:model", 5_000),
                make_atif_step(
                    step_id="tool:failure",
                    parent_step_id="tool:model",
                    operation="execute_tool",
                    name="read_files",
                    start_ms=1,
                    duration_ms=10,
                    status="error",
                    failure=tool_failure,
                ),
            ),
            execution=ExecutionResult(outcome="completed", duration_ms=5_010),
            generation={"agent_revision": "revision-1"},
            metadata={"scope_kind": "unit"},
        ),
        make_atif_trajectory(
            trajectory_id="clean",
            recording_id="session-3",
            steps=(_inference_step("clean:model", 1_000),),
            execution=ExecutionResult(outcome="completed", duration_ms=1_000),
            generation={"agent_revision": "revision-1"},
            metadata={"scope_kind": "unit"},
        ),
    )
    dataset = TrajectoryDataset(
        dataset_id="ccr-reviews",
        version="test",
        trajectories=trajectories,
    )
    run = CCRTrajectoryAnalysisRunner().run(
        dataset,
        run_id="run-1",
        created_at=datetime(2026, 9, 2, tzinfo=timezone.utc),
    )
    return TrajectoryRunArtifact(
        build=DatasetBuildResult(
            dataset=dataset,
            summary=DatasetBuildSummary(
                selected_recordings=3,
                fetched_recordings=3,
                loaded_trajectories=3,
                included_trajectories=3,
            ),
        ),
        run=run,
    )


def _inference_step(step_id: str, duration_ms: float) -> Step:
    return make_atif_step(
        step_id=step_id,
        parent_step_id=None,
        operation="inference",
        name="model",
        start_ms=0,
        duration_ms=duration_ms,
        attributes={"prompt_tokens": 100, "completion_tokens": 10},
    )


def _context_steps(prefix: str, count: int) -> tuple[Step, ...]:
    return tuple(
        make_atif_step(
            step_id=f"{prefix}:context:{index}",
            parent_step_id=None,
            operation="context",
            name="prompt",
            start_ms=0,
            duration_ms=0,
            output_messages=(
                {
                    "role": "user",
                    "parts": [{"type": "text", "content": "context " + "x" * 900}],
                },
            ),
        )
        for index in range(count)
    )


def _tool_steps(prefix: str, count: int) -> tuple[Step, ...]:
    return tuple(
        make_atif_step(
            step_id=f"{prefix}:tool:{index}",
            parent_step_id=None,
            operation="execute_tool",
            name=f"lookup_{index}",
            start_ms=0,
            duration_ms=1,
        )
        for index in range(count)
    )


class _HTTPResponse:
    def __init__(self, value: dict) -> None:
        self._value = value

    def __enter__(self) -> _HTTPResponse:
        return self

    def __exit__(self, *_: object) -> None:
        return None

    def read(self) -> bytes:
        return json.dumps(self._value).encode()


if __name__ == "__main__":
    unittest.main()

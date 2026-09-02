from __future__ import annotations

import json
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch

from ccr_trajectory_report import CCRTrajectoryEvaluationRunner
from trajectory_diagnostics import plan_diagnostics, run_diagnostics
from trajectory_harness import (
    DatasetBuildResult,
    DatasetBuildSummary,
    ExecutionResult,
    Failure,
    Step,
    Trajectory,
    TrajectoryDataset,
    TrajectoryRunArtifact,
)


class TrajectoryDiagnosticsTest(unittest.TestCase):
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
                    "trajectory_judge.evaluate",
                    side_effect=AssertionError("evaluators must not run again"),
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

            calls = []

            def diagnose(digest: str) -> dict:
                calls.append(digest)
                return {
                    "categories": [
                        {
                            "type": "model_limitation",
                            "evidence": "routing timeout",
                            "suggestion": "compare a stable endpoint",
                            "confidence": 0.9,
                        }
                    ],
                    "summary": "The request exhausted its routing budget.",
                }

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
            records = [
                json.loads(line)
                for line in (root / "out" / "diagnostic-facets.jsonl")
                .read_text(encoding="utf-8")
                .splitlines()
            ]
            self.assertEqual(records[0]["cache_status"], "generated")
            self.assertEqual(records[0]["trajectory_id"], "timeout")
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
            cached_manifest = run_diagnostics(
                cached_plan,
                output_dir=root / "cached-out",
                diagnose=lambda _: self.fail("cached facet called the judge"),
            )
            self.assertEqual(cached_manifest["coverage"]["cache_hits"], 1)
            self.assertEqual(cached_manifest["coverage"]["generated"], 0)

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
        Trajectory(
            trajectory_id="timeout",
            recording_id="session-1",
            steps=(
                _inference_step("timeout:model", 12_000),
                Step(
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
        Trajectory(
            trajectory_id="tool-error",
            recording_id="session-2",
            steps=(
                _inference_step("tool:model", 5_000),
                Step(
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
        Trajectory(
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
    run = CCRTrajectoryEvaluationRunner().run(
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
    return Step(
        step_id=step_id,
        parent_step_id=None,
        operation="inference",
        name="model",
        start_ms=0,
        duration_ms=duration_ms,
        attributes={"prompt_tokens": 100, "completion_tokens": 10},
    )


if __name__ == "__main__":
    unittest.main()

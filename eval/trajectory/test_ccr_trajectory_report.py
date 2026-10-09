from __future__ import annotations

from trajectory_harness.model import trajectory_recording_id

import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from zoneinfo import ZoneInfo

from eval.trajectory.ccr_source import CCRSessionSource
from eval.trajectory.ccr_trajectory_report import CCRTrajectoryReportBuilder, run_weekly_report
from trajectory_harness import load_run_artifact
from eval.weekly_report import SessionRecord, load_trajectory_rows
from eval.weekly_window import WeekWindow


class CCRTrajectoryReportTest(unittest.TestCase):
    def test_runs_the_canonical_dataset_to_html_pipeline(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            session_dir = root / "sessions" / "repo"
            session_dir.mkdir(parents=True)
            session_path = session_dir / "session-1.jsonl"
            session_path.write_text(
                json.dumps(
                    {
                        "type": "session_start",
                        "sessionId": "session-1",
                        "timestamp": "2026-08-20T10:00:00Z",
                        "cwd": str(root / "repo"),
                    }
                )
                + "\n"
                + json.dumps({"type": "session_end"})
                + "\n",
                encoding="utf-8",
            )
            labels = root / "eval.benchmark.labels.jsonl"
            labels.write_text(
                json.dumps(
                    {
                        "id": "sample-1",
                        "kind": "finding",
                        "label": "wrong",
                        "engine": {
                            "session_id": "session-1",
                            "hypothesis": {"origin_unit": "unit-1"},
                        },
                    }
                )
                + "\n"
                + json.dumps(
                    {
                        "id": "sample-outside-window",
                        "label": "important",
                        "engine": {"session_id": "another-session"},
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            atif = json.dumps(
                {
                    "session_id": "session-1",
                    "subagent_trajectories": [
                        {
                            "trajectory_id": "unit-1",
                            "extra": {"scope_kind": "unit"},
                            "steps": [
                                {
                                    "step_id": str(index),
                                    "source": "agent",
                                    "tool_calls": [
                                        {
                                            "tool_call_id": f"call-{index}",
                                            "function_name": "custom_tool",
                                            "arguments": {"path": "a.go"},
                                        }
                                    ],
                                    "observation": {
                                        "results": [
                                            {
                                                "source_call_id": f"call-{index}",
                                                "content": "ok",
                                                "extra": {"ok": True},
                                            }
                                        ]
                                    },
                                }
                                for index in (1, 2)
                            ],
                        }
                    ],
                }
            )
            source = CCRSessionSource(root / "sessions", exporter=lambda _: atif)
            window = WeekWindow.from_key("2026-W34", ZoneInfo("UTC"))

            result = run_weekly_report(
                window=window,
                label_paths=[labels],
                source=source,
                runs_dir=root / "runs",
            )

            self.assertTrue(result.dataset_path.is_file())
            self.assertTrue(result.run_path.is_file())
            self.assertTrue(result.report_path.is_file())
            self.assertTrue(result.verdict_path.is_file())
            persisted = load_run_artifact(result.run_dir)
            self.assertEqual(
                persisted.dataset.to_dict(), result.artifact.dataset.to_dict()
            )
            run_json = json.loads(result.run_path.read_text())
            self.assertIn("verifications", run_json)
            self.assertNotIn("evaluations", run_json)
            self.assertTrue(persisted.run.verifier_specs)
            for cell in (*persisted.run.detections, *persisted.run.verifications,
                         *persisted.run.measurements):
                self.assertTrue(all(item.status != "error" for item in cell.results))
            self.assertEqual(result.artifact.dataset.version, "2026-W34")
            self.assertEqual(result.artifact.dataset.metadata["labels"], 1)
            self.assertEqual(
                result.artifact.dataset.metadata["missing_label_sessions"], 0
            )
            self.assertEqual(
                trajectory_recording_id(result.artifact.dataset.trajectories[0]),
                "session-1",
            )
            self.assertEqual(
                result.artifact.run.target_for("session-1/unit-1"),
                "review1",
            )
            self.assertEqual(
                result.artifact.run.detections[0].category,
                "cost",
            )
            self.assertEqual(
                result.artifact.run.verifications[0].target,
                "review1",
            )
            self.assertEqual(
                {item.category for item in result.artifact.run.verifications},
                {"cost", "effect"},
            )
            self.assertEqual(
                result.artifact.run.measurements[0].category,
                "cost",
            )
            self.assertEqual(
                {spec.measurer_id for spec in result.artifact.run.measurer_specs},
                {"model_usage", "tool_usage", "context_usage"},
            )
            self.assertTrue(
                any(
                    metric.name == "finding"
                    and dict(metric.dimensions).get("code") == "repeated_tool_call"
                    for metric in result.artifact.run.metrics
                )
            )
            html = result.report_path.read_text(encoding="utf-8")
            self.assertIn("CCR weekly trajectory eval", html)
            self.assertIn("Weekly overview", html)
            self.assertIn("Labeled finding quality", html)
            self.assertIn("Cost and latency", html)
            self.assertIn("Trajectory findings", html)
            self.assertIn("repeated_tool_call", html)
            self.assertIn("Data health", html)
            self.assertNotIn("Evaluation evidence", html)
            self.assertIn("wrong", html)

            session = SessionRecord(
                path=session_path,
                session_id="session-1",
                started_at=window.start,
                cwd=str(root / "repo"),
                model="model-from-session",
                tool_version="version-from-session",
                closed=True,
                finding_count=0,
            )
            with (
                patch(
                    "eval.trajectory.trajectory_judge.detect",
                    side_effect=AssertionError("detectors must not run again"),
                ),
                patch(
                    "eval.trajectory.trajectory_judge.verify",
                    side_effect=AssertionError("verifiers must not run again"),
                ),
                patch(
                    "eval.trajectory.trajectory_judge.measure",
                    side_effect=AssertionError("measurers must not run again"),
                ),
            ):
                CCRTrajectoryReportBuilder().rerender(result.run_dir)
                rows, failures = load_trajectory_rows(
                    (persisted,),
                    (session,),
                )

            self.assertEqual(failures, set())
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]["session_id"], "session-1")
            self.assertEqual(rows[0]["stage"], "review1")
            self.assertEqual(rows[0]["model"], "model-from-session")
            self.assertEqual(
                rows[0]["analysis"]["detections"],
                [
                    item.to_dict()
                    for item in result.artifact.run.detections[0].results
                ],
            )


if __name__ == "__main__":
    unittest.main()

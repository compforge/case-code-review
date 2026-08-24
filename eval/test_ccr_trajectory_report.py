from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from zoneinfo import ZoneInfo

from ccr_source import CCRSessionSource
from ccr_trajectory_report import run_weekly_report
from weekly_report import WeekWindow


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
            labels = root / "labels.jsonl"
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
                            "steps": [],
                        }
                    ],
                }
            )
            source = CCRSessionSource(
                root / "sessions", exporter=lambda _: atif
            )

            result = run_weekly_report(
                window=WeekWindow.from_key("2026-W34", ZoneInfo("UTC")),
                label_paths=[labels],
                source=source,
                runs_dir=root / "runs",
            )

            self.assertTrue(result.dataset_path.is_file())
            self.assertTrue(result.run_path.is_file())
            self.assertTrue(result.report_path.is_file())
            self.assertTrue(result.verdict_path.is_file())
            self.assertEqual(result.artifact.dataset.version, "2026-W34")
            self.assertEqual(result.artifact.dataset.metadata["labels"], 1)
            self.assertEqual(
                result.artifact.dataset.metadata["missing_label_sessions"], 0
            )
            self.assertEqual(
                result.artifact.dataset.trajectories[0].recording_id,
                "session-1",
            )
            self.assertEqual(
                result.artifact.run.target_for("session-1/unit-1"),
                "review1",
            )
            self.assertEqual(
                result.artifact.run.evaluations[0].target,
                "review1",
            )
            self.assertEqual(
                result.artifact.run.evaluations[0].category,
                "quality",
            )
            self.assertEqual(
                result.artifact.run.measurements[0].category,
                "cost",
            )
            html = result.report_path.read_text(encoding="utf-8")
            self.assertIn("CCR weekly trajectory eval", html)
            self.assertIn("Weekly overview", html)
            self.assertIn("Labeled finding quality", html)
            self.assertIn("Cost and latency", html)
            self.assertIn("Data health", html)
            self.assertNotIn("Evaluation evidence", html)
            self.assertIn("wrong", html)


if __name__ == "__main__":
    unittest.main()

from __future__ import annotations

import json
import tempfile
import unittest
from contextlib import redirect_stderr
from io import StringIO
from pathlib import Path

from build_trajectory_dataset import build_dataset
from ccr_source import CCRSessionSource
from trajectory_harness import RecordingRef


class BuildTrajectoryDatasetTest(unittest.TestCase):
    def test_joins_one_label_to_unit_and_lane_trajectories(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            sessions = root / "sessions" / "repo"
            sessions.mkdir(parents=True)
            session = sessions / "session-1.jsonl"
            session.write_text(
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
                        "finding": "anonymous finding",
                        "label": "wrong",
                        "rationale": "anonymous counter-evidence",
                        "source": "github:example/repo#1",
                        "engine": {
                            "session_id": "session-1",
                            "hypothesis": {"origin_unit": "unit-1"},
                            "assessment": {"lane_id": "lane-1"},
                        },
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            atif = json.dumps(
                {
                    "session_id": "session-1",
                    "subagent_trajectories": [
                        {"trajectory_id": "unit-1", "steps": []},
                        {"trajectory_id": "hypothesis_review:lane-1", "steps": []},
                    ],
                }
            )
            source = CCRSessionSource(root / "sessions", exporter=lambda _: atif)

            summary = build_dataset([labels], source, root / "out")

            self.assertEqual(summary["samples"], 1)
            self.assertEqual(summary["trajectory_bundles"], 1)
            sample = json.loads((root / "out" / "samples.jsonl").read_text())
            self.assertEqual(
                sample["trajectory_ids"],
                ["unit-1", "hypothesis_review:lane-1"],
            )
            bundle = json.loads((root / "out" / "trajectories.jsonl").read_text())
            self.assertEqual(len(bundle["trajectories"]), 2)
            self.assertEqual(bundle["recording"]["recording_id"], "session-1")

    def test_reports_session_and_error_when_trajectory_build_fails(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            labels = root / "labels.jsonl"
            labels.write_text(
                json.dumps(
                    {
                        "id": "sample-1",
                        "engine": {"session_id": "session-1"},
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            stderr = StringIO()

            with redirect_stderr(stderr):
                summary = build_dataset(
                    [labels],
                    _FailingSource(),
                    root / "out",
                )

            self.assertEqual(summary["export_failures"], 1)
            self.assertIn("session session-1", stderr.getvalue())
            self.assertIn("export timed out", stderr.getvalue())


class _FailingSource:
    def select(self, query=None):
        del query
        return [RecordingRef("session-1", "file:///unused")]

    def fetch(self, ref):
        raise RuntimeError("export timed out")


if __name__ == "__main__":
    unittest.main()

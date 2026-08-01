from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

import build_label_dataset as dataset


class BuildLabelDatasetTest(unittest.TestCase):
    def test_joins_engine_and_review_artifacts_by_fingerprint(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            sessions = root / "sessions" / "repo"
            sessions.mkdir(parents=True)
            records = [
                {
                    "type": "session_start",
                    "sessionId": "s1",
                    "cwd": "/tmp/repo",
                    "model": "critic-model",
                    "tool_version": "v1.13.2 (abc123)",
                    "features": {"hypothesis_review": True},
                    "params": {"unit_watermark": 10},
                    "git_head": "deadbeef",
                },
                {
                    "type": "artifact",
                    "artifact_kind": "review_hypothesis",
                    "data": {"id": "h-1", "path": "a.go", "trigger": "x"},
                },
                {
                    "type": "artifact",
                    "artifact_kind": "review_assessment",
                    "data": {
                        "hypothesis_id": "h-1",
                        "submission_index": 1,
                        "support": "supported",
                    },
                },
                {
                    "type": "artifact",
                    "artifact_kind": "trial_decision",
                    "data": {
                        "hypothesis_id": "h-1",
                        "assessment_submission_index": 1,
                        "passed_trial": True,
                    },
                },
                {
                    "type": "finding",
                    "timestamp": "2026-07-31T10:00:00Z",
                    "hypothesis_id": "h-1",
                    "fingerprint": "fp1",
                    "content": "real finding",
                },
            ]
            (sessions / "s.jsonl").write_text(
                "".join(json.dumps(record) + "\n" for record in records),
                encoding="utf-8",
            )

            findings = dataset.load_session_findings(root / "sessions")
            example = dataset.build_example(
                {
                    "label": "important",
                    "finding": "real finding",
                    "fingerprint": "fp1",
                    "source": "github:org/repo#1",
                    "reply_id": "1",
                    "at": "2026-07-31T10:01:00Z",
                },
                findings,
            )

            self.assertIsNotNone(example)
            engine = example["engine"]
            self.assertEqual(engine["tool_version"], "v1.13.2 (abc123)")
            self.assertEqual(engine["hypothesis"]["id"], "h-1")
            self.assertEqual(engine["assessment"]["support"], "supported")
            self.assertTrue(engine["trial"]["passed_trial"])


if __name__ == "__main__":
    unittest.main()

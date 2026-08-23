from __future__ import annotations

import json
import tempfile
import unittest
from datetime import UTC, datetime
from pathlib import Path

from ccr_source import CCRSessionSource
from trajectory_harness import RecordingQuery, RecordingSource


class CCRSessionSourceTest(unittest.TestCase):
    def test_selects_closed_sessions_and_fetches_atif(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            repo = root / "repo"
            repo.mkdir()
            sessions = root / "sessions" / "encoded-repo"
            sessions.mkdir(parents=True)
            closed = sessions / "session-1.jsonl"
            _write_session(closed, repo, session_id="session-1", closed=True)
            _write_session(
                sessions / "active.jsonl", repo, session_id="active", closed=False
            )
            exported: list[Path] = []

            def exporter(path: Path) -> str:
                exported.append(path)
                return '{"session_id":"session-1","subagent_trajectories":[]}\n'

            source = CCRSessionSource(
                root / "sessions", repositories=[repo], exporter=exporter
            )
            refs = source.select(
                RecordingQuery(
                    started_at_or_after=datetime(2026, 8, 1, tzinfo=UTC),
                    attributes={"model": "example-model"},
                )
            )

            self.assertIsInstance(source, RecordingSource)
            self.assertEqual([ref.recording_id for ref in refs], ["session-1"])
            recording = source.fetch(refs[0])
            self.assertEqual(exported, [closed.resolve()])
            self.assertIn('"session_id":"session-1"', recording.text)

    def test_rejects_fetch_outside_sessions_root(self) -> None:
        from trajectory_harness import RecordingRef

        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            outside = root / "outside.jsonl"
            outside.write_text("{}\n", encoding="utf-8")
            source = CCRSessionSource(root / "sessions", exporter=lambda _: "{}")
            ref = RecordingRef("outside", outside.resolve().as_uri())

            with self.assertRaisesRegex(ValueError, "outside"):
                source.fetch(ref)


def _write_session(path: Path, repo: Path, *, session_id: str, closed: bool) -> None:
    records = [
        {
            "type": "session_start",
            "sessionId": session_id,
            "timestamp": "2026-08-20T10:00:00Z",
            "cwd": str(repo),
            "model": "example-model",
            "tool_version": "v1.0.0",
        },
        {"type": "finding", "fingerprint": "anonymous-fingerprint"},
    ]
    if closed:
        records.append({"type": "session_end"})
    path.write_text(
        "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
    )


if __name__ == "__main__":
    unittest.main()

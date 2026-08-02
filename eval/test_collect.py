import json
import tempfile
import unittest
from pathlib import Path

from collect import session_complete, summarize


class CollectSummaryTest(unittest.TestCase):
    def test_session_complete_requires_terminal_session_end(self):
        with tempfile.TemporaryDirectory() as tmp:
            session = Path(tmp) / "session.jsonl"
            session.write_text(json.dumps({"type": "session_start"}) + "\n")
            self.assertFalse(session_complete(session))

            with session.open("a") as stream:
                stream.write(json.dumps({"type": "session_end"}) + "\n")
            self.assertTrue(session_complete(session))

    def test_separates_review1_units_and_review2_lanes(self):
        trajectory = {
            "subagent_trajectories": [
                {
                    "trajectory_id": "unit-1",
                    "extra": {"scope_kind": "unit", "file_path": "a.go"},
                    "steps": [],
                },
                {
                    "trajectory_id": "hypothesis_review:lane-1",
                    "extra": {"scope_kind": "lane", "file_path": "0.667"},
                    "steps": [],
                },
            ]
        }
        markdown = []

        summarize("feature", trajectory, markdown, {})

        text = "\n".join(markdown)
        self.assertIn("review1_units=1", text)
        self.assertIn("review2_lanes=1", text)
        self.assertIn("`a.go`", text)
        self.assertIn("`hypothesis_review:lane-1`", text)
        self.assertNotIn("`0.667`", text)


if __name__ == "__main__":
    unittest.main()

import json
from pathlib import Path
import tempfile
import unittest

from eval.session_recording import execution_facts, read_records, timeline_stages


class SessionRecordingTests(unittest.TestCase):
    def test_reader_retains_only_latest_snapshot_and_all_content(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            first = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": [{"id": "first"}]}}
            latest = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": [{"id": "latest"}]}}
            content = {"type": "llm_request", "seq": 2}
            path.write_text("\n".join(json.dumps(record) for record in [first, content, latest]) + "\n")
            records, gaps = read_records(path)
            self.assertEqual(records, [content, latest])
            self.assertEqual(gaps, [])
            with path.open("a") as stream:
                stream.write(json.dumps({**latest, "timeline_id": "foreign"}) + "\n")
            records, gaps = read_records(path)
            self.assertEqual(records, [content, latest])
            self.assertIn("inconsistent timeline ID", gaps[0])

    def test_snapshot_attributes_preserve_execution_outcome(self):
        for attributes in (
            {"attributes": {"outcome": "completed"}},
        ):
            with self.subTest(attributes=attributes):
                stage = {"id": "exec", "name": "execution",
                         "started_at": "2026-10-01T00:00:00Z",
                         "finished_at": "2026-10-01T00:00:01Z", **attributes}
                record = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": [stage]}}
                facts = execution_facts([record])["exec"]
                self.assertEqual(facts["outcome"], "completed")
                self.assertEqual(facts["duration_ms"], 1000)
        stage["attributes"] = {}
        self.assertNotIn("outcome", execution_facts([record])["exec"])

    def test_zero_go_time_is_running_and_latest_snapshot_wins(self):
        stage = {"id": "exec", "name": "execution",
                 "started_at": "2026-10-01T00:00:00Z",
                 "finished_at": "0001-01-01T00:00:00Z",
                 "attributes": {"outcome": "completed"}}
        begin = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": [stage]}}
        self.assertNotIn("outcome", execution_facts([begin])["exec"])
        end = {**stage, "finished_at": "2026-10-01T00:00:01Z"}
        finish = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": [end]}}
        facts = execution_facts([begin, finish, finish])["exec"]
        self.assertEqual(facts["outcome"], "completed")
        self.assertEqual(facts["duration_ms"], 1000)
        empty = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": []}}
        self.assertEqual(timeline_stages([finish, empty]), {})
        conflict = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "other", "stages": [end]}}
        with self.assertRaises(ValueError):
            execution_facts([finish, conflict])

    def test_truncated_and_corrupt_records_keep_prefix_with_gap(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            prefix = json.dumps({"type": "session_start"}).encode() + b"\n"
            for tail, kind in [(b'{"type":', "truncated"), (b'"\xe4', "truncated"),
                               (b'{\n', "corrupt"), (b'null\n', "corrupt")]:
                path.write_bytes(prefix + tail)
                records, gaps = read_records(path)
                self.assertEqual(len(records), 1)
                self.assertIn(kind, gaps[0])
            path.write_bytes(prefix + b'{"type":"session_end"}')
            records, gaps = read_records(path)
            self.assertEqual(len(records), 2)
            self.assertFalse(gaps)

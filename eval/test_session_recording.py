import json
from pathlib import Path
import tempfile
import unittest

from session_recording import execution_facts, read_records


class SessionRecordingTests(unittest.TestCase):
    def test_attributes_and_legacy_fields_preserve_execution_outcome(self):
        for attributes in (
            {"attributes": {"outcome": "completed"}},
            {"fields": {"outcome": "completed"}},
            {"attributes": None, "fields": {"outcome": "completed"}},
            {"attributes": {"outcome": "completed"}, "fields": {"outcome": "failed"}},
        ):
            with self.subTest(attributes=attributes):
                stage = {"id": "exec", "name": "execution", "revision": 2,
                         "started_at": "2026-10-01T00:00:00Z",
                         "finished_at": "2026-10-01T00:00:01Z", **attributes}
                record = {"type": "timeline_update", "update": {"Stages": [stage]}}
                facts = execution_facts([record])["exec"]
                self.assertEqual(facts["outcome"], "completed")
                self.assertEqual(facts["duration_ms"], 1000)
        stage["attributes"] = {}
        self.assertNotIn("outcome", execution_facts([record])["exec"])

    def test_zero_go_time_is_running_and_revisions_merge(self):
        stage = {"id": "exec", "name": "execution", "revision": 1,
                 "started_at": "2026-10-01T00:00:00Z",
                 "finished_at": "0001-01-01T00:00:00Z",
                 "fields": {"outcome": "completed"}}
        begin = {"type": "timeline_update", "update": {"Stages": [stage]}}
        self.assertNotIn("outcome", execution_facts([begin])["exec"])
        end = {**stage, "revision": 2, "finished_at": "2026-10-01T00:00:01Z"}
        finish = {"type": "timeline_update", "update": {"Stages": [end]}}
        facts = execution_facts([begin, finish, begin, finish])["exec"]
        self.assertEqual(facts["outcome"], "completed")
        self.assertEqual(facts["duration_ms"], 1000)
        conflict = {"type": "timeline_update", "update": {"Stages": [{**end, "revision": 3}]}}
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

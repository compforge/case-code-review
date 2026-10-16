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

    def test_shared_delta_fixture_and_gaps(self):
        fixture = Path(__file__).resolve().parents[1] / "internal/harness/session/testdata/timeline-deltas.jsonl"
        raw = fixture.read_text()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            path.write_text(raw)
            records, gaps = read_records(path)
            self.assertFalse(gaps)
            self.assertEqual(len(records), 3)
            snapshot = records[-1]["snapshot"]
            self.assertEqual(snapshot["attributes"], {"done": True})
            self.assertEqual(snapshot["actors"], [{"id": "worker-a"}, {"id": "worker-b"}])
            self.assertEqual([(s["id"], s["actor_ref"], s["status"]) for s in snapshot["stages"]],
                             [("shared", 1, "succeeded"), ("shared", 2, "failed")])
            path.write_text(raw + '{"type":"timeline_delta",')
            partial, gaps = read_records(path)
            self.assertIn("truncated", gaps[0])
            self.assertEqual(partial, records)
            for broken in (
                raw.replace('"base_seq":4', '"base_seq":3'),
                raw.replace('"seq":5', '"seq":4'),
                raw.replace('"actor_ref":1', '"actor_ref":9', 1),
                "\n".join(raw.splitlines()[3:]),
            ):
                path.write_text(broken)
                _, gaps = read_records(path)
                self.assertTrue(gaps)
            # A complete checkpoint replaces the projection, without carrying old stages.
            path.write_text(raw + json.dumps({"type": "timeline_snapshot", "seq": 6, "timeline_id": "run", "snapshot": {"id": "run"}}) + "\n")
            replaced, gaps = read_records(path)
            self.assertFalse(gaps)
            self.assertEqual(timeline_stages(replaced), {})

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

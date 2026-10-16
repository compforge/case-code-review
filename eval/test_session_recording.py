import json
import shutil
from pathlib import Path
import tempfile
import unittest

from eval.eval_snapshot import review_sha256
from eval.session_recording import Recording, execution_facts, read_records, timeline_stages


class SessionRecordingTests(unittest.TestCase):
    def test_reader_retains_only_latest_snapshot_and_all_content(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            first = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": [{"id": "first"}]}}
            latest = {"type": "timeline_snapshot", "timeline_id": "run", "snapshot": {"id": "run", "stages": [{"id": "latest"}]}}
            header = {"type":"session_start","schema_version":13}
            content = {"type": "llm_request", "seq": 2}
            path.write_text("\n".join(json.dumps(record) for record in [header, first, content, latest]) + "\n")
            records, gaps = read_records(path)
            self.assertEqual(records, [header, content, latest])
            self.assertEqual(gaps, [])
            with path.open("a") as stream:
                stream.write(json.dumps({**latest, "timeline_id": "foreign"}) + "\n")
            records, gaps = read_records(path)
            self.assertEqual(records, [header, content, latest])
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
            prefix = json.dumps({"type": "session_start", "schema_version": 13}).encode() + b"\n"
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


class NativeSessionTests(unittest.TestCase):
    def test_native_session_join_matches_go_reader(self):
        path = Path(__file__).parents[1] / "testdata/recording/review.jsonl"
        recording = Recording.read(path)
        self.assertFalse(recording.gaps)
        costs = recording.costs()
        self.assertEqual((costs["rounds"], costs["error_calls"], costs["prompt_tokens"], costs["completion_tokens"]), (3, 1, 240, 20))
        results = {r["result"]: r["request_id"] for r in recording.records if r["type"] == "tool_result"}
        self.assertEqual(results, {"evidence 1": "session-1/run-1/model-1/2", "evidence 2": "session-1/run-2/model-1/1"})
        compacted = [r for r in recording.records if r["type"] == "context_compacted"]
        self.assertEqual([r["tokens_after"] for r in compacted], [50, 50])

    def test_session_without_timeline_keeps_native_call_joins(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(__file__).parents[1] / "testdata/recording"
            shutil.copytree(source, directory, dirs_exist_ok=True)
            path = Path(directory) / "review.jsonl"
            path.with_suffix(".timeline").unlink()
            recording = Recording.read(path)
            self.assertIn("missing review timeline", recording.gaps)
            costs = recording.costs()
            self.assertEqual((costs["rounds"], costs["error_calls"], costs["prompt_tokens"]), (3, 1, 240))
            calls = {r["call_id"] for r in recording.records if r["type"] == "llm_response"}
            tools = [r for r in recording.records if r["type"] == "tool_result"]
            self.assertEqual(len(tools), 2)
            self.assertTrue(all(r["request_id"] in calls for r in tools))

    def test_evidence_hash_covers_timeline_and_loop_sessions(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(__file__).parents[1] / "testdata/recording"
            shutil.copytree(source, directory, dirs_exist_ok=True)
            path = Path(directory) / "review.jsonl"
            original = review_sha256(path)
            loop = path.with_suffix(".sessions") / "loop.jsonl"
            content = loop.read_bytes()
            loop.write_bytes(content.replace(b"evidence 1", b"evidence X"))
            self.assertNotEqual(review_sha256(path), original)
            loop.write_bytes(content)
            self.assertEqual(review_sha256(path), original)
            loop.unlink()
            self.assertNotEqual(review_sha256(path), original)
            loop.write_bytes(content)
            path.with_suffix(".timeline").unlink()
            self.assertNotEqual(review_sha256(path), original)

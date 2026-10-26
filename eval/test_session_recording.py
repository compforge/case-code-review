import json
import shutil
from pathlib import Path
import tempfile
import unittest

from eval.eval_snapshot import review_sha256
from eval.session_recording import Recording, execution_facts, read_records, operation_index


class SessionRecordingTests(unittest.TestCase):
    def test_reader_retains_only_latest_snapshot_and_all_content(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            first = {"type": "operation", "review_id": "run", "operation": {"id": "exec", "status":"running"}}
            latest = {"type": "operation", "review_id": "run", "operation": {"id": "exec", "status":"completed"}}
            header = {"type":"session_start","schema_version":14}
            content = {"type": "llm_request", "seq": 2}
            path.write_text("\n".join(json.dumps(record) for record in [header, first, content, latest]) + "\n")
            records, gaps = read_records(path)
            self.assertEqual(records, [header, first, content, latest])
            self.assertEqual(gaps, [])
            with path.open("a") as stream:
                stream.write(json.dumps({**latest, "review_id": "foreign"}) + "\n")
            records, gaps = read_records(path)
            self.assertEqual(records, [header, first, content, latest])
            self.assertIn("inconsistent review ID", gaps[0])

    def test_snapshot_attributes_preserve_execution_outcome(self):
        for attributes in (
            {"attributes": {"outcome": "completed"}},
        ):
            with self.subTest(attributes=attributes):
                stage = {"id": "exec", "name": "execution",
                         "started_at": "2026-10-01T00:00:00Z",
                         "finished_at": "2026-10-01T00:00:01Z", **attributes}
                record = {"type": "operation", "review_id": "run", "operation": stage}
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
        begin = {"type": "operation", "review_id": "run", "operation": stage}
        self.assertNotIn("outcome", execution_facts([begin])["exec"])
        end = {**stage, "finished_at": "2026-10-01T00:00:01Z"}
        finish = {"type": "operation", "review_id": "run", "operation": end}
        facts = execution_facts([begin, finish, finish])["exec"]
        self.assertEqual(facts["outcome"], "completed")
        self.assertEqual(facts["duration_ms"], 1000)
        self.assertEqual(operation_index([begin, finish])["exec"], end)
        conflict = {**finish, "review_id": "other"}
        with self.assertRaises(ValueError):
            execution_facts([finish, conflict])

    def test_truncated_and_corrupt_records_keep_prefix_with_gap(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            prefix = json.dumps({"type": "session_start", "schema_version": 14}).encode() + b"\n"
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
    def test_business_completion_is_not_inferred_from_closure(self):
        self.assertEqual(Recording([], []).completion, {"status": "running"})
        self.assertEqual(Recording([{"type": "session_end"}], []).completion, {"status": "unknown"})
        completion = {"status": "partial", "units": [{"id": "u", "outcome": "timeout", "unassessed_hypotheses": ["h"]}]}
        recording = Recording([{"type": "session_end", "completion": completion}], [])
        self.assertEqual(recording.completion, completion)

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
            path.write_text("".join(line for line in path.read_text().splitlines(keepends=True) if json.loads(line).get("type") != "operation"))
            recording = Recording.read(path)
            self.assertIn("missing review operations", recording.gaps)
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
            path.write_text("".join(line for line in path.read_text().splitlines(keepends=True) if json.loads(line).get("type") != "operation"))
            self.assertNotEqual(review_sha256(path), original)

    def test_failed_partial_message_retains_usage_without_becoming_success(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(__file__).parents[1] / "testdata/recording"
            shutil.copytree(source, directory, dirs_exist_ok=True)
            path = Path(directory) / "review.jsonl"
            loop = path.with_suffix(".sessions") / "loop.jsonl"
            records = [json.loads(line) for line in loop.read_text().splitlines()]
            for record in records:
                data = record.get("data", {})
                if record["type"] == "model_exec_end" and data.get("error"):
                    data["partial_message"] = {"type": "message", "value": {
                        "role": "assistant", "content": [{"type": "text", "text": "unfinished"}],
                        "usage": {"input": 4, "output": 9, "reasoning": 7},
                    }}
            loop.write_text("".join(json.dumps(record) + "\n" for record in records))
            recording = Recording.read(path)
            self.assertFalse(recording.gaps)
            failed = next(r for r in recording.records if r["type"] == "llm_error")
            self.assertTrue(failed["partial"])
            self.assertEqual(failed["content"], "unfinished")
            costs = recording.costs()
            self.assertEqual((costs["rounds"], costs["error_calls"], costs["unknown_usage"]), (3, 1, 0))
            self.assertEqual((costs["prompt_tokens"], costs["completion_tokens"], costs["reasoning_tokens"]), (244, 29, 7))

import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from eval.eval_snapshot import review_sha256
from eval.trajectory.analyze_cases import analyze
from eval.trajectory.case_evidence import context_outline


class CaseEvidenceTests(unittest.TestCase):
    def test_context_preserves_order_sections_and_unknown_requests(self):
        node = {"id": "call", "session": {"source": "agentgo", "id": "s"},
                "request": {"input": {"messages": [
                    {"role": "system", "content": "# Policy\nCheck recovery"},
                    {"role": "user", "content": "task"},
                    {"role": "user", "content": "# Diff\n" + "x" * 200},
                    {"role": "tool", "content": [{"type": "text", "text": "unavailable"}]}],
                    "tools": [{"name": "read"}]}}}
        outline = context_outline(node)
        self.assertEqual([m["role"] for m in outline["messages"]], ["system", "user", "user", "tool"])
        self.assertEqual(outline["messages"][2]["headings"], ["# Diff"])
        self.assertTrue(outline["messages"][2]["excerpt_truncated"])
        self.assertEqual(outline["messages"][3]["excerpt"], "unavailable")
        self.assertEqual(outline["tools"], [{"name": "read"}])
        self.assertIn("issue", context_outline({"id": "missing"}))

    def test_views_only_retains_gaps_and_analyst_notes_without_evaluation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            recording = root / "review.jsonl"
            records = [{"type": "session_start", "sessionId": "r", "schema_version": 14},
                       {"type": "artifact", "artifact_kind": "recording_gap", "seq": 2,
                        "data": {"error": "checkpoint failed"}}]
            recording.write_text("".join(json.dumps(r) + "\n" for r in records))
            diagnosis = {"producer": "analyst", "criterion": "check recovery", "cause_status": "hypothesis"}
            selected = root / "selected.jsonl"
            selected.write_text(json.dumps({"case_id": "case", "session": str(recording),
                                           "session_sha256": review_sha256(recording),
                                           "unit_ids": ["u1"], "diagnosis": diagnosis}) + "\n")
            node = {"kind": "model_call", "id": "m", "session": {"source": "agentgo", "id": "s"},
                    "evidence": [{"source": "agentgo", "id": "7"}],
                    "request": {"input": {"messages": [{"role": "user", "content": "recovery"}]}}}
            job = {"schema_version": "agent-ledger/job@1", "job": {"source": "ccr", "id": "r/u1"},
                   "attributes": {"unit_id": "u1"}, "status": "completed", "issues": ["missing tool result"],
                   "nodes": [{"kind": "operation", "id": "op", "nodes": [node]}]}
            response = subprocess.CompletedProcess([], 0, json.dumps(job), "")
            with patch("eval.trajectory.case_evidence.subprocess.run", return_value=response) as run:
                result = analyze(selected, root / "out", views_only=True)[0]
            self.assertEqual(run.call_count, 1)
            self.assertEqual(run.call_args.args[0][1:6], ["export", "--format", "job", "--unit", "u1"])
            self.assertEqual(result["diagnosis"], diagnosis)
            evidence = result["evidence"]
            self.assertFalse(evidence["recording_closed"])
            self.assertTrue(any("checkpoint failed" in issue for issue in evidence["issues"]))
            self.assertTrue(any("missing session_end" in issue for issue in evidence["issues"]))
            unit = evidence["units"][0]
            self.assertEqual(unit["status"], "completed")
            self.assertEqual(unit["issues"], ["missing tool result"])
            self.assertEqual(unit["session_calls"][0]["id"], "m")
            self.assertEqual(json.loads(Path(unit["job_view"]).read_text()), job)
            fragment = json.loads(Path(unit["session_views"][0]).read_text())
            self.assertEqual(fragment["session"], node["session"])
            self.assertEqual(fragment["nodes"][0]["id"], "m")
            self.assertIn("Job-owned calls", fragment["attributes"]["selection"])
            self.assertNotIn("trajectories", result)

    def test_changed_recording_is_not_exported(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            recording = root / "review.jsonl"
            recording.write_text('{}\n')
            selected = root / "selected.jsonl"
            selected.write_text(json.dumps({"session": str(recording), "session_sha256": "old", "unit_ids": ["u1"]}))
            with patch("eval.trajectory.case_evidence.subprocess.run") as run:
                result = analyze(selected, root / "out", views_only=True)[0]
            run.assert_not_called()
            self.assertIn("changed", result["error"])

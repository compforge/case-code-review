import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from eval.eval_snapshot import file_sha256
from eval.trajectory.analyze_cases import analyze


class AnalyzeCasesTest(unittest.TestCase):
    def test_missing_changed_and_export_failure_remain_explicit_without_review(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            session = root / "session.jsonl"
            session.write_text('{}\n')
            rows = [{"case_id": "missing"}, {"case_id": "changed", "session": str(session), "session_sha256": "old"},
                    {"case_id": "export-error", "session": str(session), "session_sha256": file_sha256(session)}]
            selected = root / "selected.jsonl"
            selected.write_text("".join(json.dumps(row) + "\n" for row in rows))
            with patch("eval.trajectory.analyze_cases.subprocess.run", side_effect=subprocess.CalledProcessError(1, "export")) as run:
                results = analyze(selected, root / "out")
            self.assertEqual(len(results), 3)
            self.assertTrue(all("error" in row for row in results))
            self.assertEqual(run.call_count, 1)
            self.assertEqual(run.call_args.args[0][1], "export")

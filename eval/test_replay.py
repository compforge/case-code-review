import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from replay import build_schedule, collect, compare_runs, main, run_ccr


class ReplayTest(unittest.TestCase):
    def test_interleaved_schedule_rotates_arm_order_per_run(self):
        entries = [{"name": "case-a"}, {"name": "case-b"}]
        arms = [("base", []), ("candidate", ["--feature", "gate=on"])]

        schedule = build_schedule(entries, arms, 3, "interleaved")

        self.assertEqual(
            [(item[0]["name"], item[1], item[3]) for item in schedule],
            [
                ("case-a", "base", 0),
                ("case-a", "candidate", 0),
                ("case-a", "candidate", 1),
                ("case-a", "base", 1),
                ("case-a", "base", 2),
                ("case-a", "candidate", 2),
                ("case-b", "base", 0),
                ("case-b", "candidate", 0),
                ("case-b", "candidate", 1),
                ("case-b", "base", 1),
                ("case-b", "base", 2),
                ("case-b", "candidate", 2),
            ],
        )

    def test_arm_major_schedule_preserves_legacy_order(self):
        entries = [{"name": "case-a"}]
        arms = [("base", []), ("candidate", [])]

        schedule = build_schedule(entries, arms, 2, "arm-major")

        self.assertEqual(
            [(item[1], item[3]) for item in schedule],
            [("base", 0), ("base", 1), ("candidate", 0), ("candidate", 1)],
        )

    @patch("replay.subprocess.run")
    def test_run_ccr_forwards_generation_controls(self, run):
        run.return_value = subprocess.CompletedProcess([], 0, b"", b"")

        ok, error = run_ccr(
            "/tmp/ccr",
            "/repo",
            {"from": "base", "to": "head"},
            ["--feature", "gate=on"],
            "eval-tag",
            model="model-a",
            concurrency=2,
        )

        self.assertTrue(ok)
        self.assertEqual(error, "")
        self.assertEqual(
            run.call_args.args[0],
            [
                "/tmp/ccr",
                "review",
                "--from",
                "base",
                "--to",
                "head",
                "--feature",
                "gate=on",
                "--model",
                "model-a",
                "--concurrency",
                "2",
            ],
        )
        self.assertEqual(run.call_args.kwargs["env"]["CCR_EVAL_TAG"], "eval-tag")

    def test_comparison_pairs_every_repeat_and_preserves_failed_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "session.jsonl"
            path.write_text('{"type":"session_start"}\n', encoding="utf-8")
            results = {( "case", arm, run): {"session": str(path)}
                       for arm in ("base", "candidate") for run in range(2)}
            results[("case", "candidate", 1)]["error"] = "timeout"
            reports = compare_runs(results, [{"name": "case"}], [("base", []), ("candidate", [])], 2)
            self.assertEqual([r["run"] for r in reports], [0, 1])
            self.assertTrue(any("command failed" in w for w in reports[1]["warnings"]))

    def test_failed_command_keeps_found_transcript_in_runlog(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            corpus = root / "corpus.json"
            corpus.write_text(json.dumps({"entries": [{"name": "case", "from": "a", "to": "b"}]}))
            session = root / "session.jsonl"
            session.write_text('{"type":"session_start"}\n{"type":"finding","path":"a.go","content":"bug"}\n')
            out = root / "out"
            with patch("replay.run_ccr", return_value=(False, "timeout")), \
                 patch("replay.find_session", return_value=session), \
                 patch("sys.argv", ["replay", str(corpus), "--arm", "base", "--out", str(out)]), \
                 patch("builtins.print"):
                self.assertEqual(main(), 0)
            run = json.loads((out / "runs.jsonl").read_text())
            self.assertEqual(run["error"], "timeout")
            self.assertEqual(len(run["findings"]), 1)
            self.assertFalse(run["closed"])

    def test_collect_retains_generation_provenance(self):
        events = [
            {
                "type": "session_start",
                "tool_version": "v1.2.3",
                "git_head": "abc123",
                "model": "model-a",
                "features": {"gate": True},
                "params": {"unit_watermark": 10},
            },
            {
                "type": "session_end",
                "duration_seconds": 12.5,
                "llm_failures": 0,
            },
        ]
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "session.jsonl"
            path.write_text(
                "".join(json.dumps(event) + "\n" for event in events),
                encoding="utf-8",
            )

            result = collect(path)

        self.assertEqual(
            result["generation"],
            {
                "tool_version": "v1.2.3",
                "git_head": "abc123",
                "model": "model-a",
                "features": {"gate": True},
                "params": {"unit_watermark": 10},
            },
        )
        self.assertEqual(result["duration_s"], 12.5)


if __name__ == "__main__":
    unittest.main()

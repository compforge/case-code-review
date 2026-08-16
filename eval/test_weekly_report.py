from __future__ import annotations

import json
import tempfile
import unittest
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

import weekly_report as weekly


class WeeklyReportTest(unittest.TestCase):
    def setUp(self) -> None:
        self.zone = ZoneInfo("Asia/Shanghai")
        self.window = weekly.WeekWindow.from_key("2026-W32", self.zone)

    def test_iso_week_and_previous_window(self) -> None:
        self.assertEqual(self.window.start.isoformat(), "2026-08-03T00:00:00+08:00")
        self.assertEqual(self.window.end.isoformat(), "2026-08-10T00:00:00+08:00")
        self.assertEqual(self.window.previous().key, "2026-W31")
        self.assertEqual(
            weekly.WeekWindow.previous_complete(
                self.zone, datetime.fromisoformat("2026-08-09T12:00:00+08:00")
            ).key,
            "2026-W31",
        )
        self.assertTrue(
            self.window.contains(datetime.fromisoformat("2026-08-09T23:59:59+08:00"))
        )
        self.assertFalse(
            self.window.contains(datetime.fromisoformat("2026-08-10T00:00:00+08:00"))
        )

    def test_discovery_uses_session_timestamp_and_tracks_unclosed(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            repository = root / "repo"
            repository.mkdir()
            sessions = root / "sessions" / weekly.encode_repo_path(repository)
            sessions.mkdir(parents=True)
            closed = sessions / "closed.jsonl"
            closed.write_text(
                "\n".join(
                    json.dumps(record)
                    for record in (
                        {
                            "type": "session_start",
                            "sessionId": "s1",
                            "cwd": str(repository),
                            "timestamp": "2026-08-04T02:00:00Z",
                            "model": "m1",
                            "tool_version": "v1",
                        },
                        {"type": "finding", "fingerprint": "fp1"},
                        {"type": "session_end"},
                    )
                )
                + "\n",
                encoding="utf-8",
            )
            unclosed = sessions / "unclosed.jsonl"
            unclosed.write_text(
                json.dumps(
                    {
                        "type": "session_start",
                        "sessionId": "s2",
                        "cwd": str(repository / ".worktrees" / "feature"),
                        "timestamp": "2026-08-05T02:00:00Z",
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            legacy_repository = root / "legacy-repo"
            legacy_repository.symlink_to(repository, target_is_directory=True)
            legacy_sessions = (
                root / "sessions" / weekly.encode_repo_path(legacy_repository)
            )
            legacy_sessions.mkdir()
            (legacy_sessions / "legacy.jsonl").write_text(
                "\n".join(
                    json.dumps(record)
                    for record in (
                        {
                            "type": "session_start",
                            "sessionId": "s3",
                            "cwd": str(legacy_repository),
                            "timestamp": "2026-08-06T02:00:00Z",
                        },
                        {"type": "session_end"},
                    )
                )
                + "\n",
                encoding="utf-8",
            )
            unrelated = root / "sessions" / "unrelated"
            unrelated.mkdir()
            (unrelated / "not-a-session.jsonl").write_text("{}\n", encoding="utf-8")

            discovered, invalid = weekly.discover_sessions(
                root / "sessions", self.zone, [str(repository)]
            )

            self.assertEqual(invalid, 0)
            by_id = {session.session_id: session for session in discovered}
            self.assertEqual(set(by_id), {"s1", "s2", "s3"})
            self.assertTrue(by_id["s1"].closed)
            self.assertFalse(by_id["s2"].closed)
            self.assertEqual(by_id["s1"].finding_count, 1)

    def test_stage_metrics_keep_completion_timeout_and_cost_separate(self) -> None:
        rows = [
            {
                "stage": "review1",
                "outcome": "completed",
                "score": 1.0,
                "rounds": 2,
                "duration_sec": 10,
                "prompt_tokens": 100,
                "completion_tokens": 10,
                "tool_freq": {"read_files": 1},
                "signals": {"evaluations": []},
            },
            {
                "stage": "review1",
                "outcome": "timeout",
                "score": 0.5,
                "rounds": 4,
                "duration_sec": 50,
                "prompt_tokens": 300,
                "completion_tokens": 30,
                "tool_freq": {"search_code": 2},
                "signals": {
                    "evaluations": [],
                    "llm_failures": [
                        {
                            "key": "llm.routing.timeout",
                            "request_phase": "await_response",
                        }
                    ],
                },
            },
            {
                "stage": "review1",
                "outcome": "unknown",
                "score": 1.0,
                "rounds": 1,
                "duration_sec": 5,
                "prompt_tokens": 50,
                "completion_tokens": 5,
                "tool_freq": {},
                "signals": {"evaluations": []},
            },
        ]

        metrics = weekly.aggregate_stage(rows, "review1")

        self.assertEqual(metrics["chains"], 3)
        self.assertEqual(metrics["outcome_coverage"], 0.667)
        self.assertEqual(metrics["completion_rate"], 0.5)
        self.assertEqual(metrics["timeout_rate"], 0.5)
        self.assertEqual(metrics["average_score"], 0.833)
        self.assertEqual(metrics["duration_sec"]["average"], 21.667)
        self.assertEqual(metrics["duration_sec"]["p95"], 50)
        self.assertEqual(metrics["prompt_tokens"]["average"], 150)
        self.assertEqual(metrics["tool_freq"], {"search_code": 2, "read_files": 1})
        self.assertEqual(
            metrics["llm_failures"],
            {
                "total": 1,
                "items": [
                    {
                        "failure": "llm.routing.timeout",
                        "request_phase": "await_response",
                        "count": 1,
                    }
                ],
            },
        )

    def test_unit_duration_records_keep_each_unit_and_sort_slowest_first(self) -> None:
        rows = [
            {
                "stage": "review1",
                "session_id": "s1",
                "trajectory_id": "a.py",
                "execution_id": "e1",
                "unit": "a.py",
                "outcome": "completed",
                "reason": "",
                "duration_sec": 12.5,
                "rounds": 2,
                "prompt_tokens": 100,
                "completion_tokens": 20,
                "cached_tokens": 10,
                "model": "m1",
                "tool_version": "v1",
            },
            {
                "stage": "review1",
                "session_id": "s1",
                "trajectory_id": "b.py",
                "execution_id": "e2",
                "unit": "b.py",
                "outcome": "timeout",
                "reason": "deadline",
                "duration_sec": 40,
                "rounds": 4,
                "prompt_tokens": 300,
                "completion_tokens": 30,
                "cached_tokens": 0,
                "model": "m1",
                "tool_version": "v1",
            },
            {"stage": "review2", "duration_sec": 99},
        ]

        records = weekly.unit_duration_records(rows)

        self.assertEqual([record["unit"] for record in records], ["b.py", "a.py"])
        self.assertEqual(records[0]["duration_sec"], 40)
        self.assertEqual(records[0]["execution_id"], "e2")

    def test_quality_separates_review_week_from_label_week(self) -> None:
        session = weekly.SessionRecord(
            path=Path("s.jsonl"),
            session_id="s1",
            started_at=datetime.fromisoformat("2026-08-04T10:00:00+08:00"),
            cwd="/repo",
            model="m1",
            tool_version="v1",
            closed=True,
            finding_count=2,
        )
        datasets = [
            {
                "id": "a",
                "kind": "finding",
                "label": "wrong",
                "tags": ["cross-file"],
                "at": "2026-08-11T10:00:00+08:00",
                "engine": {"session_id": "s1"},
            },
            {
                "id": "b",
                "kind": "finding",
                "label": "important",
                "at": "2026-08-06T10:00:00+08:00",
                "engine": {"session_id": "older"},
            },
        ]

        metrics = weekly.quality_metrics(
            datasets, [session], self.window, {"s1", "older"}, False
        )

        self.assertEqual(metrics["review_week"]["by_label"], {"wrong": 1})
        self.assertEqual(metrics["review_week"]["label_coverage"], 0.5)
        self.assertEqual(metrics["labeled_this_week"]["by_label"], {"important": 1})
        self.assertEqual(metrics["review_week"]["wrong_tags"], {"cross-file": 1})

    def test_comparison_includes_week_over_week_duration(self) -> None:
        current = {
            "review1": {"duration_sec": {"average": 20, "p50": 15, "p95": 40}},
            "review2": {"duration_sec": {"average": 30, "p50": 25, "p95": 50}},
        }
        previous = {
            "review1": {"duration_sec": {"average": 10, "p50": 12, "p95": 30}},
            "review2": {"duration_sec": {"average": 20, "p50": 20, "p95": 45}},
        }

        comparison = {
            item["metric"]: item
            for item in weekly.build_comparison(current, previous)
        }

        review1_average = comparison["Review 1 average duration (sec)"]
        self.assertEqual(review1_average["current"], 20)
        self.assertEqual(review1_average["previous"], 10)
        self.assertEqual(review1_average["delta"], 10)
        self.assertEqual(review1_average["change_pct"], 1.0)
        self.assertEqual(comparison["Review 2 p95 duration (sec)"]["delta"], 5)

    def test_writes_week_partition_with_machine_and_human_reports(self) -> None:
        empty = {
            "week": "2026-W32",
            "window": {
                "start": self.window.start.isoformat(),
                "end": self.window.end.isoformat(),
                "timezone": "Asia/Shanghai",
            },
            "sessions": {
                "total": 0,
                "closed": 0,
                "unclosed": 0,
                "repositories": 0,
                "finding_events": 0,
                "by_tool_version": {},
                "by_model": {},
            },
            "review1": weekly.aggregate_stage([], "review1"),
            "review2": weekly.aggregate_stage([], "review2"),
            "unknown": weekly.aggregate_stage([], "unknown"),
            "quality": {
                "review_week": {
                    "examples": 0,
                    "labeled_findings": 0,
                    "by_label": {},
                    "wrong_tags": {},
                    "label_coverage": None,
                },
                "labeled_this_week": {
                    "examples": 0,
                    "by_label": {},
                    "wrong_tags": {},
                    "without_session": 0,
                },
            },
            "data_quality": {
                "invalid_session_files_in_scan": 0,
                "trajectory_export_failures": 0,
                "missing_dataset_files": 2,
                "invalid_dataset_lines": 0,
            },
        }
        previous = json.loads(json.dumps(empty))
        previous["week"] = "2026-W31"
        comparison = weekly.build_comparison(empty, previous)
        with tempfile.TemporaryDirectory() as raw:
            out = weekly.write_report(
                Path(raw),
                empty,
                previous,
                comparison,
                {"week": "2026-W32"},
                [
                    {
                        "session_id": "s1",
                        "trajectory_id": "src/a.py",
                        "execution_id": "e1",
                        "unit": "src/a.py",
                        "outcome": "completed",
                        "reason": "",
                        "duration_sec": 12,
                        "rounds": 2,
                        "prompt_tokens": 100,
                        "completion_tokens": 20,
                        "cached_tokens": 0,
                        "model": "m1",
                        "tool_version": "v1",
                    }
                ],
            )
            self.assertEqual(out.name, "2026-W32")
            self.assertTrue((out / "REPORT.md").is_file())
            self.assertTrue((out / "metrics.json").is_file())
            self.assertTrue((out / "manifest.json").is_file())
            self.assertTrue((out / "unit-durations.jsonl").is_file())
            unit = json.loads(
                (out / "unit-durations.jsonl").read_text(encoding="utf-8")
            )
            self.assertEqual(unit["unit"], "src/a.py")
            report = (out / "REPORT.md").read_text(encoding="utf-8")
            self.assertIn("Slowest Review 1 units", report)
            self.assertIn("LLM failures", report)
            self.assertIn("avg sec", report)


if __name__ == "__main__":
    unittest.main()

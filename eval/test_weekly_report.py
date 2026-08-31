from __future__ import annotations

import json
import tempfile
import unittest
from datetime import datetime
from pathlib import Path
from unittest import mock
from zoneinfo import ZoneInfo

import weekly_report as weekly
from eval_snapshot import dataset_artifacts


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

    def test_default_datasets_resolve_from_main_worktree(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            main = root / "main"
            worktree = root / "worktree"
            datasets = main / "eval/data/datasets"
            datasets.mkdir(parents=True)
            worktree.mkdir()
            for name in (
                "review-comments-public.jsonl",
                "review-comments-private.jsonl",
            ):
                (datasets / name).write_text("", encoding="utf-8")
            dataset_manifest = datasets / "label-dataset.json"
            dataset_manifest.write_text("{}\n", encoding="utf-8")
            completed = mock.Mock(stdout=str(main / ".git") + "\n")
            with mock.patch.object(weekly.subprocess, "run", return_value=completed):
                paths = weekly.default_dataset_paths(worktree)
                manifest = weekly.default_label_dataset_manifest(worktree)

            self.assertEqual(
                paths,
                [
                    (datasets / "review-comments-public.jsonl").resolve(),
                    (datasets / "review-comments-private.jsonl").resolve(),
                ],
            )
            self.assertEqual(manifest, dataset_manifest.resolve())

    def test_github_label_sync_exposes_missing_and_complete_harvests(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            public = root / "review-comments-public.jsonl"
            private = root / "review-comments-private.jsonl"
            public.write_text('{"id":"one"}\n', encoding="utf-8")
            private.write_text("", encoding="utf-8")
            paths = [public, private]
            harvest = {
                "schema_version": "github-label-harvest-v2",
                "snapshot_id": "harvest-1",
                "generated_at": "2026-08-10T00:00:00Z",
                "query": {
                    "start": "2026-08-03T00:00:00+08:00",
                    "end": "2026-08-10T00:00:00+08:00",
                },
                "pull_requests_discovered": 12,
                "pull_requests_harvested": 12,
                "pull_requests_failed": 0,
            }
            dataset_manifest = {
                "schema_version": "label-dataset-v1",
                "inputs": {"github_harvest_snapshot_id": "harvest-1"},
                "artifacts": dataset_artifacts(((public, 1), (private, 0))),
                "stats": {"unpaired": 0},
            }

            missing = weekly.github_label_sync_metrics(
                paths, self.window, None, False, None, False
            )
            ready = weekly.github_label_sync_metrics(
                paths, self.window, harvest, False, dataset_manifest, False
            )
            stale_manifest = {
                **dataset_manifest,
                "inputs": {"github_harvest_snapshot_id": "harvest-0"},
            }
            stale = weekly.github_label_sync_metrics(
                paths, self.window, harvest, False, stale_manifest, False
            )
            public.write_text('{"id":"changed"}\n', encoding="utf-8")
            changed = weekly.github_label_sync_metrics(
                paths, self.window, harvest, False, dataset_manifest, False
            )

        self.assertEqual(missing["status"], "missing")
        self.assertEqual(ready["status"], "ready")
        self.assertEqual(ready["harvest_coverage"], 1.0)
        self.assertTrue(ready["covers_report_window"])
        self.assertTrue(ready["dataset_current"])
        self.assertEqual(stale["status"], "dataset_stale")
        self.assertFalse(stale["dataset_current"])
        self.assertEqual(changed["status"], "dataset_changed")
        self.assertFalse(changed["artifacts_current"])

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
                "analysis": {
                    "code_searches": {
                        "calls": 1,
                        "requests": 2,
                        "context_projections": 1,
                        "returned_context_lines": 5,
                        "context_truncated_results": 1,
                    },
                    "search_then_read": {
                        "hit_search_request_count": 1,
                        "context_hit_search_request_count": 1,
                    },
                    "initial_context": {
                        "outlines": {
                            "attempts": 2,
                            "outcomes": {"admitted": 1, "empty": 1},
                            "by_language": {
                                "go": {"admitted": 1},
                                "markdown": {"empty": 1},
                            },
                            "admitted_bytes": 80,
                        }
                    },
                    "evaluations": [
                        {
                            "findings": [
                                {
                                    "severity": "info",
                                    "code": "search_then_read",
                                }
                            ]
                        }
                    ],
                },
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
                "analysis": {
                    "code_searches": {
                        "calls": 2,
                        "requests": 3,
                    },
                    "search_then_read": {
                        "hit_search_request_count": 2,
                        "follow_up_read_request_count": 1,
                        "plain_hit_search_request_count": 2,
                        "plain_follow_up_read_request_count": 1,
                    },
                    "evaluations": [
                        {
                            "findings": [
                                {
                                    "severity": "warning",
                                    "code": "unbatched_same_turn_reads",
                                }
                            ]
                        }
                    ],
                    "failures": [
                        {
                            "impact": "execution",
                            "key": "workflow.timeout",
                        }
                    ],
                },
            },
            {
                "stage": "review1",
                "outcome": "llm_error",
                "score": 0.5,
                "rounds": 3,
                "duration_sec": 25,
                "prompt_tokens": 200,
                "completion_tokens": 20,
                "tool_freq": {},
                "analysis": {
                    "evaluations": [],
                    "failures": [
                        {"impact": "step", "key": "llm.routing.timeout"},
                        {"impact": "execution", "key": "llm.routing.timeout"},
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
                "analysis": {"evaluations": []},
            },
        ]

        metrics = weekly.aggregate_stage(rows, "review1")

        self.assertEqual(metrics["chains"], 4)
        self.assertEqual(metrics["outcome_coverage"], 0.75)
        self.assertEqual(metrics["completion_rate"], 0.333)
        self.assertEqual(metrics["workflow_timeout_rate"], 0.25)
        self.assertEqual(metrics["llm_routing_timeout_rate"], 0.25)
        self.assertEqual(metrics["average_score"], 0.75)
        self.assertEqual(metrics["duration_sec"]["average"], 22.5)
        self.assertEqual(metrics["duration_sec"]["p95"], 50)
        self.assertEqual(metrics["prompt_tokens"]["average"], 162.5)
        self.assertEqual(metrics["tool_freq"], {"search_code": 2, "read_files": 1})
        self.assertEqual(
            metrics["code_searches"],
            {
                "calls": 3,
                "requests": 5,
                "context_projections": 1,
                "context_projection_rate": 0.2,
                "returned_context_lines": 5,
                "context_truncated_results": 1,
                "context_unavailable_results": 0,
                "symbol_context_attempts": 0,
                "symbol_context_outcomes": {},
                "returned_symbol_context_lines": 0,
            },
        )
        self.assertEqual(
            metrics["search_follow_up"],
            {
                "hit_search_requests": 3,
                "follow_up_read_requests": 1,
                "follow_up_read_rate": 0.333,
                "context_hit_search_requests": 1,
                "context_follow_up_read_requests": 0,
                "context_follow_up_read_rate": 0.0,
                "plain_hit_search_requests": 2,
                "plain_follow_up_read_requests": 1,
                "plain_follow_up_read_rate": 0.5,
                "symbol_hit_search_requests": 0,
                "symbol_follow_up_read_requests": 0,
                "symbol_follow_up_read_rate": None,
                "symbol_expanded_hit_search_requests": 0,
                "symbol_expanded_follow_up_read_requests": 0,
                "symbol_expanded_follow_up_read_rate": None,
                "symbol_expanded_within_span_follow_up_read_requests": 0,
                "symbol_expanded_within_span_follow_up_read_rate": None,
                "symbol_expanded_extending_follow_up_read_requests": 0,
                "symbol_expanded_extending_follow_up_read_rate": None,
            },
        )
        self.assertEqual(
            metrics["initial_outlines"],
            {
                "attempts": 2,
                "admission_rate": 0.5,
                "admitted_bytes": 80,
                "outcomes": {"admitted": 1, "empty": 1},
                "by_language": {
                    "go": {"admitted": 1},
                    "markdown": {"empty": 1},
                },
            },
        )
        self.assertEqual(
            metrics["failures"],
            {
                "events": 3,
                "items": [
                    {
                        "impact": "execution",
                        "failure": "llm.routing.timeout",
                        "count": 1,
                        "affected_chains": 1,
                        "rate": 0.25,
                    },
                    {
                        "impact": "execution",
                        "failure": "workflow.timeout",
                        "count": 1,
                        "affected_chains": 1,
                        "rate": 0.25,
                    },
                    {
                        "impact": "step",
                        "failure": "llm.routing.timeout",
                        "count": 1,
                        "affected_chains": 1,
                        "rate": 0.25,
                    },
                ],
            },
        )
        self.assertEqual(
            metrics["diagnostic_findings"],
            {
                "events": 2,
                "items": [
                    {
                        "severity": "info",
                        "finding": "search_then_read",
                        "count": 1,
                        "affected_chains": 1,
                        "rate": 0.25,
                    },
                    {
                        "severity": "warning",
                        "finding": "unbatched_same_turn_reads",
                        "count": 1,
                        "affected_chains": 1,
                        "rate": 0.25,
                    },
                ],
            },
        )

    def test_review2_cost_is_reported_per_lane_and_per_assessment(self) -> None:
        rows = [
            {
                "stage": "review2",
                "outcome": "completed",
                "score": 1.0,
                "rounds": 6,
                "duration_sec": 120,
                "prompt_tokens": 300,
                "completion_tokens": 30,
                "cached_tokens": 15,
                "tool_freq": {},
                "analysis": {"evaluations": [], "assessment_count": 2},
            },
            {
                "stage": "review2",
                "outcome": "completed",
                "score": 1.0,
                "rounds": 3,
                "duration_sec": 60,
                "prompt_tokens": 90,
                "completion_tokens": 15,
                "cached_tokens": 0,
                "tool_freq": {},
                "analysis": {"evaluations": [], "assessment_count": 1},
            },
        ]

        metrics = weekly.aggregate_stage(rows, "review2")

        self.assertEqual(metrics["chains"], 2)
        self.assertEqual(metrics["assessments"], 3)
        self.assertEqual(metrics["duration_sec"]["average"], 90)
        self.assertEqual(metrics["prompt_tokens"]["average"], 195)
        self.assertEqual(
            metrics["per_assessment"],
            {
                "duration_sec": 60.0,
                "prompt_tokens": 130.0,
                "completion_tokens": 15.0,
                "cached_tokens": 5.0,
                "total_tokens": 145.0,
            },
        )
        self.assertEqual(
            metrics["code_searches"],
            {
                "calls": 0,
                "requests": 0,
                "context_projections": 0,
                "context_projection_rate": None,
                "returned_context_lines": 0,
                "context_truncated_results": 0,
                "context_unavailable_results": 0,
                "symbol_context_attempts": 0,
                "symbol_context_outcomes": {},
                "returned_symbol_context_lines": 0,
            },
        )

    def test_cohorts_keep_tool_model_and_repository_separate(self) -> None:
        def row(tool_version: str, model: str, repository: str) -> dict:
            return {
                "stage": "review1",
                "tool_version": tool_version,
                "model": model,
                "repository": repository,
                "outcome": "completed",
                "score": 1.0,
                "rounds": 1,
                "duration_sec": 10,
                "prompt_tokens": 100,
                "completion_tokens": 10,
                "tool_freq": {},
                "analysis": {"evaluations": []},
            }

        cohorts = weekly.aggregate_cohorts(
            [row("v1", "m1", "/repo"), row("v2", "m1", "/repo")]
        )

        self.assertEqual(
            [(item["tool_version"], item["chains"]) for item in cohorts],
            [("v1", 1), ("v2", 1)],
        )
        self.assertEqual(cohorts[0]["completion_rate"], 1.0)

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
            finding_count=5,
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
                "id": "accepted-important",
                "kind": "finding",
                "label": "important",
                "at": "2026-08-11T10:00:00+08:00",
                "engine": {"session_id": "s1"},
            },
            {
                "id": "accepted-minor",
                "kind": "finding",
                "label": "minor",
                "at": "2026-08-11T10:00:00+08:00",
                "engine": {"session_id": "s1"},
            },
            {
                "id": "repeat",
                "kind": "finding",
                "label": "repeat",
                "at": "2026-08-11T10:00:00+08:00",
                "engine": {"session_id": "s1"},
            },
            {
                "id": "debatable",
                "kind": "finding",
                "label": "debatable",
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
            {
                "id": "missed",
                "kind": "missed",
                "label": "missed",
                "at": "2026-08-06T11:00:00+08:00",
            },
        ]

        metrics = weekly.quality_metrics(
            datasets, [session], self.window, {"s1", "older"}, False, "ready"
        )

        self.assertEqual(metrics["label_dataset"], {"status": "ready", "records": 7})
        self.assertEqual(
            metrics["review_week"]["by_label"],
            {"debatable": 1, "important": 1, "minor": 1, "repeat": 1, "wrong": 1},
        )
        self.assertEqual(metrics["review_week"]["label_coverage"], 1.0)
        self.assertEqual(metrics["review_week"]["accepted_findings"], 2)
        self.assertEqual(metrics["review_week"]["accepted_rate"], 0.4)
        self.assertEqual(metrics["review_week"]["wrong_rate"], 0.2)
        self.assertEqual(metrics["review_week"]["repeat_rate"], 0.2)
        self.assertEqual(metrics["review_week"]["debatable_rate"], 0.2)
        self.assertIsNone(metrics["review_week"]["recall_rate"])
        self.assertEqual(
            metrics["labeled_this_week"]["by_label"],
            {"important": 1, "missed": 1},
        )
        self.assertEqual(metrics["labeled_this_week"]["missed_findings_reported"], 1)
        self.assertEqual(metrics["review_week"]["wrong_tags"], {"cross-file": 1})

        cost_effect = weekly.cost_effect_metrics(
            [
                {
                    "total_tokens": 600,
                    "prompt_tokens": 500,
                    "completion_tokens": 100,
                    "cached_tokens": 200,
                    "uncached_tokens": 300,
                    "model_calls": 4,
                    "usage_reported_calls": 3,
                }
            ],
            metrics,
        )
        self.assertEqual(cost_effect["labeled_accepted_findings"], 2)
        self.assertEqual(cost_effect["tokens_per_labeled_accepted_finding"], 300)
        self.assertEqual(cost_effect["usage_coverage"], 0.75)

    def test_missing_dataset_makes_label_quality_unavailable(self) -> None:
        session = weekly.SessionRecord(
            path=Path("s.jsonl"),
            session_id="s1",
            started_at=datetime.fromisoformat("2026-08-04T10:00:00+08:00"),
            cwd="/repo",
            model="m1",
            tool_version="v1",
            closed=True,
            finding_count=1,
        )
        metrics = weekly.quality_metrics(
            [
                {
                    "kind": "finding",
                    "label": "important",
                    "engine": {"session_id": "s1"},
                }
            ],
            [session],
            self.window,
            {"s1"},
            False,
            "missing",
        )

        self.assertEqual(metrics["label_dataset"]["status"], "missing")
        self.assertIsNone(metrics["review_week"]["labeled_findings"])
        self.assertEqual(metrics["review_week"]["by_label"], {})
        self.assertIsNone(metrics["review_week"]["label_coverage"])
        self.assertIsNone(metrics["review_week"]["accepted_rate"])
        self.assertIsNone(metrics["review_week"]["wrong_rate"])
        self.assertIsNone(metrics["labeled_this_week"]["examples"])
        self.assertIsNone(metrics["labeled_this_week"]["missed_findings_reported"])

    def test_build_week_metrics_projects_dataset_health(self) -> None:
        common = {
            "window": self.window,
            "all_sessions": [],
            "rows": [],
            "failed_session_ids": set(),
            "datasets": [],
            "invalid_session_files": 0,
            "repositories_scoped": False,
        }

        missing = weekly.build_week_metrics(
            missing_datasets=1, invalid_dataset_lines=0, **common
        )
        invalid = weekly.build_week_metrics(
            missing_datasets=0, invalid_dataset_lines=1, **common
        )
        unsynced = weekly.build_week_metrics(
            missing_datasets=0, invalid_dataset_lines=0, **common
        )

        self.assertEqual(missing["quality"]["label_dataset"]["status"], "missing")
        self.assertEqual(invalid["quality"]["label_dataset"]["status"], "invalid")
        self.assertEqual(unsynced["quality"]["label_dataset"]["status"], "missing")
        self.assertIsNone(unsynced["quality"]["review_week"]["label_coverage"])
        self.assertIsNone(invalid["quality"]["review_week"]["accepted_rate"])

    def test_comparison_includes_week_over_week_duration(self) -> None:
        current = {
            "review1": {
                "duration_sec": {"average": 20, "p50": 15, "p95": 40},
                "code_searches": {"requests": 12, "context_projection_rate": 0.75},
            },
            "review2": {
                "duration_sec": {"average": 30, "p50": 25, "p95": 50},
                "code_searches": {"requests": 4, "context_projection_rate": 1.0},
            },
        }
        previous = {
            "review1": {
                "duration_sec": {"average": 10, "p50": 12, "p95": 30},
                "code_searches": {"requests": 8, "context_projection_rate": 0.5},
            },
            "review2": {
                "duration_sec": {"average": 20, "p50": 20, "p95": 45},
                "code_searches": {"requests": 0, "context_projection_rate": None},
            },
        }

        comparison = {
            item["metric"]: item for item in weekly.build_comparison(current, previous)
        }

        review1_average = comparison["Review 1 average duration (sec)"]
        self.assertEqual(review1_average["current"], 20)
        self.assertEqual(review1_average["previous"], 10)
        self.assertEqual(review1_average["delta"], 10)
        self.assertEqual(review1_average["change_pct"], 1.0)
        self.assertEqual(comparison["Review 2 p95 duration (sec)"]["delta"], 5)
        self.assertEqual(comparison["Review 1 search requests"]["delta"], 4)
        self.assertEqual(comparison["Review 1 search context projection"]["delta"], 0.25)
        self.assertIsNone(comparison["Review 2 search context projection"]["delta"])

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
                "label_dataset": {"status": "missing", "records": 0},
                "review_week": {
                    "examples": None,
                    "labeled_findings": None,
                    "accepted_findings": None,
                    "by_label": {},
                    "wrong_tags": {},
                    "label_coverage": None,
                    "accepted_rate": None,
                    "wrong_rate": None,
                    "repeat_rate": None,
                    "debatable_rate": None,
                    "recall_rate": None,
                },
                "labeled_this_week": {
                    "examples": None,
                    "by_label": {},
                    "wrong_tags": {},
                    "without_session": None,
                    "missed_findings_reported": None,
                },
            },
            "data_quality": {
                "invalid_session_files_in_scan": 0,
                "trajectory_export_failures": 0,
                "missing_dataset_files": 2,
                "invalid_dataset_lines": 0,
            },
        }
        empty["cost_effect"] = weekly.cost_effect_metrics([], empty["quality"])
        empty["review1"]["code_searches"] = {
            "calls": 1,
            "requests": 2,
            "context_projection_rate": 1.0,
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
            metrics = json.loads((out / "metrics.json").read_text(encoding="utf-8"))
            self.assertEqual(metrics["schema_version"], "weekly-report-v10")
            unit = json.loads(
                (out / "unit-durations.jsonl").read_text(encoding="utf-8")
            )
            self.assertEqual(unit["unit"], "src/a.py")
            report = (out / "REPORT.md").read_text(encoding="utf-8")
            self.assertIn("Slowest Review 1 units", report)
            self.assertIn("Failures", report)
            self.assertIn("Diagnostic findings", report)
            self.assertIn("Search context effectiveness", report)
            self.assertIn("Initial FileOutline availability", report)
            self.assertIn("Execution cohorts", report)
            self.assertIn("Workflow timeout", report)
            self.assertIn("llm.routing.timeout", report)
            self.assertIn("Total/Assessment", report)
            self.assertIn("avg sec", report)
            self.assertIn("finding-quality rates are unavailable", report)
            self.assertIn("Label snapshot manifest is missing", report)
            self.assertIn("Recall remains unavailable", report)


if __name__ == "__main__":
    unittest.main()

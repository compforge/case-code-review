#!/usr/bin/env python3
"""Build one reproducible weekly CCR eval report and compare it with last week.

Session JSONL and normalized label datasets remain the sources of truth. This
script only materializes a weekly read model under
``eval/data/reports/weekly/<YYYY-Www>/``.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import subprocess
import sys
from collections import Counter
from dataclasses import dataclass
from datetime import date, datetime, time, timedelta, timezone
from pathlib import Path
from typing import Any, Iterable, cast
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from ccr_trajectory import ATIFTrajectoryLoader, REVIEW1, REVIEW2, UNKNOWN_STAGE
from trajectory_judge import main_deductions, objective_signals
from weekly_report_render import render_markdown

DEFAULT_DATASETS = (
    Path("eval/data/datasets/review-comments-public.jsonl"),
    Path("eval/data/datasets/review-comments-private.jsonl"),
)
WEEK_RE = re.compile(r"^(\d{4})-W(\d{2})$")
REPORT_SCHEMA_VERSION = "weekly-report-v5"


@dataclass(frozen=True, slots=True)
class WeekWindow:
    key: str
    start: datetime
    end: datetime

    @classmethod
    def from_key(cls, key: str, zone: ZoneInfo) -> "WeekWindow":
        match = WEEK_RE.fullmatch(key)
        if not match:
            raise ValueError(f"invalid ISO week {key!r}; expected YYYY-Www")
        year, week = (int(value) for value in match.groups())
        start_date = date.fromisocalendar(year, week, 1)
        start = datetime.combine(start_date, time.min, tzinfo=zone)
        return cls(key=key, start=start, end=start + timedelta(days=7))

    @classmethod
    def previous_complete(
        cls, zone: ZoneInfo, now: datetime | None = None
    ) -> "WeekWindow":
        local_now = (now or datetime.now(timezone.utc)).astimezone(zone)
        current_monday = local_now.date() - timedelta(days=local_now.weekday())
        previous_monday = current_monday - timedelta(days=7)
        iso_year, iso_week, _ = previous_monday.isocalendar()
        return cls.from_key(f"{iso_year}-W{iso_week:02d}", zone)

    def previous(self) -> "WeekWindow":
        previous_date = self.start.date() - timedelta(days=7)
        iso_year, iso_week, _ = previous_date.isocalendar()
        return WeekWindow.from_key(
            f"{iso_year}-W{iso_week:02d}", cast(ZoneInfo, self.start.tzinfo)
        )

    def contains(self, value: datetime) -> bool:
        local = value.astimezone(self.start.tzinfo)
        return self.start <= local < self.end


@dataclass(frozen=True, slots=True)
class SessionRecord:
    path: Path
    session_id: str
    started_at: datetime
    cwd: str
    model: str
    tool_version: str
    closed: bool
    finding_count: int


def parse_timestamp(value: Any, default_zone: ZoneInfo) -> datetime | None:
    if not value:
        return None
    try:
        parsed = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=default_zone)
    return parsed


def read_session(path: Path, zone: ZoneInfo) -> SessionRecord | None:
    manifest: dict[str, Any] | None = None
    first_timestamp: datetime | None = None
    last_type = ""
    finding_count = 0
    try:
        with path.open(encoding="utf-8") as stream:
            for line in stream:
                if not line.strip():
                    continue
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    continue
                last_type = str(record.get("type") or "")
                if first_timestamp is None:
                    first_timestamp = parse_timestamp(record.get("timestamp"), zone)
                if last_type == "session_start":
                    manifest = record
                elif last_type == "finding":
                    finding_count += 1
    except OSError:
        return None
    if manifest is None:
        return None
    started_at = parse_timestamp(manifest.get("timestamp"), zone) or first_timestamp
    if started_at is None:
        return None
    return SessionRecord(
        path=path,
        session_id=str(manifest.get("sessionId") or path.stem),
        started_at=started_at,
        cwd=str(manifest.get("cwd") or ""),
        model=str(manifest.get("model") or "unknown"),
        tool_version=str(manifest.get("tool_version") or "unknown"),
        closed=last_type == "session_end",
        finding_count=finding_count,
    )


def _resolved(path: str | Path) -> str:
    return str(Path(path).expanduser().resolve())


def encode_repo_path(repository: str | Path) -> str:
    return os.path.abspath(repository).lstrip(os.sep).replace(os.sep, "-")


def session_matches_repositories(
    session: SessionRecord, repositories: list[str]
) -> bool:
    if not repositories:
        return True
    cwd = _resolved(session.cwd) if session.cwd else ""
    for repository in repositories:
        root = _resolved(repository)
        if cwd == root or cwd.startswith(root + os.sep + ".worktrees" + os.sep):
            return True
    return False


def repository_identity(cwd: str) -> str:
    if not cwd:
        return "unknown"
    path = Path(cwd)
    if ".worktrees" in path.parts:
        index = path.parts.index(".worktrees")
        path = Path(*path.parts[:index])
    return str(path)


def discover_sessions(
    sessions_dir: Path, zone: ZoneInfo, repositories: list[str]
) -> tuple[list[SessionRecord], int]:
    sessions: list[SessionRecord] = []
    invalid = 0
    if not sessions_dir.is_dir():
        return sessions, invalid
    encoded_repositories = {
        encode_repo_path(repository)
        for repository in (*repositories, *map(_resolved, repositories))
    }
    directories = [sessions_dir]
    directories.extend(path for path in sessions_dir.iterdir() if path.is_dir())
    explicit_directories = {
        path
        for path in directories
        if any(
            path.name == encoded or path.name.startswith(encoded + "-.worktrees-")
            for encoded in encoded_repositories
        )
    }
    paths = (file for directory in directories for file in directory.glob("*.jsonl"))
    for path in sorted(paths):
        session = read_session(path, zone)
        if session is None:
            if not repositories or path.parent in explicit_directories:
                invalid += 1
            continue
        if session_matches_repositories(session, repositories):
            sessions.append(session)
    return sessions, invalid


def _inference_tokens(trajectory: Any, key: str) -> int:
    return sum(
        int((step.attributes or {}).get(key) or 0)
        for step in trajectory.steps
        if step.operation == "inference"
    )


def load_trajectory_rows(
    sessions: Iterable[SessionRecord], ccr: str
) -> tuple[list[dict[str, Any]], set[str]]:
    rows: list[dict[str, Any]] = []
    failures: set[str] = set()
    loader = ATIFTrajectoryLoader()
    for session in sessions:
        if not session.closed:
            continue
        try:
            result = subprocess.run(
                [ccr, "export", "--format", "atif", str(session.path)],
                capture_output=True,
                text=True,
                timeout=120,
                check=True,
            )
            trajectories = loader.loads(result.stdout, source=str(session.path))
            for trajectory in trajectories:
                signals = objective_signals(trajectory)
                metadata = trajectory.metadata or {}
                agent = metadata.get("agent") or {}
                rows.append(
                    {
                        "session_id": session.session_id,
                        "trajectory_id": trajectory.trajectory_id,
                        "execution_id": str(metadata.get("execution_id") or ""),
                        "unit": str(
                            metadata.get("file_path") or trajectory.trajectory_id
                        ),
                        "stage": signals["stage"],
                        "outcome": str(metadata.get("execution_outcome") or "unknown"),
                        "reason": str(metadata.get("execution_reason") or ""),
                        "score": signals["score"],
                        "rounds": signals["rounds"],
                        "duration_sec": signals["duration_sec"],
                        "prompt_tokens": _inference_tokens(trajectory, "prompt_tokens"),
                        "completion_tokens": _inference_tokens(
                            trajectory, "completion_tokens"
                        ),
                        "cached_tokens": _inference_tokens(trajectory, "cached_tokens"),
                        "tool_freq": signals["tool_freq"],
                        "model": str(agent.get("model_name") or session.model),
                        "tool_version": str(
                            metadata.get("tool_version") or session.tool_version
                        ),
                        "signals": signals,
                    }
                )
        except (
            OSError,
            subprocess.SubprocessError,
            json.JSONDecodeError,
            ValueError,
        ) as error:
            failures.add(session.session_id)
            print(
                f"weekly_report: failed to evaluate session {session.session_id}: {error}",
                file=sys.stderr,
            )
    return rows, failures


def unit_duration_records(rows: Iterable[dict[str, Any]]) -> list[dict[str, Any]]:
    """Project Review 1 trajectories into one timing record per Unit execution."""
    fields = (
        "session_id",
        "trajectory_id",
        "execution_id",
        "unit",
        "outcome",
        "reason",
        "duration_sec",
        "rounds",
        "prompt_tokens",
        "completion_tokens",
        "cached_tokens",
        "model",
        "tool_version",
    )
    records = [
        {field: row.get(field) for field in fields}
        for row in rows
        if row["stage"] == REVIEW1
    ]
    return sorted(
        records,
        key=lambda record: (
            -float(record["duration_sec"] or 0),
            str(record["session_id"] or ""),
            str(record["unit"] or ""),
        ),
    )


def _percentile(values: list[float], percentile: float) -> float | int | None:
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, math.ceil(percentile * len(ordered)) - 1)
    value = ordered[index]
    return int(value) if float(value).is_integer() else round(value, 3)


def distribution(values: list[float]) -> dict[str, float | int | None]:
    if not values:
        return {"total": 0, "average": None, "p50": None, "p95": None}
    total = sum(values)
    average = total / len(values)
    return {
        "total": int(total) if float(total).is_integer() else round(total, 3),
        "average": round(average, 3),
        "p50": _percentile(values, 0.50),
        "p95": _percentile(values, 0.95),
    }


def average_per(values: list[float], count: int) -> float | None:
    return round(sum(values) / count, 3) if count else None


def aggregate_stage(rows: list[dict[str, Any]], stage: str) -> dict[str, Any]:
    selected = [row for row in rows if row["stage"] == stage]
    outcomes = Counter(row["outcome"] for row in selected)
    scores = [float(row["score"]) for row in selected if row["score"] is not None]
    tools: Counter[str] = Counter()
    failure_events: Counter[tuple[str, str]] = Counter()
    failure_affected: dict[tuple[str, str], set[int]] = {}
    diagnostic_events: Counter[tuple[str, str]] = Counter()
    diagnostic_affected: dict[tuple[str, str], set[int]] = {}
    search_purposes: Counter[str] = Counter()
    search_calls = 0
    search_requests = 0
    for row_index, row in enumerate(selected):
        tools.update(row["tool_freq"])
        signals = row.get("signals") or {}
        code_searches = signals.get("code_searches") or {}
        search_calls += int(code_searches.get("calls") or 0)
        search_requests += int(code_searches.get("requests") or 0)
        search_purposes.update(
            {
                str(purpose): int(requests)
                for purpose, requests in (
                    code_searches.get("purpose_counts") or {}
                ).items()
            }
        )
        for failure in signals.get("failures") or []:
            key = (
                str(failure.get("impact") or "step"),
                str(failure.get("key") or "unknown.unknown.unknown"),
            )
            failure_events[key] += 1
            failure_affected.setdefault(key, set()).add(row_index)
        for evaluation in signals.get("evaluations") or []:
            for signal in evaluation.get("signals") or []:
                key = (
                    str(signal.get("severity") or "info"),
                    str(signal.get("code") or "unknown"),
                )
                diagnostic_events[key] += 1
                diagnostic_affected.setdefault(key, set()).add(row_index)
    count = len(selected)
    known_outcomes = count - outcomes["unknown"]
    assessments = sum(
        int((row.get("signals") or {}).get("assessment_count") or 0)
        for row in selected
    )
    durations = [float(row["duration_sec"]) for row in selected]
    prompt_tokens = [float(row["prompt_tokens"]) for row in selected]
    completion_tokens = [float(row["completion_tokens"]) for row in selected]
    cached_tokens = [float(row.get("cached_tokens", 0)) for row in selected]
    failure_items = [
        {
            "impact": impact,
            "failure": failure,
            "count": event_count,
            "affected_chains": len(failure_affected[(impact, failure)]),
            "rate": round(len(failure_affected[(impact, failure)]) / count, 3),
        }
        for (impact, failure), event_count in sorted(
            failure_events.items(),
            key=lambda item: (-len(failure_affected[item[0]]), item[0]),
        )
    ]
    execution_failure_rates = {
        item["failure"]: item["rate"]
        for item in failure_items
        if item["impact"] == "execution"
    }
    diagnostic_items = [
        {
            "severity": severity,
            "signal": signal,
            "count": event_count,
            "affected_chains": len(diagnostic_affected[(severity, signal)]),
            "rate": round(len(diagnostic_affected[(severity, signal)]) / count, 3),
        }
        for (severity, signal), event_count in sorted(
            diagnostic_events.items(),
            key=lambda item: (-len(diagnostic_affected[item[0]]), item[0]),
        )
    ]
    labeled_search_requests = sum(
        requests
        for purpose, requests in search_purposes.items()
        if purpose != "(unspecified)"
    )
    return {
        "chains": count,
        "outcomes": dict(sorted(outcomes.items())),
        "outcome_coverage": round(known_outcomes / count, 3) if count else None,
        "completion_rate": round(outcomes["completed"] / known_outcomes, 3)
        if known_outcomes
        else None,
        "workflow_timeout_rate": execution_failure_rates.get(
            "workflow.timeout", 0.0 if count else None
        ),
        "llm_routing_timeout_rate": execution_failure_rates.get(
            "llm.routing.timeout", 0.0 if count else None
        ),
        "average_score": round(sum(scores) / len(scores), 3) if scores else None,
        "assessments": assessments,
        "rounds": distribution([float(row["rounds"]) for row in selected]),
        "duration_sec": distribution(durations),
        "prompt_tokens": distribution(prompt_tokens),
        "completion_tokens": distribution(completion_tokens),
        "cached_tokens": distribution(cached_tokens),
        "per_assessment": {
            "duration_sec": average_per(durations, assessments),
            "prompt_tokens": average_per(prompt_tokens, assessments),
            "completion_tokens": average_per(completion_tokens, assessments),
            "cached_tokens": average_per(cached_tokens, assessments),
        },
        "tool_freq": dict(sorted(tools.items(), key=lambda item: (-item[1], item[0]))),
        "code_searches": {
            "calls": search_calls,
            "requests": search_requests,
            "purpose_counts": dict(
                sorted(search_purposes.items(), key=lambda item: (-item[1], item[0]))
            ),
            "purpose_coverage": round(
                labeled_search_requests / search_requests, 3
            )
            if search_requests
            else None,
        },
        "failures": {
            "events": sum(failure_events.values()),
            "items": failure_items,
        },
        "diagnostic_signals": {
            "events": sum(diagnostic_events.values()),
            "items": diagnostic_items,
        },
        "main_deductions": main_deductions([row["signals"] for row in selected]),
    }


def aggregate_sessions(sessions: list[SessionRecord]) -> dict[str, Any]:
    versions = Counter(session.tool_version for session in sessions)
    models = Counter(session.model for session in sessions)
    repositories = {repository_identity(session.cwd) for session in sessions}
    return {
        "total": len(sessions),
        "closed": sum(session.closed for session in sessions),
        "unclosed": sum(not session.closed for session in sessions),
        "repositories": len(repositories),
        "finding_events": sum(session.finding_count for session in sessions),
        "by_tool_version": dict(sorted(versions.items())),
        "by_model": dict(sorted(models.items())),
    }


def load_datasets(paths: list[Path]) -> tuple[list[dict[str, Any]], int, int]:
    records: dict[str, dict[str, Any]] = {}
    missing = 0
    invalid = 0
    for path in paths:
        if not path.is_file():
            missing += 1
            continue
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except OSError:
            missing += 1
            continue
        for line in lines:
            if not line.strip():
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                invalid += 1
                continue
            identity = str(
                record.get("id") or f"{record.get('source')}:{record.get('reply_id')}"
            )
            records[identity] = record
    return list(records.values()), missing, invalid


def _label_distribution(records: Iterable[dict[str, Any]]) -> dict[str, int]:
    return dict(
        sorted(
            Counter(str(record.get("label") or "unknown") for record in records).items()
        )
    )


def _wrong_tags(records: Iterable[dict[str, Any]]) -> dict[str, int]:
    tags: Counter[str] = Counter()
    for record in records:
        if record.get("label") != "wrong":
            continue
        values = record.get("tags") or ["untagged"]
        tags.update(str(value) for value in values)
    return dict(sorted(tags.items(), key=lambda item: (-item[1], item[0])))


def _label_rate(
    distribution: dict[str, int], labels: tuple[str, ...], total: int, available: bool
) -> float | None:
    if not available or not total:
        return None
    return round(sum(distribution.get(label, 0) for label in labels) / total, 3)


def quality_metrics(
    datasets: list[dict[str, Any]],
    week_sessions: list[SessionRecord],
    window: WeekWindow,
    scoped_session_ids: set[str],
    repositories_scoped: bool,
    dataset_status: str,
) -> dict[str, Any]:
    week_ids = {session.session_id for session in week_sessions}
    review_records = [
        record
        for record in datasets
        if str((record.get("engine") or {}).get("session_id") or "") in week_ids
    ]
    label_records = []
    labels_without_session = 0
    for record in datasets:
        labeled_at = parse_timestamp(
            record.get("at"), cast(ZoneInfo, window.start.tzinfo)
        )
        if labeled_at is None or not window.contains(labeled_at):
            continue
        session_id = str((record.get("engine") or {}).get("session_id") or "")
        if repositories_scoped and session_id not in scoped_session_ids:
            continue
        if not session_id:
            labels_without_session += 1
        label_records.append(record)

    finding_events = sum(session.finding_count for session in week_sessions)
    finding_records = [
        record for record in review_records if record.get("kind") == "finding"
    ]
    labeled_findings = len(finding_records)
    review_distribution = _label_distribution(finding_records)
    label_distribution = _label_distribution(label_records)
    quality_available = dataset_status == "ready"
    return {
        "label_dataset": {
            "status": dataset_status,
            "records": len(datasets),
        },
        "review_week": {
            "examples": len(review_records) if quality_available else None,
            "labeled_findings": labeled_findings if quality_available else None,
            "by_label": review_distribution if quality_available else {},
            "wrong_tags": _wrong_tags(review_records) if quality_available else {},
            "label_coverage": round(labeled_findings / finding_events, 3)
            if quality_available and finding_events
            else None,
            "accepted_rate": _label_rate(
                review_distribution,
                ("important", "minor"),
                labeled_findings,
                quality_available,
            ),
            "wrong_rate": _label_rate(
                review_distribution,
                ("wrong",),
                labeled_findings,
                quality_available,
            ),
            "repeat_rate": _label_rate(
                review_distribution,
                ("repeat",),
                labeled_findings,
                quality_available,
            ),
            "debatable_rate": _label_rate(
                review_distribution,
                ("debatable",),
                labeled_findings,
                quality_available,
            ),
            # A missed count alone has no denominator. Recall needs a review-level
            # marker that says the human ground truth is exhaustive.
            "recall_rate": None,
        },
        "labeled_this_week": {
            "examples": len(label_records) if quality_available else None,
            "by_label": label_distribution if quality_available else {},
            "wrong_tags": _wrong_tags(label_records) if quality_available else {},
            "without_session": labels_without_session if quality_available else None,
            "missed_findings_reported": label_distribution.get("missed", 0)
            if quality_available
            else None,
        },
    }


def build_week_metrics(
    window: WeekWindow,
    all_sessions: list[SessionRecord],
    rows: list[dict[str, Any]],
    failed_session_ids: set[str],
    datasets: list[dict[str, Any]],
    invalid_session_files: int,
    missing_datasets: int,
    invalid_dataset_lines: int,
    repositories_scoped: bool,
) -> dict[str, Any]:
    sessions = [
        session for session in all_sessions if window.contains(session.started_at)
    ]
    session_ids = {session.session_id for session in sessions}
    week_rows = [row for row in rows if row["session_id"] in session_ids]
    scoped_session_ids = {session.session_id for session in all_sessions}
    dataset_status = (
        "missing"
        if missing_datasets
        else "invalid"
        if invalid_dataset_lines
        else "ready"
    )
    return {
        "week": window.key,
        "window": {
            "start": window.start.isoformat(),
            "end": window.end.isoformat(),
            "timezone": str(window.start.tzinfo),
        },
        "sessions": aggregate_sessions(sessions),
        REVIEW1: aggregate_stage(week_rows, REVIEW1),
        REVIEW2: aggregate_stage(week_rows, REVIEW2),
        UNKNOWN_STAGE: aggregate_stage(week_rows, UNKNOWN_STAGE),
        "quality": quality_metrics(
            datasets,
            sessions,
            window,
            scoped_session_ids,
            repositories_scoped,
            dataset_status,
        ),
        "data_quality": {
            "invalid_session_files_in_scan": invalid_session_files,
            "trajectory_export_failures": len(failed_session_ids & session_ids),
            "missing_dataset_files": missing_datasets,
            "invalid_dataset_lines": invalid_dataset_lines,
        },
    }


COMPARISON_METRICS = (
    ("Sessions", ("sessions", "total"), "number"),
    ("Finding events", ("sessions", "finding_events"), "number"),
    ("Review 1 chains", (REVIEW1, "chains"), "number"),
    ("Review 1 outcome coverage", (REVIEW1, "outcome_coverage"), "percent"),
    ("Review 1 completion", (REVIEW1, "completion_rate"), "percent"),
    (
        "Review 1 workflow timeout",
        (REVIEW1, "workflow_timeout_rate"),
        "percent",
    ),
    (
        "Review 1 llm.routing.timeout",
        (REVIEW1, "llm_routing_timeout_rate"),
        "percent",
    ),
    ("Review 1 score", (REVIEW1, "average_score"), "score"),
    (
        "Review 1 average duration (sec)",
        (REVIEW1, "duration_sec", "average"),
        "number",
    ),
    ("Review 1 p50 duration (sec)", (REVIEW1, "duration_sec", "p50"), "number"),
    ("Review 1 p95 duration (sec)", (REVIEW1, "duration_sec", "p95"), "number"),
    ("Review 1 prompt/chain", (REVIEW1, "prompt_tokens", "average"), "number"),
    ("Review 1 search requests", (REVIEW1, "code_searches", "requests"), "number"),
    (
        "Review 1 search purpose coverage",
        (REVIEW1, "code_searches", "purpose_coverage"),
        "percent",
    ),
    ("Review 2 chains", (REVIEW2, "chains"), "number"),
    ("Review 2 assessments", (REVIEW2, "assessments"), "number"),
    ("Review 2 outcome coverage", (REVIEW2, "outcome_coverage"), "percent"),
    ("Review 2 completion", (REVIEW2, "completion_rate"), "percent"),
    (
        "Review 2 workflow timeout",
        (REVIEW2, "workflow_timeout_rate"),
        "percent",
    ),
    (
        "Review 2 llm.routing.timeout",
        (REVIEW2, "llm_routing_timeout_rate"),
        "percent",
    ),
    ("Review 2 score", (REVIEW2, "average_score"), "score"),
    (
        "Review 2 average duration/Lane (sec)",
        (REVIEW2, "duration_sec", "average"),
        "number",
    ),
    (
        "Review 2 average duration/Assessment (sec)",
        (REVIEW2, "per_assessment", "duration_sec"),
        "number",
    ),
    ("Review 2 p50 duration (sec)", (REVIEW2, "duration_sec", "p50"), "number"),
    ("Review 2 p95 duration (sec)", (REVIEW2, "duration_sec", "p95"), "number"),
    ("Review 2 prompt/Lane", (REVIEW2, "prompt_tokens", "average"), "number"),
    (
        "Review 2 prompt/Assessment",
        (REVIEW2, "per_assessment", "prompt_tokens"),
        "number",
    ),
    ("Review 2 search requests", (REVIEW2, "code_searches", "requests"), "number"),
    (
        "Review 2 search purpose coverage",
        (REVIEW2, "code_searches", "purpose_coverage"),
        "percent",
    ),
    (
        "Review-week labeled findings",
        ("quality", "review_week", "labeled_findings"),
        "number",
    ),
    (
        "Review-week label coverage",
        ("quality", "review_week", "label_coverage"),
        "percent",
    ),
    (
        "Review-week accepted findings",
        ("quality", "review_week", "accepted_rate"),
        "percent",
    ),
    (
        "Review-week wrong findings",
        ("quality", "review_week", "wrong_rate"),
        "percent",
    ),
    (
        "Review-week repeat findings",
        ("quality", "review_week", "repeat_rate"),
        "percent",
    ),
    (
        "Review-week debatable findings",
        ("quality", "review_week", "debatable_rate"),
        "percent",
    ),
    ("Labels added", ("quality", "labeled_this_week", "examples"), "number"),
    (
        "Missed findings reported",
        ("quality", "labeled_this_week", "missed_findings_reported"),
        "number",
    ),
)


def _nested(data: dict[str, Any], path: tuple[str, ...]) -> Any:
    value: Any = data
    for key in path:
        if not isinstance(value, dict):
            return None
        value = value.get(key)
    return value


def build_comparison(
    current: dict[str, Any], previous: dict[str, Any]
) -> list[dict[str, Any]]:
    comparison = []
    for label, path, kind in COMPARISON_METRICS:
        current_value = _nested(current, path)
        previous_value = _nested(previous, path)
        delta = None
        change_pct = None
        if current_value is not None and previous_value is not None:
            delta = round(float(current_value) - float(previous_value), 3)
            if previous_value:
                change_pct = round(delta / float(previous_value), 3)
        comparison.append(
            {
                "metric": label,
                "kind": kind,
                "current": current_value,
                "previous": previous_value,
                "delta": delta,
                "change_pct": change_pct,
            }
        )
    return comparison


def _write_text(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(content, encoding="utf-8")
    temporary.replace(path)


def _write_json(path: Path, value: Any) -> None:
    _write_text(
        path, json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    )


def _write_jsonl(path: Path, values: Iterable[dict[str, Any]]) -> None:
    _write_text(
        path,
        "".join(
            json.dumps(value, ensure_ascii=False, sort_keys=True) + "\n"
            for value in values
        ),
    )


def write_report(
    out_root: Path,
    current: dict[str, Any],
    previous: dict[str, Any],
    comparison: list[dict[str, Any]],
    manifest: dict[str, Any],
    unit_durations: list[dict[str, Any]],
) -> Path:
    report_dir = out_root / current["week"]
    report_dir.mkdir(parents=True, exist_ok=True)
    _write_text(
        report_dir / "REPORT.md",
        render_markdown(current, previous, comparison, unit_durations),
    )
    _write_json(
        report_dir / "metrics.json",
        {
            "schema_version": REPORT_SCHEMA_VERSION,
            "current": current,
            "previous": previous,
            "comparison": comparison,
        },
    )
    _write_json(report_dir / "manifest.json", manifest)
    _write_jsonl(report_dir / "unit-durations.jsonl", unit_durations)
    return report_dir


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--week", help="ISO week YYYY-Www; defaults to previous complete week"
    )
    parser.add_argument("--timezone", default="Asia/Shanghai", help="IANA timezone")
    parser.add_argument(
        "--repo",
        action="append",
        default=[],
        help="limit to one repository path; repeatable (default: all repositories)",
    )
    parser.add_argument(
        "--sessions-dir",
        type=Path,
        default=Path.home() / ".casecodereview" / "sessions",
    )
    parser.add_argument(
        "--dataset",
        action="append",
        type=Path,
        help="normalized label dataset; repeatable",
    )
    parser.add_argument(
        "--out-root",
        type=Path,
        default=Path("eval/data/reports/weekly"),
    )
    parser.add_argument("--ccr", default="ccr", help="ccr executable")
    args = parser.parse_args()

    try:
        zone = ZoneInfo(args.timezone)
        current_window = (
            WeekWindow.from_key(args.week, zone)
            if args.week
            else WeekWindow.previous_complete(zone)
        )
    except (ValueError, ZoneInfoNotFoundError) as error:
        parser.error(str(error))
    previous_window = current_window.previous()

    sessions, invalid_sessions = discover_sessions(args.sessions_dir, zone, args.repo)
    selected_sessions = [
        session
        for session in sessions
        if current_window.contains(session.started_at)
        or previous_window.contains(session.started_at)
    ]
    rows, failed_sessions = load_trajectory_rows(selected_sessions, args.ccr)
    dataset_paths = args.dataset or list(DEFAULT_DATASETS)
    datasets, missing_datasets, invalid_dataset_lines = load_datasets(dataset_paths)

    metric_args = {
        "all_sessions": sessions,
        "rows": rows,
        "failed_session_ids": failed_sessions,
        "datasets": datasets,
        "invalid_session_files": invalid_sessions,
        "missing_datasets": missing_datasets,
        "invalid_dataset_lines": invalid_dataset_lines,
        "repositories_scoped": bool(args.repo),
    }
    current = build_week_metrics(current_window, **metric_args)
    previous = build_week_metrics(previous_window, **metric_args)
    comparison = build_comparison(current, previous)
    current_session_ids = {
        session.session_id
        for session in sessions
        if current_window.contains(session.started_at)
    }
    unit_durations = unit_duration_records(
        row for row in rows if row["session_id"] in current_session_ids
    )
    generated_at = datetime.now(timezone.utc).isoformat()
    manifest = {
        "schema_version": REPORT_SCHEMA_VERSION,
        "week": current_window.key,
        "generated_at": generated_at,
        "timezone": args.timezone,
        "repositories": [_resolved(path) for path in args.repo] or ["*"],
        "sessions_dir": str(args.sessions_dir),
        "datasets": [str(path) for path in dataset_paths],
        "ccr": args.ccr,
        "session_files_scanned": len(sessions) + invalid_sessions,
        "session_files_valid": len(sessions),
        "session_files_invalid": invalid_sessions,
        "session_files_in_comparison": len(selected_sessions),
        "artifacts": [
            "REPORT.md",
            "metrics.json",
            "manifest.json",
            "unit-durations.jsonl",
        ],
    }
    report_dir = write_report(
        args.out_root,
        current,
        previous,
        comparison,
        manifest,
        unit_durations,
    )
    print(
        json.dumps(
            {
                "week": current_window.key,
                "sessions": current["sessions"]["total"],
                "review1_chains": current[REVIEW1]["chains"],
                "review2_chains": current[REVIEW2]["chains"],
                "label_dataset_status": current["quality"]["label_dataset"]["status"],
                "report": str(report_dir / "REPORT.md"),
                "unit_durations": str(report_dir / "unit-durations.jsonl"),
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Build one reproducible weekly CCR eval report and compare it with last week.

Session JSONL and normalized label datasets remain the sources of truth. This
script only materializes a weekly read model under
``eval/data/reports/weekly/<YYYY-Www>/``.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
from collections import Counter
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable, cast
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from eval.trajectory.ccr_source import CCRSessionSource
from eval.trajectory.ccr_trajectory import REVIEW1, REVIEW2, UNKNOWN_STAGE
from eval.trajectory.ccr_trajectory_report import run_weekly_report as run_trajectory_report
from eval.eval_snapshot import artifacts_match
from trajectory_harness.model import (
    trajectory_execution,
    trajectory_metadata,
    trajectory_recording_id,
)
from trajectory_harness import TrajectoryRunArtifact
from eval.trajectory.trajectory_judge import objective_analysis_from_run
from eval.weekly_report_metrics import aggregate_cohorts, aggregate_stage
from eval.weekly_report_render import render_markdown
from eval.weekly_window import WeekWindow, default_dataset_paths

DEFAULT_GITHUB_LABEL_MANIFEST = Path("eval/data/labels/github-harvest.json")
DEFAULT_LABEL_DATASET_MANIFEST = Path("eval/data/datasets/label-dataset.json")
REPORT_SCHEMA_VERSION = "weekly-report-v12"


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


def default_github_label_manifest(repo_root: Path | None = None) -> Path:
    """Resolve the ignored harvest manifest from the main worktree when needed."""

    root = (repo_root or Path.cwd()).resolve()
    local = root / DEFAULT_GITHUB_LABEL_MANIFEST
    if local.is_file():
        return local
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
            cwd=root,
            capture_output=True,
            text=True,
            timeout=5,
            check=True,
        )
    except (OSError, subprocess.SubprocessError):
        return local
    shared = (
        Path(result.stdout.strip()).resolve().parent / DEFAULT_GITHUB_LABEL_MANIFEST
    )
    return shared if shared.is_file() else local


def default_label_dataset_manifest(repo_root: Path | None = None) -> Path:
    """Resolve the ignored dataset manifest from the main worktree when needed."""

    root = (repo_root or Path.cwd()).resolve()
    local = root / DEFAULT_LABEL_DATASET_MANIFEST
    if local.is_file():
        return local
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
            cwd=root,
            capture_output=True,
            text=True,
            timeout=5,
            check=True,
        )
    except (OSError, subprocess.SubprocessError):
        return local
    shared = (
        Path(result.stdout.strip()).resolve().parent / DEFAULT_LABEL_DATASET_MANIFEST
    )
    return shared if shared.is_file() else local


def load_json_object(path: Path) -> tuple[dict[str, Any] | None, bool]:
    if not path.is_file():
        return None, False
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None, True
    return (payload, False) if isinstance(payload, dict) else (None, True)


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


def load_trajectory_rows(
    artifacts: Iterable[TrajectoryRunArtifact],
    sessions: Iterable[SessionRecord],
) -> tuple[list[dict[str, Any]], set[str]]:
    """Project persisted trajectory runs into the detailed weekly read model."""

    rows: list[dict[str, Any]] = []
    failures: set[str] = set()
    sessions_by_id = {session.session_id: session for session in sessions}
    for artifact in artifacts:
        failures.update(issue.recording_id for issue in artifact.build.summary.issues)
        for trajectory in artifact.dataset.trajectories:
            analysis = objective_analysis_from_run(trajectory, artifact.run)
            usage_result = analysis["model_usage"]
            usage = usage_result.get("measurements") or {}
            metadata = trajectory_metadata(trajectory) or {}
            agent = metadata.get("agent") or {}
            session_id = str(
                trajectory_recording_id(trajectory) or metadata.get("session_id") or ""
            )
            session = sessions_by_id.get(session_id)
            repository = (
                repository_identity(session.cwd)
                if session is not None
                else repository_identity(str(metadata.get("repo") or ""))
            )
            outcome = (
                trajectory_execution(trajectory).outcome
                if trajectory_execution(trajectory) is not None
                else str(metadata.get("execution_outcome") or "unknown")
            )
            rows.append(
                {
                    "session_id": session_id,
                    "trajectory_id": trajectory.trajectory_id,
                    "execution_id": str(metadata.get("execution_id") or ""),
                    "unit": str(
                        metadata.get("file_path")
                        or metadata.get("ccr_scope_id")
                        or trajectory.trajectory_id
                    ),
                    "stage": analysis["stage"],
                    "outcome": outcome,
                    "reason": str(metadata.get("execution_reason") or ""),
                    "score": analysis["score"],
                    "rounds": analysis["rounds"],
                    "duration_sec": analysis["duration_sec"],
                    "prompt_tokens": int(usage.get("input_tokens") or 0),
                    "completion_tokens": int(usage.get("output_tokens") or 0),
                    "cached_tokens": int(usage.get("cached_input_tokens") or 0),
                    "uncached_tokens": int(usage.get("uncached_input_tokens") or 0),
                    "total_tokens": int(usage.get("total_tokens") or 0),
                    "model_calls": int(usage.get("model_call_count") or 0),
                    "usage_reported_calls": int(
                        usage.get("usage_reported_call_count") or 0
                    ),
                    "usage_coverage": usage.get("usage_coverage_ratio"),
                    "usage_status": usage_result.get("status"),
                    "tool_freq": analysis["tool_freq"],
                    "model": str(
                        agent.get("model_name")
                        or (session.model if session is not None else "unknown")
                    ),
                    "tool_version": str(
                        metadata.get("tool_version")
                        or (
                            session.tool_version
                            if session is not None
                            else "unknown"
                        )
                    ),
                    "repository": repository,
                    "analysis": analysis,
                }
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
    accepted_findings = sum(
        review_distribution.get(label, 0) for label in ("important", "minor")
    )
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
            "accepted_findings": accepted_findings if quality_available else None,
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


def github_label_sync_metrics(
    dataset_paths: list[Path],
    window: WeekWindow,
    harvest_manifest: dict[str, Any] | None,
    harvest_manifest_invalid: bool,
    dataset_manifest: dict[str, Any] | None,
    dataset_manifest_invalid: bool,
) -> dict[str, Any]:
    """Prove that one normalized dataset consumed a complete harvest snapshot."""

    result = {
        "status": "missing",
        "harvest_snapshot_id": None,
        "dataset_harvest_snapshot_id": None,
        "dataset_current": None,
        "artifacts_current": None,
        "dataset_unpaired": None,
        "generated_at": None,
        "pull_requests_discovered": None,
        "pull_requests_harvested": None,
        "pull_requests_failed": None,
        "harvest_coverage": None,
        "covers_report_window": None,
    }
    if harvest_manifest_invalid or dataset_manifest_invalid:
        return {**result, "status": "invalid"}
    if harvest_manifest is None or dataset_manifest is None:
        return result
    if (
        harvest_manifest.get("schema_version") != "github-label-harvest-v2"
        or dataset_manifest.get("schema_version") != "label-dataset-v1"
    ):
        return {**result, "status": "invalid"}

    dataset_inputs = dataset_manifest.get("inputs")
    dataset_artifact_manifest = dataset_manifest.get("artifacts")
    dataset_stats = dataset_manifest.get("stats")
    if (
        not isinstance(dataset_inputs, dict)
        or not isinstance(dataset_artifact_manifest, list)
        or not all(
            isinstance(artifact, dict) for artifact in dataset_artifact_manifest
        )
        or not isinstance(dataset_stats, dict)
    ):
        return {**result, "status": "invalid"}

    discovered = int(harvest_manifest.get("pull_requests_discovered") or 0)
    harvested = int(harvest_manifest.get("pull_requests_harvested") or 0)
    failed = int(harvest_manifest.get("pull_requests_failed") or 0)
    harvest_snapshot_id = str(harvest_manifest.get("snapshot_id") or "")
    dataset_harvest_snapshot_id = str(
        dataset_inputs.get("github_harvest_snapshot_id") or ""
    )
    artifacts_current = artifacts_match(dataset_paths, dataset_artifact_manifest)
    dataset_current = bool(
        harvest_snapshot_id
        and dataset_harvest_snapshot_id == harvest_snapshot_id
        and artifacts_current
    )
    dataset_unpaired = int(dataset_stats.get("unpaired") or 0)
    query = harvest_manifest.get("query") or {}
    covers_window = None
    try:
        query_start = datetime.fromisoformat(
            str(query["start"]).replace("Z", "+00:00")
        )
        query_end = datetime.fromisoformat(str(query["end"]).replace("Z", "+00:00"))
        if query_start.tzinfo is None or query_end.tzinfo is None:
            raise ValueError("harvest window must include timezone")
        covers_window = query_start <= window.start and query_end >= window.end
    except (KeyError, TypeError, ValueError):
        pass

    status = "ready"
    if failed or harvested < discovered:
        status = "partial"
    elif covers_window is not True:
        status = "window_mismatch"
    elif not dataset_current:
        status = "dataset_stale" if artifacts_current else "dataset_changed"
    return {
        "status": status,
        "harvest_snapshot_id": harvest_snapshot_id or None,
        "dataset_harvest_snapshot_id": dataset_harvest_snapshot_id or None,
        "dataset_current": dataset_current,
        "artifacts_current": artifacts_current,
        "dataset_unpaired": dataset_unpaired,
        "generated_at": harvest_manifest.get("generated_at"),
        "pull_requests_discovered": discovered,
        "pull_requests_harvested": harvested,
        "pull_requests_failed": failed,
        "harvest_coverage": round(harvested / discovered, 3) if discovered else 1.0,
        "covers_report_window": covers_window,
    }


def cost_effect_metrics(
    rows: list[dict[str, Any]], quality: dict[str, Any]
) -> dict[str, Any]:
    """Join factual model usage with human-accepted review findings."""

    total_tokens = sum(int(row.get("total_tokens") or 0) for row in rows)
    model_calls = sum(int(row.get("model_calls") or 0) for row in rows)
    usage_reported_calls = sum(
        int(row.get("usage_reported_calls") or 0) for row in rows
    )
    accepted = quality["review_week"]["accepted_findings"]
    return {
        "total_tokens": total_tokens,
        "input_tokens": sum(int(row.get("prompt_tokens") or 0) for row in rows),
        "output_tokens": sum(int(row.get("completion_tokens") or 0) for row in rows),
        "cached_input_tokens": sum(int(row.get("cached_tokens") or 0) for row in rows),
        "uncached_input_tokens": sum(
            int(row.get("uncached_tokens") or 0) for row in rows
        ),
        "model_calls": model_calls,
        "usage_coverage": round(usage_reported_calls / model_calls, 3)
        if model_calls
        else None,
        "labeled_accepted_findings": accepted,
        "tokens_per_labeled_accepted_finding": round(total_tokens / accepted, 3)
        if accepted
        else None,
        "label_coverage": quality["review_week"]["label_coverage"],
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
    github_label_manifest: dict[str, Any] | None = None,
    github_label_manifest_invalid: bool = False,
    dataset_paths: list[Path] | None = None,
    label_dataset_manifest: dict[str, Any] | None = None,
    label_dataset_manifest_invalid: bool = False,
) -> dict[str, Any]:
    sessions = [
        session for session in all_sessions if window.contains(session.started_at)
    ]
    session_ids = {session.session_id for session in sessions}
    week_rows = [row for row in rows if row["session_id"] in session_ids]
    scoped_session_ids = {session.session_id for session in all_sessions}
    file_status = (
        "missing"
        if missing_datasets
        else "invalid"
        if invalid_dataset_lines
        else "ready"
    )
    label_sync = github_label_sync_metrics(
        dataset_paths or [],
        window,
        github_label_manifest,
        github_label_manifest_invalid,
        label_dataset_manifest,
        label_dataset_manifest_invalid,
    )
    dataset_status = (
        file_status if file_status != "ready" else str(label_sync["status"])
    )
    quality = quality_metrics(
        datasets,
        sessions,
        window,
        scoped_session_ids,
        repositories_scoped,
        dataset_status,
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
        "cohorts": aggregate_cohorts(week_rows),
        "quality": quality,
        "label_sync": label_sync,
        "cost_effect": cost_effect_metrics(week_rows, quality),
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
    ("Total model tokens", ("cost_effect", "total_tokens"), "number"),
    (
        "Tokens/labeled accepted Finding",
        ("cost_effect", "tokens_per_labeled_accepted_finding"),
        "number",
    ),
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
        "Review 1 search context projection",
        (REVIEW1, "code_searches", "context_projection_rate"),
        "percent",
    ),
    (
        "Review 1 search follow-up reads",
        (REVIEW1, "search_follow_up", "follow_up_read_rate"),
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
        "Review 2 search context projection",
        (REVIEW2, "code_searches", "context_projection_rate"),
        "percent",
    ),
    (
        "Review 2 search follow-up reads",
        (REVIEW2, "search_follow_up", "follow_up_read_rate"),
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
        "--github-label-manifest",
        type=Path,
        help="GitHub bulk-harvest manifest; defaults to eval/data/labels/github-harvest.json",
    )
    parser.add_argument(
        "--label-dataset-manifest",
        type=Path,
        help=(
            "normalized label dataset manifest; defaults to "
            "eval/data/datasets/label-dataset.json"
        ),
    )
    parser.add_argument(
        "--out-root",
        type=Path,
        default=Path("eval/data/reports/weekly"),
    )
    parser.add_argument(
        "--trajectory-runs-root",
        type=Path,
        default=Path("eval/data/reports/trajectory"),
        help="canonical trajectory-harness run root",
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
    dataset_paths = args.dataset or default_dataset_paths()
    source = CCRSessionSource(
        args.sessions_dir,
        repositories=args.repo,
        ccr_command=args.ccr,
    )
    previous_trajectory = run_trajectory_report(
        window=previous_window,
        label_paths=dataset_paths,
        source=source,
        runs_dir=args.trajectory_runs_root,
        include_previous=False,
    )
    current_trajectory = run_trajectory_report(
        window=current_window,
        label_paths=dataset_paths,
        source=source,
        runs_dir=args.trajectory_runs_root,
        include_previous=True,
    )
    rows, failed_sessions = load_trajectory_rows(
        (previous_trajectory.artifact, current_trajectory.artifact),
        selected_sessions,
    )
    datasets, missing_datasets, invalid_dataset_lines = load_datasets(dataset_paths)
    github_label_manifest_path = (
        args.github_label_manifest or default_github_label_manifest()
    )
    github_label_manifest, github_label_manifest_invalid = load_json_object(
        github_label_manifest_path
    )
    label_dataset_manifest_path = (
        args.label_dataset_manifest or default_label_dataset_manifest()
    )
    label_dataset_manifest, label_dataset_manifest_invalid = load_json_object(
        label_dataset_manifest_path
    )

    metric_args = {
        "all_sessions": sessions,
        "rows": rows,
        "failed_session_ids": failed_sessions,
        "datasets": datasets,
        "invalid_session_files": invalid_sessions,
        "missing_datasets": missing_datasets,
        "invalid_dataset_lines": invalid_dataset_lines,
        "repositories_scoped": bool(args.repo),
        "github_label_manifest": github_label_manifest,
        "github_label_manifest_invalid": github_label_manifest_invalid,
        "dataset_paths": dataset_paths,
        "label_dataset_manifest": label_dataset_manifest,
        "label_dataset_manifest_invalid": label_dataset_manifest_invalid,
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
        "github_label_manifest": str(github_label_manifest_path),
        "label_dataset_manifest": str(label_dataset_manifest_path),
        "ccr": args.ccr,
        "trajectory_runs": {
            current_window.key: str(current_trajectory.run_dir),
            previous_window.key: str(previous_trajectory.run_dir),
        },
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

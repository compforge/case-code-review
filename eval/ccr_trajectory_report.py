#!/usr/bin/env python3
"""Build CCR's versioned trajectory dataset, verify it, and render HTML."""

from __future__ import annotations

import argparse
import json
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Sequence
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from build_trajectory_dataset import CCRTrajectoryDatasetBuilder
from ccr_source import CCRSessionSource
from ccr_trajectory import detectors_for_stage, verifiers_for_stage, review_stage
from harness_common.report_kit import KV, Report, Section, Table
from trajectory_harness import (
    ContextUsageMeasurer,
    Detector,
    Verifier,
    ModelUsageMeasurer,
    RecordingQuery,
    RecordingSource,
    Trajectory,
    TrajectoryDataset,
    TrajectoryAnalysisRunner,
    TrajectoryHarness,
    TrajectoryHarnessResult,
    TrajectoryReportBuilder,
    TrajectoryRunArtifact,
    ToolUsageMeasurer,
)
from weekly_window import WeekWindow, default_dataset_paths


class CCRTrajectoryAnalysisRunner(TrajectoryAnalysisRunner):
    """Evaluate CCR scopes with stage-specific checks and common cost measures."""

    def __init__(self) -> None:
        super().__init__(
            measurers=(
                ModelUsageMeasurer(),
                ToolUsageMeasurer(),
                ContextUsageMeasurer(),
            )
        )

    def target_for(self, trajectory: Trajectory, dataset: TrajectoryDataset) -> str:
        del dataset
        return review_stage(trajectory)

    def verifiers_for(
        self, target: str, dataset: TrajectoryDataset
    ) -> Sequence[Verifier]:
        del dataset
        return verifiers_for_stage(target)

    def detectors_for(
        self, target: str, dataset: TrajectoryDataset
    ) -> Sequence[Detector]:
        del dataset
        return detectors_for_stage(target)


class CCRTrajectoryReportBuilder(TrajectoryReportBuilder):
    """Project the canonical artifacts into a compact weekly CCR summary."""

    report_title = "CCR weekly trajectory eval"

    def build(
        self,
        current: TrajectoryRunArtifact,
        *,
        history: Sequence[TrajectoryRunArtifact] = (),
    ) -> Report:
        previous = max(history, key=lambda item: item.run.created_at, default=None)
        return Report(
            title=self.report_title,
            meta=[
                ("Week", current.dataset.version),
                (
                    "Compared with",
                    previous.dataset.version if previous is not None else "—",
                ),
            ],
            sections=[
                _overview_section(current, previous),
                _label_section(current, previous),
                _cost_and_latency_section(current, previous),
                _trajectory_findings_section(current),
                _data_health_section(current),
            ],
        )


def _overview_section(
    current: TrajectoryRunArtifact,
    previous: TrajectoryRunArtifact | None,
) -> Section:
    current_labels = _label_counts(current)
    previous_labels = _label_counts(previous)
    current_total = sum(current_labels.values())
    previous_total = sum(previous_labels.values())
    current_accepted = sum(current_labels[label] for label in ("important", "minor"))
    previous_accepted = sum(previous_labels[label] for label in ("important", "minor"))
    return Section(
        heading="Weekly overview",
        blocks=[
            KV(
                items=[
                    (
                        "Accepted share among labeled findings",
                        _summary_value(
                            _rate(current_accepted, current_total),
                            _rate(previous_accepted, previous_total),
                            "percent",
                        ),
                    ),
                    (
                        "Wrong share among labeled findings",
                        _summary_value(
                            _rate(current_labels["wrong"], current_total),
                            _rate(previous_labels["wrong"], previous_total),
                            "percent",
                        ),
                    ),
                    (
                        "Total model tokens",
                        _summary_value(
                            _total_tokens(current),
                            _total_tokens(previous),
                            "number",
                        ),
                    ),
                    (
                        "Review 1 average duration",
                        _summary_value(
                            _duration_seconds(current, "review1", "mean"),
                            _duration_seconds(previous, "review1", "mean"),
                            "seconds",
                        ),
                    ),
                    (
                        "Review 2 average duration",
                        _summary_value(
                            _duration_seconds(current, "review2", "mean"),
                            _duration_seconds(previous, "review2", "mean"),
                            "seconds",
                        ),
                    ),
                ]
            )
        ],
    )


def _label_section(
    current: TrajectoryRunArtifact,
    previous: TrajectoryRunArtifact | None,
) -> Section:
    current_labels = _label_counts(current)
    previous_labels = _label_counts(previous)
    current_total = sum(current_labels.values())
    previous_total = sum(previous_labels.values())
    labels = sorted(
        set(current_labels) | set(previous_labels),
        key=lambda label: (
            ("important", "minor", "debatable", "wrong", "repeat").index(label)
            if label in ("important", "minor", "debatable", "wrong", "repeat")
            else 5,
            label,
        ),
    )
    rows = []
    for label in labels:
        current_rate = _rate(current_labels[label], current_total)
        previous_rate = _rate(previous_labels[label], previous_total)
        rows.append(
            [
                label,
                str(current_labels[label]),
                _format_value(current_rate, "percent"),
                str(previous_labels[label]) if previous is not None else "—",
                _format_value(previous_rate, "percent"),
                _format_delta(current_rate, previous_rate, "percent"),
            ]
        )
    return Section(
        heading="Labeled finding quality",
        blocks=[
            Table(
                columns=[
                    "Label",
                    f"{current.dataset.version} count",
                    f"{current.dataset.version} share",
                    f"{previous.dataset.version if previous else 'Previous'} count",
                    f"{previous.dataset.version if previous else 'Previous'} share",
                    "Change",
                ],
                rows=rows,
            )
        ],
    )


def _cost_and_latency_section(
    current: TrajectoryRunArtifact,
    previous: TrajectoryRunArtifact | None,
) -> Section:
    rows = []
    for target, title in (("review1", "Review 1"), ("review2", "Review 2")):
        rows.extend(
            _comparison_rows(
                current,
                previous,
                target,
                title,
            )
        )
    return Section(
        heading="Cost and latency",
        blocks=[
            Table(
                columns=[
                    "Metric",
                    current.dataset.version,
                    previous.dataset.version if previous else "Previous",
                    "Change",
                ],
                rows=rows,
            )
        ],
    )


def _comparison_rows(
    current: TrajectoryRunArtifact,
    previous: TrajectoryRunArtifact | None,
    target: str,
    title: str,
) -> list[list[str]]:
    values = (
        (
            f"{title} trajectories",
            _trajectory_count(current, target),
            _trajectory_count(previous, target),
            "number",
        ),
        (
            f"{title} total tokens",
            _model_usage(current, target, "total_tokens", "sum"),
            _model_usage(previous, target, "total_tokens", "sum"),
            "number",
        ),
        (
            f"{title} average tokens / trajectory",
            _model_usage(current, target, "total_tokens", "mean"),
            _model_usage(previous, target, "total_tokens", "mean"),
            "number",
        ),
        (
            f"{title} average tool calls / trajectory",
            _measurement(
                current,
                target,
                "tool_usage",
                "tool_call_count",
                "mean",
            ),
            _measurement(
                previous,
                target,
                "tool_usage",
                "tool_call_count",
                "mean",
            ),
            "number",
        ),
        (
            f"{title} average peak input tokens",
            _measurement(
                current,
                target,
                "context_usage",
                "peak_input_tokens",
                "mean",
            ),
            _measurement(
                previous,
                target,
                "context_usage",
                "peak_input_tokens",
                "mean",
            ),
            "number",
        ),
        (
            f"{title} average duration",
            _duration_seconds(current, target, "mean"),
            _duration_seconds(previous, target, "mean"),
            "seconds",
        ),
        (
            f"{title} p95 duration",
            _duration_seconds(current, target, "p95"),
            _duration_seconds(previous, target, "p95"),
            "seconds",
        ),
    )
    return [
        [
            label,
            _format_value(current_value, kind),
            _format_value(previous_value, kind),
            _format_delta(current_value, previous_value, kind),
        ]
        for label, current_value, previous_value, kind in values
    ]


def _trajectory_findings_section(current: TrajectoryRunArtifact) -> Section:
    rows = []
    for metric in current.run.metrics:
        if metric.name != "finding" or metric.aggregation != "count":
            continue
        dimensions = dict(metric.dimensions)
        rate = _metric_value(
            current,
            name="finding",
            aggregation="rate",
            dimensions=dimensions,
        )
        rows.append(
            [
                dimensions.get("target", "—"),
                dimensions.get("detector_id", "—"),
                dimensions.get("code", "—"),
                dimensions.get("severity", "—"),
                _format_value(metric.value, "number"),
                _format_value(rate, "percent"),
            ]
        )
    rows.sort(key=lambda row: (row[0], row[1], row[2], row[3]))
    return Section(
        heading="Trajectory findings",
        blocks=[
            Table(
                columns=[
                    "Target",
                    "Detector",
                    "Finding",
                    "Severity",
                    "Count",
                    "Affected trajectories",
                ],
                rows=rows,
            )
        ],
    )


def _data_health_section(current: TrajectoryRunArtifact) -> Section:
    summary = current.build.summary
    metadata = current.dataset.metadata
    return Section(
        heading="Data health",
        blocks=[
            KV(
                items=[
                    ("Selected recordings", str(summary.selected_recordings)),
                    ("Included trajectories", str(summary.included_trajectories)),
                    ("Label annotations", str(summary.included_annotations)),
                    (
                        "Annotations without trajectory",
                        str(summary.unmatched_annotations),
                    ),
                    (
                        "Matched label Sessions",
                        str(metadata.get("matched_label_sessions", 0)),
                    ),
                    (
                        "Missing label Sessions",
                        str(metadata.get("missing_label_sessions", 0)),
                    ),
                    (
                        "Review 1 token usage coverage",
                        _format_value(
                            _model_usage(
                                current,
                                "review1",
                                "usage_coverage_ratio",
                                "mean",
                            ),
                            "percent",
                        ),
                    ),
                    (
                        "Review 2 token usage coverage",
                        _format_value(
                            _model_usage(
                                current,
                                "review2",
                                "usage_coverage_ratio",
                                "mean",
                            ),
                            "percent",
                        ),
                    ),
                    ("Dataset issues", str(len(summary.issues))),
                ]
            )
        ],
    )


def _label_counts(artifact: TrajectoryRunArtifact | None) -> Counter[str]:
    if artifact is None:
        return Counter()
    return Counter(
        str(annotation.annotation.get("label") or "unknown")
        for annotation in artifact.dataset.annotations
    )


def _rate(value: int, total: int) -> float | None:
    return value / total if total else None


def _trajectory_count(
    artifact: TrajectoryRunArtifact | None, target: str
) -> float | None:
    if artifact is None:
        return None
    return float(sum(value == target for _, value in artifact.run.trajectory_targets))


def _model_usage(
    artifact: TrajectoryRunArtifact | None,
    target: str,
    measurement: str,
    aggregation: str,
) -> float | None:
    return _measurement(
        artifact,
        target,
        "model_usage",
        measurement,
        aggregation,
    )


def _measurement(
    artifact: TrajectoryRunArtifact | None,
    target: str,
    measurer_id: str,
    measurement: str,
    aggregation: str,
) -> float | None:
    return _metric_value(
        artifact,
        name="measurement.value",
        aggregation=aggregation,
        dimensions={
            "target": target,
            "category": "cost",
            "measurer_id": measurer_id,
            "measurement": measurement,
        },
    )


def _duration_seconds(
    artifact: TrajectoryRunArtifact | None,
    target: str,
    aggregation: str,
) -> float | None:
    duration_ms = _metric_value(
        artifact,
        name="execution.duration_ms",
        aggregation=aggregation,
        dimensions={"target": target},
    )
    return duration_ms / 1000 if duration_ms is not None else None


def _total_tokens(artifact: TrajectoryRunArtifact | None) -> float | None:
    if artifact is None:
        return None
    values = [
        _model_usage(artifact, target, "total_tokens", "sum")
        for target in ("review1", "review2")
    ]
    present = [value for value in values if value is not None]
    return sum(present) if present else None


def _metric_value(
    artifact: TrajectoryRunArtifact | None,
    *,
    name: str,
    aggregation: str,
    dimensions: dict[str, str],
) -> float | None:
    if artifact is None:
        return None
    for metric in artifact.run.metrics:
        actual = dict(metric.dimensions)
        if (
            metric.name == name
            and metric.aggregation == aggregation
            and all(actual.get(key) == value for key, value in dimensions.items())
        ):
            return metric.value
    return None


def _summary_value(
    current: float | None,
    previous: float | None,
    kind: str,
) -> str:
    return (
        f"{_format_value(current, kind)} "
        f"(previous {_format_value(previous, kind)}, "
        f"{_format_delta(current, previous, kind)})"
    )


def _format_value(value: float | None, kind: str) -> str:
    if value is None:
        return "—"
    if kind == "percent":
        return f"{value * 100:.1f}%"
    if kind == "seconds":
        return f"{value:,.1f}s"
    return f"{value:,.0f}"


def _format_delta(
    current: float | None,
    previous: float | None,
    kind: str,
) -> str:
    if current is None or previous is None:
        return "—"
    delta = current - previous
    if kind == "percent":
        return f"{delta * 100:+.1f} pp"
    change = f", {delta / previous:+.1%}" if previous else ""
    if kind == "seconds":
        return f"{delta:+,.1f}s{change}"
    return f"{delta:+,.0f}{change}"


def create_harness(
    *,
    label_paths: Sequence[Path],
    source: RecordingSource,
    dataset_version: str,
) -> TrajectoryHarness:
    return TrajectoryHarness(
        builder=CCRTrajectoryDatasetBuilder(
            label_paths=label_paths,
            source=source,
            version=dataset_version,
        ),
        runner=CCRTrajectoryAnalysisRunner(),
        reporter=CCRTrajectoryReportBuilder(),
    )


def run_weekly_report(
    *,
    window: WeekWindow,
    label_paths: Sequence[Path],
    source: RecordingSource,
    runs_dir: Path,
    include_previous: bool = True,
) -> TrajectoryHarnessResult:
    history_dirs = ()
    previous_dir = runs_dir / "ccr-weekly" / window.previous().key
    if include_previous and (previous_dir / "run.json").is_file():
        history_dirs = (previous_dir,)
    return create_harness(
        label_paths=label_paths,
        source=source,
        dataset_version=window.key,
    ).run(
        runs_dir,
        scope="ccr-weekly",
        run_id=window.key,
        query=RecordingQuery(
            started_at_or_after=window.start,
            started_before=window.end,
        ),
        created_at=datetime.now(timezone.utc),
        history_dirs=history_dirs,
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--week", help="ISO week YYYY-Www; defaults to previous complete week"
    )
    parser.add_argument("--timezone", default="Asia/Shanghai")
    parser.add_argument("--labels", type=Path, action="append", default=None)
    parser.add_argument(
        "--sessions-dir",
        type=Path,
        default=Path.home() / ".casecodereview" / "sessions",
    )
    parser.add_argument("--repo", type=Path, action="append", default=[])
    parser.add_argument(
        "--runs-dir",
        type=Path,
        default=Path("eval/data/reports/trajectory"),
    )
    parser.add_argument("--no-history", action="store_true")
    args = parser.parse_args()

    try:
        zone = ZoneInfo(args.timezone)
        window = (
            WeekWindow.from_key(args.week, zone)
            if args.week
            else WeekWindow.previous_complete(zone)
        )
    except (ValueError, ZoneInfoNotFoundError) as error:
        parser.error(str(error))

    result = run_weekly_report(
        window=window,
        label_paths=args.labels or default_dataset_paths(),
        source=CCRSessionSource(args.sessions_dir, repositories=args.repo),
        runs_dir=args.runs_dir,
        include_previous=not args.no_history,
    )
    summary = result.artifact.build.summary
    print(
        json.dumps(
            {
                "run_dir": str(result.run_dir),
                "dataset": str(result.dataset_path),
                "report": str(result.report_path),
                "verdict": str(result.verdict_path),
                "trajectories": summary.included_trajectories,
                "annotations": summary.included_annotations,
                "issues": len(summary.issues),
            },
            ensure_ascii=False,
        )
    )
    return 1 if summary.issues else 0


if __name__ == "__main__":
    raise SystemExit(main())

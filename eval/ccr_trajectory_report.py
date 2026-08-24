#!/usr/bin/env python3
"""Build CCR's versioned trajectory dataset, evaluate it, and render HTML."""

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
from ccr_trajectory import evaluators_for_stage, review_stage
from harness_common.report_kit import KV, Section, Table
from trajectory_harness import (
    Evaluator,
    ModelUsageMeasurer,
    RecordingQuery,
    RecordingSource,
    Trajectory,
    TrajectoryDataset,
    TrajectoryEvaluationRunner,
    TrajectoryHarness,
    TrajectoryHarnessResult,
    TrajectoryReportBuilder,
    TrajectoryRunArtifact,
)
from weekly_report import WeekWindow, default_dataset_paths


class CCRTrajectoryEvaluationRunner(TrajectoryEvaluationRunner):
    """Evaluate CCR scopes with stage-specific checks and common cost measures."""

    def __init__(self) -> None:
        super().__init__(measurers=(ModelUsageMeasurer(),))

    def target_for(
        self, trajectory: Trajectory, dataset: TrajectoryDataset
    ) -> str:
        del dataset
        return review_stage(trajectory)

    def evaluators_for(
        self, target: str, dataset: TrajectoryDataset
    ) -> Sequence[Evaluator]:
        del dataset
        return evaluators_for_stage(target)


class CCRTrajectoryReportBuilder(TrajectoryReportBuilder):
    """Add CCR label coverage and annotation joins to the canonical report."""

    report_title = "CCR trajectory evaluation"

    def extra_sections(
        self,
        current: TrajectoryRunArtifact,
        history: Sequence[TrajectoryRunArtifact],
    ) -> tuple[Section, ...]:
        del history
        dataset = current.dataset
        labels = Counter(
            str(annotation.annotation.get("label") or "unknown")
            for annotation in dataset.annotations
        )
        metadata = dataset.metadata
        return (
            Section(
                heading="CCR label coverage",
                blocks=[
                    KV(
                        items=[
                            ("Input labels", str(metadata.get("labels", 0))),
                            (
                                "Labels without Session",
                                str(metadata.get("labels_without_session", 0)),
                            ),
                            (
                                "Matched label Sessions",
                                str(metadata.get("matched_label_sessions", 0)),
                            ),
                            (
                                "Missing label Sessions",
                                str(metadata.get("missing_label_sessions", 0)),
                            ),
                        ]
                    ),
                    Table(
                        columns=["Label", "Annotations"],
                        rows=[
                            [label, str(count)]
                            for label, count in sorted(labels.items())
                        ],
                        sort_default=(1, "desc"),
                    ),
                    Table(
                        columns=[
                            "Annotation",
                            "Label",
                            "Recording",
                            "Trajectories",
                        ],
                        rows=[
                            [
                                annotation.annotation_id,
                                str(
                                    annotation.annotation.get("label") or "unknown"
                                ),
                                annotation.recording_id,
                                ", ".join(annotation.trajectory_ids) or "—",
                            ]
                            for annotation in dataset.annotations
                        ],
                    ),
                ],
            ),
        )


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
        runner=CCRTrajectoryEvaluationRunner(),
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

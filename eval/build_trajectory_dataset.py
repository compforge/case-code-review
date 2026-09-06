#!/usr/bin/env python3
"""Build a versioned CCR trajectory dataset from local sessions and labels."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Iterable, Sequence

from ccr_source import CCRSessionSource
from ccr_trajectory import ATIFTrajectoryLoader
from trajectory_harness.model import (
    trajectory_metadata,
    trajectory_recording_id,
)
from trajectory_harness import (
    RecordingQuery,
    RecordingRef,
    RecordingSource,
    Trajectory,
    TrajectoryAnnotation,
    TrajectoryDataset,
    TrajectoryDatasetBuilder,
    write_dataset_artifact,
)


def read_examples(paths: Iterable[Path]) -> list[dict]:
    examples: dict[str, dict] = {}
    for path in paths:
        if not path.is_file():
            continue
        for line in path.read_text(encoding="utf-8").splitlines():
            if not line.strip():
                continue
            example = json.loads(line)
            if example.get("id"):
                examples[str(example["id"])] = example
    return sorted(
        examples.values(), key=lambda item: (item.get("at") or "", item["id"])
    )


class CCRTrajectoryDatasetBuilder(TrajectoryDatasetBuilder):
    """Join normalized forge labels to canonical trajectories by Session identity."""

    def __init__(
        self,
        *,
        label_paths: Sequence[Path],
        source: RecordingSource,
        dataset_id: str = "ccr-reviews",
        version: str = "local",
        loader: ATIFTrajectoryLoader | None = None,
    ) -> None:
        super().__init__(source=source, loader=loader or ATIFTrajectoryLoader())
        self.examples = read_examples(label_paths)
        self.dataset_id = dataset_id
        self.version = version

    def assemble(
        self,
        recordings: Sequence[RecordingRef],
        trajectories: Sequence[Trajectory],
        query: RecordingQuery | None,
    ) -> TrajectoryDataset:
        recording_ids = {recording.recording_id for recording in recordings}
        trajectory_ids_by_recording: dict[str, dict[str, str]] = {
            recording_id: {} for recording_id in recording_ids
        }
        dataset_trajectories = []
        for trajectory in trajectories:
            scope_id = trajectory.trajectory_id
            dataset_id = f"{trajectory_recording_id(trajectory)}/{scope_id}"
            trajectory_ids_by_recording[trajectory_recording_id(trajectory)][scope_id] = dataset_id
            dataset_trajectories.append(
                trajectory.model_copy(update={
                    "trajectory_id": dataset_id,
                    "extra": {
                        **(trajectory.extra or {}),
                        "case_harness": {
                            **(trajectory.extra or {}).get("case_harness", {}),
                            "metadata": {**trajectory_metadata(trajectory), "ccr_scope_id": scope_id},
                        },
                    },
                })
            )

        examples = self.examples
        if query is not None:
            # A time-bounded Dataset is scoped by selected Session identity, not by
            # the later forge-comment timestamp of its labels.
            examples = [
                example
                for example in examples
                if str((example.get("engine") or {}).get("session_id") or "")
                in recording_ids
            ]

        annotations: list[TrajectoryAnnotation] = []
        label_session_ids = set()
        labels_without_session = 0
        for example in examples:
            session_id = str((example.get("engine") or {}).get("session_id") or "")
            if not session_id:
                labels_without_session += 1
                continue
            label_session_ids.add(session_id)
            if session_id not in recording_ids:
                continue
            annotations.append(
                TrajectoryAnnotation(
                    annotation_id=str(example["id"]),
                    recording_id=session_id,
                    trajectory_ids=tuple(
                        trajectory_ids_by_recording[session_id][scope_id]
                        for scope_id in _trajectory_ids(
                            example,
                            set(trajectory_ids_by_recording[session_id]),
                        )
                    ),
                    annotation=_annotation(example),
                    dimensions={
                        "kind": str(example.get("kind") or "unknown"),
                        "label": str(example.get("label") or "unknown"),
                    },
                    metadata={"engine": example.get("engine") or {}},
                )
            )

        return TrajectoryDataset(
            dataset_id=self.dataset_id,
            version=self.version,
            trajectories=tuple(dataset_trajectories),
            annotations=tuple(annotations),
            metadata={
                "domain": "case-code-review",
                "labels": len(examples),
                "labels_without_session": labels_without_session,
                "label_sessions": len(label_session_ids),
                "matched_label_sessions": len(label_session_ids & recording_ids),
                "missing_label_sessions": len(label_session_ids - recording_ids),
            },
        )


def build_dataset(
    label_paths: Sequence[Path],
    source: RecordingSource,
    output_dir: Path,
    *,
    loader: ATIFTrajectoryLoader | None = None,
    dataset_id: str = "ccr-reviews",
    version: str = "local",
    query: RecordingQuery | None = None,
) -> dict[str, int]:
    """Build and persist the canonical trajectory-harness dataset artifact."""

    result = CCRTrajectoryDatasetBuilder(
        label_paths=label_paths,
        source=source,
        dataset_id=dataset_id,
        version=version,
        loader=loader,
    ).build(query)
    write_dataset_artifact(output_dir, result)
    for issue in result.summary.issues:
        print(
            f"trajectory build failed for session {issue.recording_id}: {issue.error}",
            file=sys.stderr,
        )

    metadata = result.dataset.metadata
    return {
        "labels": int(metadata["labels"]),
        "labels_without_session": int(metadata["labels_without_session"]),
        "linked_sessions": int(metadata["matched_label_sessions"]),
        "trajectories": len(result.dataset.trajectories),
        "annotations": len(result.dataset.annotations),
        "annotations_without_trajectory": result.summary.unmatched_annotations,
        "missing_sessions": int(metadata["missing_label_sessions"]),
        "export_failures": len(result.summary.issues),
    }


def _trajectory_ids(example: dict, available_ids: set[str]) -> list[str]:
    engine = example.get("engine") or {}
    hypothesis = engine.get("hypothesis") or {}
    assessment = engine.get("assessment") or {}
    candidates = [str(hypothesis.get("origin_unit") or "")]
    lane_id = str(assessment.get("lane_id") or "")
    if lane_id:
        candidates.append(
            lane_id
            if lane_id.startswith("hypothesis_review:")
            else f"hypothesis_review:{lane_id}"
        )
    return list(dict.fromkeys(item for item in candidates if item in available_ids))


def _annotation(example: dict) -> dict:
    keys = (
        "kind",
        "finding",
        "label",
        "rationale",
        "tags",
        "fingerprint",
        "path",
        "line",
        "source",
        "comment_url",
        "at",
    )
    return {key: example.get(key) for key in keys}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--labels",
        type=Path,
        action="append",
        default=None,
        help="normalized label JSONL; repeatable",
    )
    parser.add_argument(
        "--sessions-dir",
        type=Path,
        default=Path.home() / ".casecodereview" / "sessions",
    )
    parser.add_argument(
        "--out",
        type=Path,
        default=Path("eval/data/datasets/ccr-trajectories"),
    )
    parser.add_argument("--dataset-id", default="ccr-reviews")
    parser.add_argument("--dataset-version", default="local")
    parser.add_argument("--repo", type=Path, action="append", default=[])
    args = parser.parse_args()
    labels = args.labels or [
        Path("eval/data/datasets/review-comments-public.jsonl"),
        Path("eval/data/datasets/review-comments-private.jsonl"),
    ]
    source = CCRSessionSource(args.sessions_dir, repositories=args.repo)
    summary = build_dataset(
        labels,
        source,
        args.out,
        dataset_id=args.dataset_id,
        version=args.dataset_version,
    )
    print(json.dumps(summary, ensure_ascii=False))
    return 1 if summary["export_failures"] else 0


if __name__ == "__main__":
    raise SystemExit(main())

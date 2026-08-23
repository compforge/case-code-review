#!/usr/bin/env python3
"""Join normalized forge labels to local CCR trajectories."""

from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path
from typing import Iterable, Sequence

from ccr_source import CCRSessionSource
from ccr_trajectory import ATIFTrajectoryLoader
from trajectory_harness import RecordingSource


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


def build_dataset(
    label_paths: Sequence[Path],
    source: RecordingSource,
    output_dir: Path,
    *,
    loader: ATIFTrajectoryLoader | None = None,
) -> dict[str, int]:
    """Write one trajectory bundle per session and one sample per joined label."""

    loader = loader or ATIFTrajectoryLoader()
    examples = read_examples(label_paths)
    by_session: dict[str, list[dict]] = defaultdict(list)
    without_session = 0
    for example in examples:
        session_id = str((example.get("engine") or {}).get("session_id") or "")
        if not session_id:
            without_session += 1
            continue
        by_session[session_id].append(example)

    refs = {ref.recording_id: ref for ref in source.select()}
    output_dir.mkdir(parents=True, exist_ok=True)
    trajectories_path = output_dir / "trajectories.jsonl"
    samples_path = output_dir / "samples.jsonl"
    trajectory_temp = trajectories_path.with_suffix(".jsonl.tmp")
    sample_temp = samples_path.with_suffix(".jsonl.tmp")
    bundles = 0
    samples = 0
    missing_sessions = 0
    export_failures = 0
    samples_without_trajectory = 0
    try:
        with (
            trajectory_temp.open("w", encoding="utf-8") as trajectory_output,
            sample_temp.open("w", encoding="utf-8") as sample_output,
        ):
            for session_id in sorted(by_session):
                ref = refs.get(session_id)
                if ref is None:
                    missing_sessions += 1
                    continue
                try:
                    recording = source.fetch(ref)
                    trajectories = loader.loads(recording.text, source=ref.uri)
                except (OSError, RuntimeError, ValueError) as error:
                    export_failures += 1
                    print(
                        f"trajectory build failed for session {session_id}: {error}",
                        file=sys.stderr,
                    )
                    continue
                available_ids = {
                    trajectory.trajectory_id for trajectory in trajectories
                }
                bundle = {
                    "recording": ref.to_dict(),
                    "trajectories": [
                        trajectory.to_dict() for trajectory in trajectories
                    ],
                }
                trajectory_output.write(json.dumps(bundle, ensure_ascii=False) + "\n")
                bundles += 1
                for example in by_session[session_id]:
                    trajectory_ids = _trajectory_ids(example, available_ids)
                    if not trajectory_ids:
                        samples_without_trajectory += 1
                    sample = {
                        "sample_id": example["id"],
                        "recording_id": session_id,
                        "trajectory_ids": trajectory_ids,
                        "annotation": _annotation(example),
                        "engine": example.get("engine") or {},
                    }
                    sample_output.write(json.dumps(sample, ensure_ascii=False) + "\n")
                    samples += 1
        trajectory_temp.replace(trajectories_path)
        sample_temp.replace(samples_path)
    except BaseException:
        trajectory_temp.unlink(missing_ok=True)
        sample_temp.unlink(missing_ok=True)
        raise

    summary = {
        "labels": len(examples),
        "labels_without_session": without_session,
        "linked_sessions": len(by_session),
        "trajectory_bundles": bundles,
        "samples": samples,
        "samples_without_trajectory": samples_without_trajectory,
        "missing_sessions": missing_sessions,
        "export_failures": export_failures,
    }
    _write_json(output_dir / "manifest.json", summary)
    return summary


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


def _write_json(path: Path, value: object) -> None:
    temp = path.with_suffix(path.suffix + ".tmp")
    temp.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    temp.replace(path)


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
    parser.add_argument("--repo", type=Path, action="append", default=[])
    args = parser.parse_args()
    labels = args.labels or [
        Path("eval/data/datasets/review-comments-public.jsonl"),
        Path("eval/data/datasets/review-comments-private.jsonl"),
    ]
    source = CCRSessionSource(args.sessions_dir, repositories=args.repo)
    summary = build_dataset(labels, source, args.out)
    print(json.dumps(summary, ensure_ascii=False))
    return 1 if summary["export_failures"] else 0


if __name__ == "__main__":
    raise SystemExit(main())

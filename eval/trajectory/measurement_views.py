"""Generate a deterministic measurement snapshot for one CCR Review Viewer."""

from __future__ import annotations

import argparse
import hashlib
import json
import logging
import subprocess
from pathlib import Path

from trajectory_harness import TrajectoryAnalysisRunner, TrajectoryDataset, measurement_views
from eval.trajectory.atif import ATIFTrajectoryLoader
from eval.trajectory.ccr_trajectory_report import CCRTrajectoryAnalysisRunner


def source_digests(review: Path) -> dict[str, str]:
    files = [review, *sorted(review.with_suffix('.sessions').glob('*.jsonl'))]
    return {str(path.relative_to(review.parent)): hashlib.sha256(path.read_bytes()).hexdigest() for path in files}


# +spec=Viewer snapshots measure each native Session once and are bound to unchanged recording contents; no Detector or Verifier runs
# +link=repo://docs/observability.md
def write_measurements(review: Path, *, ccr_command: str = 'ccr') -> Path:
    review = review.expanduser().resolve()
    before = source_digests(review)
    exported = subprocess.run(
        [ccr_command, 'export', '--format', 'atif', str(review)],
        capture_output=True, text=True, check=True, timeout=120,
    )
    trajectories = ATIFTrajectoryLoader().loads(exported.stdout, source=str(review))
    version = hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest()
    dataset = TrajectoryDataset(review.stem, version, tuple(trajectories))
    runner = TrajectoryAnalysisRunner(measurers=CCRTrajectoryAnalysisRunner().measurers)
    run = runner.run(dataset, run_id=f'{review.stem}:{version[:12]}')
    snapshot = {'sources': before, 'views': measurement_views(run, trajectories=trajectories).to_dict()}
    if before != source_digests(review):
        raise RuntimeError('Recording changed during measurement; retry after the Review stops writing.')
    destination = review.with_suffix('.measurements.json')
    temporary = destination.with_suffix('.json.tmp')
    temporary.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2) + '\n')
    temporary.replace(destination)
    logging.getLogger(__name__).info('Measurement snapshot written review=%s sessions=%d path=%s', review, len(trajectories), destination)
    return destination


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('review', type=Path, help='Review journal JSONL path')
    parser.add_argument('--ccr', default='ccr', help='CCR binary with ATIF v1.8 export')
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO)
    print(write_measurements(args.review, ccr_command=args.ccr))


if __name__ == '__main__':
    main()

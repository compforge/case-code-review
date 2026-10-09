"""Analyze selected benchmark cases using their original Sessions; never rerun reviews."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess

from eval.eval_snapshot import file_sha256
from eval.trajectory.trajectory_judge import load_trajectories, objective_analysis


def analyze(selected: Path, out: Path, ccr: str = "ccr") -> list[dict]:
    out.mkdir(parents=True, exist_ok=True)
    cached: dict[str, dict] = {}
    results = []
    for line in selected.read_text().splitlines():
        row = json.loads(line)
        result = {key: row.get(key) for key in (
            "dataset", "dataset_id", "case_id", "run_id", "arm", "session", "session_id",
            "session_sha256", "selection_reasons", "hypothesis_ids", "unit_ids", "lane_ids")}
        session = Path(row["session"]) if row.get("session") else None
        digest = row.get("session_sha256")
        if session is None or not session.is_file():
            result["error"] = "session missing"
        elif file_sha256(session) != digest:
            result["error"] = "session changed since benchmark run"
        else:
            if digest not in cached:
                try:
                    exported = subprocess.run([ccr, "export", "--format", "atif", str(session)],
                                              capture_output=True, text=True, timeout=120, check=True)
                    atif = out / f"{digest}.atif.jsonl"
                    atif.write_text(exported.stdout)
                    trajectories = load_trajectories(str(atif))
                    if not trajectories:
                        raise ValueError("Session export contains no trajectories")
                    # Keep the complete session context. Matched Unit/Hypothesis
                    # IDs guide inspection; they do not hide formation/lane failures.
                    cached[digest] = {"atif": str(atif.resolve()), "trajectories": [
                        {"trajectory_id": item.trajectory_id, "analysis": objective_analysis(item)}
                        for item in trajectories]}
                except (OSError, ValueError, subprocess.SubprocessError) as error:
                    cached[digest] = {"error": f"trajectory unavailable: {error}"}
            result.update(cached[digest])
        results.append(result)
    (out / "cases.jsonl").write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in results))
    return results


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("selected", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--ccr", default="ccr", help="CLI used only for offline ATIF export")
    args = parser.parse_args()
    results = analyze(args.selected, args.out, args.ccr)
    failed = sum("error" in row for row in results)
    print(f"cases={len(results)} unavailable={failed} → {args.out}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())

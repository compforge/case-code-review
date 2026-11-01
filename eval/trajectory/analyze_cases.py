"""Analyze selected benchmark cases using their original Sessions; never rerun reviews."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess

from eval.eval_snapshot import review_sha256
from eval.trajectory.case_evidence import build_case_evidence
from eval.trajectory.trajectory_judge import load_trajectories, objective_analysis


def case_report(results: list[dict]) -> str:
    lines = ["# Case diagnosis evidence", "",
             "Evidence projection is deterministic. Diagnosis notes are supplied by the analyst, not generated Findings or Verdicts.", ""]
    for result in results:
        lines.extend([f"## {result.get('case_id') or 'unnamed case'}", "",
                      f"Snapshot: `{result.get('session_sha256')}`", ""])
        if result.get("error"):
            lines.extend(["Unavailable: " + result["error"], ""])
        evidence = result.get("evidence", {})
        if evidence:
            lines.append("Review evidence issues: " + json.dumps(evidence.get("issues", []), ensure_ascii=False))
        else:
            lines.append("Review evidence unavailable in this result.")
        for unit in evidence.get("units", []):
            lines.extend(["", f"Unit `{unit['unit_id']}` · recorded status `{unit['status']}`",
                          f"[Job View]({unit['job_view']}) · [Context View]({unit['context_text']})"])
            for path in unit["session_views"]:
                lines.append(f"[Session View fragment]({path})")
            if unit["issues"]:
                lines.append("View issues: " + json.dumps(unit["issues"], ensure_ascii=False))
        if result.get("diagnosis") is not None:
            lines.extend(["", "Analyst diagnosis (supplied input):", "", "```json",
                          json.dumps(result["diagnosis"], ensure_ascii=False, indent=2), "```"])
        lines.append("")
    return "\n".join(lines)


def analyze(selected: Path, out: Path, ccr: str = "ccr", *, views_only: bool = False) -> list[dict]:
    out.mkdir(parents=True, exist_ok=True)
    cached: dict[str, dict] = {}
    results = []
    for line in selected.read_text().splitlines():
        row = json.loads(line)
        result = {key: row.get(key) for key in (
            "dataset", "dataset_id", "case_id", "run_id", "arm", "session", "session_id",
            "session_sha256", "selection_reasons", "hypothesis_ids", "unit_ids", "lane_ids", "diagnosis")}
        session = Path(row["session"]) if row.get("session") else None
        digest = row.get("session_sha256")
        if session is None or not session.is_file():
            result["error"] = "session missing"
        elif review_sha256(session) != digest:
            result["error"] = "session changed since benchmark run"
        else:
            if row.get("unit_ids"):
                try:
                    result["evidence"] = build_case_evidence(session, row["unit_ids"], out / "views", ccr)
                    if review_sha256(session) != digest:
                        result.pop("evidence")
                        raise ValueError("session changed during evidence export")
                except (OSError, ValueError, subprocess.SubprocessError) as error:
                    result["error"] = f"case evidence unavailable: {error}"
            if views_only:
                if not row.get("unit_ids"):
                    result["error"] = "--views-only requires selected Unit IDs"
                results.append(result)
                continue
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
    (out / "cases.md").write_text(case_report(results))
    return results


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("selected", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--ccr", default="ccr", help="CLI used only for offline ATIF/Job export; Unit views require Job export support")
    parser.add_argument("--views-only", action="store_true", help="export linked Job/Context evidence without ATIF evaluation or model calls")
    args = parser.parse_args()
    results = analyze(args.selected, args.out, args.ccr, views_only=args.views_only)
    failed = sum("error" in row for row in results)
    print(f"cases={len(results)} unavailable={failed} → {args.out}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())

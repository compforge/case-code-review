"""Effect report; reference coverage and negative-claim repetition are separate."""
from __future__ import annotations

from collections import Counter, defaultdict
import json
from pathlib import Path


def write_report(results: list[dict], out: Path) -> None:
    groups: dict[tuple[str, str, str], Counter] = defaultdict(Counter)
    for result in results:
        for reference in result["references"]:
            for stage, value in reference["stages"].items():
                counter = groups[(result["arm"], stage, reference["reference"]["label"])]
                counter["total"] += 1
                counter["unknown" if value is None else "matched" if value else "unmatched"] += 1
    lines = ["# Benchmark effects", "",
             "Positive: coverage of annotated concerns. Negative: repetition of annotated incorrect claims.",
             "Unknown is excluded from evaluated denominators. Unlabelled predictions are not automatically false positives.",
             "Review 3 filtering is a product-policy observation, not automatically a defect.", "",
             "| Arm | Stage | Label | Matched | Evaluated | Unknown | Total |",
             "|---|---|---|---:|---:|---:|---:|"]
    summary = []
    for (arm, stage, label), counts in sorted(groups.items()):
        evaluated = counts["matched"] + counts["unmatched"]
        lines.append(f"| {arm} | {stage} | {label} | {counts['matched']} | {evaluated} | {counts['unknown']} | {counts['total']} |")
        summary.append({"arm": arm, "stage": stage, "label": label, **counts,
                        "evaluated": evaluated,
                        "match_rate": counts["matched"] / evaluated if evaluated else None})
    lines += ["", "## Cases for trajectory analysis", ""]
    for result in results:
        if result["selection_reasons"]:
            lines.append(f"- {result['case_id']} / {result['arm']}: {', '.join(result['selection_reasons'])}; "
                         f"session={result.get('session_id', 'missing')}")
    (out / "REPORT.md").write_text("\n".join(lines) + "\n")
    (out / "summary.json").write_text(json.dumps({"groups": summary, "cases": len(results),
        "incomplete": sum(not row["complete"] for row in results)}, ensure_ascii=False, indent=2) + "\n")

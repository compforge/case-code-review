"""Analyze benchmark effects and select recorded cases for trajectory diagnosis."""
from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path

from harness_common.llm import LLMClient

from eval.benchmark.matching import Matcher, match_reference
from eval.benchmark.report import write_report
from eval.benchmark.stages import project
from eval.eval_snapshot import file_sha256


def selection_reasons(result: dict) -> list[str]:
    reasons = set()
    if not result["complete"]:
        reasons.add("incomplete")
    for reference in result["references"]:
        stages = reference["stages"]
        if any(value is None for value in stages.values()):
            reasons.add("match_unassessed")
        if reference["reference"]["label"] == "negative":
            if stages["review2"] is True:
                reasons.add("negative_claim_supported")
            continue
        if stages["review1"] is False and result["complete"]:
            reasons.add("reference_not_discovered")
        elif stages["review1"] is True and stages["review2"] is False:
            reasons.add("review2_not_supported")
        elif stages["review2"] is True and stages["review3"] is False:
            # A product policy filter is an observation for inspection, not an
            # automatic bug verdict or an incentive to change the review prompt.
            reasons.add("review3_filtered")
    return sorted(reasons)


async def evaluate(run: dict, matcher: Matcher) -> dict:
    result = {**run, "complete": False, "claims": [], "references": [], "gaps": []}
    session = Path(run["session"]) if run.get("session") else None
    if session is None or not session.is_file():
        result["gaps"] = ["session missing"]
    elif file_sha256(session) != run.get("session_sha256"):
        result["gaps"] = ["session changed since run capture"]
    else:
        try:
            result.update(project(session))
        except (OSError, ValueError, TypeError, KeyError) as error:
            result["gaps"] = [f"session unreadable: {type(error).__name__}"]
    if run.get("error") or run.get("solve_state") != "ok":
        result["complete"] = False
    for reference in run["case"].get("references", []):
        matched = await match_reference(reference, result["claims"], matcher)
        # A positive observed match survives partial execution. Absence in an
        # incomplete run cannot establish a miss or successful negative rejection.
        if not result["complete"]:
            matched["stages"] = {stage: value if value is True else None
                                 for stage, value in matched["stages"].items()}
        result["references"].append(matched)
    result["selection_reasons"] = selection_reasons(result)
    result["hypothesis_ids"] = sorted({pair["claim_id"] for ref in result["references"]
                                       for pair in ref["pairs"] if pair["matched"] is True})
    result["unit_ids"] = sorted({claim["origin_unit"] for claim in result["claims"]
                                if claim["claim_id"] in result["hypothesis_ids"] and claim["origin_unit"]})
    result["lane_ids"] = sorted({claim["trial"].get("lane_id") for claim in result["claims"]
                                if claim["claim_id"] in result["hypothesis_ids"] and claim["trial"].get("lane_id")})
    return result


async def run_analysis(runs: Path, out: Path, matcher: Matcher) -> list[dict]:
    out.mkdir(parents=True, exist_ok=True)
    results = []
    with (out / "results.jsonl").open("w") as all_rows, (out / "selected.jsonl").open("w") as selected:
        for line in runs.read_text().splitlines():
            result = await evaluate(json.loads(line), matcher)
            encoded = json.dumps(result, ensure_ascii=False) + "\n"
            all_rows.write(encoded)
            if result["selection_reasons"]:
                selected.write(encoded)
            all_rows.flush()
            selected.flush()
            results.append(result)
    write_report(results, out)
    return results


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("runs", type=Path, help="runs.jsonl from benchmark.run")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--judge", action="store_true", help="Semantic matcher using EVAL_JUDGE_BASE/KEY/MODEL")
    parser.add_argument("--matches", type=Path, help="Explicit pair_id/matched/reason decisions override the judge")
    args = parser.parse_args()
    client = LLMClient.from_env() if args.judge else None
    if client is not None and not client.ready():
        parser.error("--judge requires EVAL_JUDGE_BASE, EVAL_JUDGE_KEY and EVAL_JUDGE_MODEL")
    matcher = Matcher(args.out / "matches.jsonl", client=client, decisions=args.matches)
    results = asyncio.run(run_analysis(args.runs, args.out, matcher))
    print(f"cases={len(results)} selected={sum(bool(row['selection_reasons']) for row in results)} → {args.out}")


if __name__ == "__main__":
    main()

"""Analyze benchmark effects and select recorded cases for trajectory diagnosis."""
from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path

from harness_toolbox.llm import LLMClient

from eval.benchmark.matching import Matcher, match_reference
from eval.benchmark.report import write_report
from eval.benchmark.stages import project
from eval.eval_snapshot import review_sha256, file_sha256


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
            elif stages["review1"] is True:
                reasons.add("negative_claim_proposed")
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
    elif review_sha256(session) != run.get("session_sha256"):
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


def load_runs(runs: Path, corpus: Path | None = None) -> list[dict]:
    rows = [json.loads(line) for line in runs.read_text().splitlines() if line.strip()]
    if corpus is None:
        return rows
    # A replay log alone does not freeze reference labels or the requested revisions.
    plan = json.loads((runs.parent / "plan.json").read_text())
    digest = file_sha256(corpus)
    if plan.get("corpus_sha256") != digest:
        raise ValueError("corpus changed since replay planning")
    entries = {e["name"]: e for e in json.loads(corpus.read_text())["entries"]}
    if len(entries) != len(json.loads(corpus.read_text())["entries"]):
        raise ValueError("duplicate corpus case names")
    recorded = {}
    for row in rows:
        sequence = row.get("sequence")
        if type(sequence) is not int or not 0 <= sequence < len(plan["schedule"]):
            raise ValueError("replay sequence outside frozen schedule")
        if sequence in recorded:
            raise ValueError("duplicate replay sequence")
        recorded[sequence] = row
    converted = []
    for sequence, selected in enumerate(plan["schedule"]):
        row = recorded.get(sequence, {"entry": selected["case"], "arm": selected["arm"],
                                     "run": selected["repeat"], "sequence": sequence,
                                     "error": "scheduled execution not recorded (pending or interrupted)"})
        entry = entries[row["entry"]]
        if (selected["case"], selected["arm"], selected["repeat"], selected["from"], selected["to"]) != (
                row["entry"], row["arm"], row["run"], entry["from"], entry["to"]):
            raise ValueError("replay row does not match frozen schedule")
        converted.append({**row, "case": entry, "case_id": entry.get("case_id", entry["name"]),
                          "dataset": "replay", "dataset_id": digest, "run_id": f"repeat-{row['run']}",
                          "solve_state": "error" if row.get("error") else "ok"})
    return converted


async def run_analysis(runs: Path, out: Path, matcher: Matcher, *, corpus: Path | None = None) -> list[dict]:
    inputs = load_runs(runs, corpus)
    out.mkdir(parents=True, exist_ok=True)
    results = []
    with (out / "results.jsonl").open("w") as all_rows, (out / "selected.jsonl").open("w") as selected:
        for run in inputs:
            result = await evaluate(run, matcher)
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
    parser.add_argument("--corpus", type=Path, help="interpret replay runs using its content-bound corpus and plan.json")
    parser.add_argument("--judge", action="store_true", help="Semantic matcher using EVAL_JUDGE_BASE/KEY/MODEL")
    parser.add_argument("--matches", type=Path, help="Explicit pair_id/matched/reason decisions override the judge")
    args = parser.parse_args()
    client = LLMClient.from_env() if args.judge else None
    if client is not None and not client.ready():
        parser.error("--judge requires EVAL_JUDGE_BASE, EVAL_JUDGE_KEY and EVAL_JUDGE_MODEL")
    matcher = Matcher(args.out / "matches.jsonl", client=client, decisions=args.matches)
    results = asyncio.run(run_analysis(args.runs, args.out, matcher, corpus=args.corpus))
    print(f"cases={len(results)} selected={sum(bool(row['selection_reasons']) for row in results)} → {args.out}")


if __name__ == "__main__":
    main()

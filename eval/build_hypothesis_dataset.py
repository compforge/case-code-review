#!/usr/bin/env python3
"""Build fixed Hypothesis cases from labeled finding datasets.

The output freezes the generator stage: later reviewer experiments consume the
same Hypothesis and human verdict, so changes in Unit Review recall cannot be
mistaken for changes in Hypothesis Review precision.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def read_jsonl(paths: list[Path]) -> list[dict]:
    records = []
    for path in paths:
        if not path.exists():
            continue
        records.extend(
            json.loads(line)
            for line in path.read_text(encoding="utf-8").splitlines()
            if line.strip()
        )
    return records


def build_case(record: dict) -> dict | None:
    engine = record.get("engine") or {}
    hypothesis = engine.get("hypothesis")
    if not hypothesis:
        return None
    label = record.get("label")
    return {
        "id": record.get("id"),
        "expected_delivery": label in {"important", "minor"},
        "label": label,
        "rationale": record.get("rationale") or "",
        "hypothesis": hypothesis,
        "previous_assessment": engine.get("assessment"),
        "engine": {
            key: engine.get(key)
            for key in ("session_id", "tool_version", "model", "features", "git_head")
        },
        "source": record.get("source"),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "inputs",
        nargs="*",
        type=Path,
        default=[
            Path("eval/data/datasets/review-comments-public.jsonl"),
            Path("eval/data/datasets/review-comments-private.jsonl"),
        ],
    )
    parser.add_argument(
        "--out",
        type=Path,
        default=Path("eval/data/datasets/review-hypotheses.jsonl"),
    )
    args = parser.parse_args()

    cases = [case for record in read_jsonl(args.inputs) if (case := build_case(record))]
    args.out.parent.mkdir(parents=True, exist_ok=True)
    temp = args.out.with_suffix(args.out.suffix + ".tmp")
    temp.write_text(
        "".join(json.dumps(case, ensure_ascii=False) + "\n" for case in cases),
        encoding="utf-8",
    )
    temp.replace(args.out)
    print(json.dumps({"cases": len(cases), "out": str(args.out)}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

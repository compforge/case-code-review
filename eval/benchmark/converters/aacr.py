"""Convert AACR's positive/negative comment annotations into a frozen CCR corpus."""
from __future__ import annotations

import argparse
import json
import re
from pathlib import Path

from eval.eval_snapshot import canonical_sha256, file_sha256


def convert(positive: list[dict], negative: list[dict], language: str | None = None) -> dict:
    cases: dict[str, dict] = {}
    for label, records in (("positive", positive), ("negative", negative)):
        for record in records:
            if language and record.get("project_main_language") != language:
                continue
            match = re.fullmatch(r"https://github.com/([^/]+/[^/]+)/pull/(\d+)/?", record["githubPrUrl"])
            if match is None:
                raise ValueError(f"invalid GitHub PR URL: {record['githubPrUrl']}")
            repo, pr = match.groups()
            base, head = record["source_commit"], record["target_commit"]
            identity = f"{repo}#{pr}@{base}..{head}"
            case = cases.setdefault(identity, {
                "name": identity, "case_id": identity, "repository": repo,
                "from": base, "to": head, "url": record["githubPrUrl"],
                "language": record.get("project_main_language"), "references": [],
            })
            for comment in record.get("comments", []):
                # A negative annotation labels an incorrect comment, never a clean PR.
                reference = {
                    "label": label, "path": comment["path"], "text": comment["note"],
                    "side": {"left": "old", "right": "new"}[comment["side"].lower()],
                    "start_line": comment.get("from_line"), "end_line": comment.get("to_line"),
                    "category": comment.get("category"), "context": comment.get("context"),
                    "is_ai_comment": comment.get("is_ai_comment"),
                    "source_model": comment.get("source_model"),
                }
                reference["reference_id"] = canonical_sha256(reference)
                if reference not in case["references"]:
                    case["references"].append(reference)
    entries = sorted(cases.values(), key=lambda case: case["case_id"])
    return {"dataset": "aacr-bench", "entries": entries}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--positive", type=Path, required=True)
    parser.add_argument("--negative", type=Path)
    parser.add_argument("--language", help="Exact language name, e.g. Go")
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    paths = [args.positive] + ([args.negative] if args.negative else [])
    corpus = convert(json.loads(args.positive.read_text()),
                     json.loads(args.negative.read_text()) if args.negative else [], args.language)
    corpus["sources"] = [{"name": path.name, "sha256": file_sha256(path)} for path in paths]
    corpus["dataset_id"] = canonical_sha256(corpus)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(corpus, ensure_ascii=False, indent=2) + "\n")
    print(f"cases={len(corpus['entries'])} dataset_id={corpus['dataset_id']} → {args.out}")


if __name__ == "__main__":
    main()

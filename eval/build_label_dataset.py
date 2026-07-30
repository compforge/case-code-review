#!/usr/bin/env python3
"""Build self-contained finding + human verdict datasets from harvested labels."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from collections import Counter, defaultdict
from pathlib import Path

HEADER_RE = re.compile(
    r"^\s*🤖\s+\*\*devloop code-review\*\*(?:\s*·\s*([^\n]+))?\n+"
)
FOOTER_RE = re.compile(r"\n*<sub>ccr:fp=[A-Za-z0-9_-]+</sub>\s*$")
THREAD_ID_RE = re.compile(r"(?:discussion_r|note_)(\d+)")


def read_jsonl(path: Path) -> list[dict]:
    return [
        json.loads(line)
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]


def record_identity(record: dict) -> tuple:
    source = record.get("source") or ""
    reply_id = record.get("reply_id")
    if source.startswith("github:"):
        # GitHub review comment ids are global; this also collapses repository
        # rename duplicates such as old-owner/repo → new-owner/repo.
        return "github", reply_id
    if source.startswith("gitlab:"):
        host = source.split("/", 1)[0]
        return host, reply_id
    return source, reply_id


def load_labels(labels_dir: Path) -> list[dict]:
    unique_replies: dict[tuple, dict] = {}
    for path in sorted(labels_dir.glob("*.jsonl")):
        for record in read_jsonl(path):
            key = record_identity(record)
            current = unique_replies.get(key)
            if current is None or (not current.get("finding") and record.get("finding")):
                unique_replies[key] = record

    latest_by_thread: dict[tuple, dict] = {}
    for record in unique_replies.values():
        comment_url = record.get("comment_url") or ""
        thread_id = THREAD_ID_RE.search(comment_url)
        key = (
            (record.get("source") or "").split(":", 1)[0],
            thread_id.group(1),
        ) if thread_id else record_identity(record)
        current = latest_by_thread.get(key)
        if current is None or (record.get("at") or "") > (current.get("at") or ""):
            latest_by_thread[key] = record
    return list(latest_by_thread.values())


def load_session_findings(sessions_dir: Path) -> dict[str, str]:
    by_fingerprint: dict[str, set[str]] = defaultdict(set)
    for path in sessions_dir.rglob("*.jsonl"):
        try:
            records = read_jsonl(path)
        except (OSError, json.JSONDecodeError):
            continue
        for record in records:
            fingerprint = record.get("fingerprint")
            content = record.get("content")
            if record.get("type") == "finding" and fingerprint and content:
                by_fingerprint[fingerprint].add(content.strip())
    # A fingerprint with multiple distinct texts is ambiguous and unsafe to
    # backfill. Direct forge comment text still wins for those records.
    return {
        fingerprint: next(iter(contents))
        for fingerprint, contents in by_fingerprint.items()
        if len(contents) == 1
    }


def normalize_finding(raw: str) -> tuple[str, str | None]:
    match = HEADER_RE.match(raw)
    model = match.group(1).strip() if match and match.group(1) else None
    body = raw[match.end() :] if match else raw
    return FOOTER_RE.sub("", body).strip(), model


def build_example(record: dict, session_findings: dict[str, str]) -> dict | None:
    kind = "missed" if record.get("label") == "missed" else "finding"
    raw = record.get("finding")
    if not raw and record.get("fingerprint"):
        raw = session_findings.get(record["fingerprint"])
    if kind == "missed":
        raw = raw or record.get("note")
    if not raw:
        return None
    finding, model = normalize_finding(raw)
    source = record.get("source") or ""
    reply_id = record.get("reply_id")
    example_id = hashlib.sha256(f"{source}:{reply_id}".encode()).hexdigest()[:16]
    return {
        "id": example_id,
        "kind": kind,
        "finding": finding,
        "label": record.get("label"),
        "rationale": record.get("note") or "",
        "tags": record.get("tags") or [],
        "fingerprint": record.get("fingerprint"),
        "path": record.get("path"),
        "line": record.get("line"),
        "model": model,
        "source": source,
        "comment_url": record.get("comment_url"),
        "reply_id": reply_id,
        "by": record.get("by"),
        "at": record.get("at"),
    }


def write_jsonl(path: Path, records: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_suffix(path.suffix + ".tmp")
    with temp.open("w", encoding="utf-8") as output:
        for record in records:
            output.write(json.dumps(record, ensure_ascii=False) + "\n")
    temp.replace(path)


def distribution(records: list[dict]) -> dict[str, int]:
    return dict(sorted(Counter(record["label"] for record in records).items()))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--labels-dir", type=Path, default=Path("eval/data/labels"))
    parser.add_argument(
        "--sessions-dir",
        type=Path,
        default=Path.home() / ".casecodereview" / "sessions",
    )
    parser.add_argument(
        "--public-out",
        type=Path,
        default=Path("eval/data/datasets/review-comments-public.jsonl"),
    )
    parser.add_argument(
        "--private-out",
        type=Path,
        default=Path("eval/data/datasets/review-comments-private.jsonl"),
    )
    args = parser.parse_args()

    labels = load_labels(args.labels_dir)
    session_findings = load_session_findings(args.sessions_dir)
    examples = [
        example
        for record in labels
        if (example := build_example(record, session_findings)) is not None
    ]
    examples.sort(key=lambda record: (record.get("at") or "", record["id"]))
    public = [record for record in examples if record["source"].startswith("github:")]
    private = [record for record in examples if not record["source"].startswith("github:")]
    write_jsonl(args.public_out, public)
    write_jsonl(args.private_out, private)
    print(
        json.dumps(
            {
                "harvested_labels": len(labels),
                "examples": len(examples),
                "unpaired": len(labels) - len(examples),
                "public": {
                    "count": len(public),
                    "by_label": distribution(public),
                    "out": str(args.public_out),
                },
                "private": {
                    "count": len(private),
                    "by_label": distribution(private),
                    "out": str(args.private_out),
                },
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

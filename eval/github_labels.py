#!/usr/bin/env python3
"""Harvest CCR labels from GitHub pull requests discovered by time window.

The single-PR parser remains in ``labels.py``. This source adds the missing
discovery and orchestration layer: find merged pull requests, harvest every
review comment (resolved threads remain visible through the REST API), and
upsert one raw label file per repository.

Usage:
    python3 eval/github_labels.py --owner example --author @me \
        --since 2026-08-17 --until 2026-08-23
"""

from __future__ import annotations

import argparse
import json
import subprocess
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import asdict, dataclass
from datetime import date, datetime, timezone
from pathlib import Path
from typing import Callable

from labels import harvest_github, upsert_records


@dataclass(frozen=True, slots=True)
class PullRequest:
    repository: str
    number: int
    url: str


def _gh_json(*args: str) -> list[dict]:
    process = subprocess.run(
        ["gh", *args],
        capture_output=True,
        text=True,
        timeout=120,
    )
    if process.returncode != 0:
        detail = process.stderr.strip() or process.stdout.strip() or "unknown error"
        raise RuntimeError(f"GitHub discovery failed: {detail}")
    payload = json.loads(process.stdout)
    if not isinstance(payload, list):
        raise ValueError("GitHub discovery returned a non-list response")
    return payload


def discover_pull_requests(
    owners: list[str],
    author: str,
    since: date,
    until: date,
    limit: int,
) -> list[PullRequest]:
    """Discover merged pull requests without coupling harvesting to one repo."""

    found: dict[tuple[str, int], PullRequest] = {}
    merged_at = f"{since.isoformat()}..{until.isoformat()}"
    for owner in owners:
        rows = _gh_json(
            "search",
            "prs",
            "--owner",
            owner,
            "--author",
            author,
            "--merged",
            "--merged-at",
            merged_at,
            "--limit",
            str(limit),
            "--json",
            "repository,number,url",
        )
        for row in rows:
            repository = str((row.get("repository") or {}).get("nameWithOwner") or "")
            number = int(row.get("number") or 0)
            if not repository or number <= 0:
                continue
            found[(repository, number)] = PullRequest(
                repository=repository,
                number=number,
                url=str(row.get("url") or ""),
            )
    return sorted(found.values(), key=lambda item: (item.repository, item.number))


def _label_path(out_dir: Path, repository: str) -> Path:
    return out_dir / f"{repository.replace('/', '-')}.jsonl"


def harvest_pull_requests(
    pull_requests: list[PullRequest],
    out_dir: Path,
    workers: int,
    harvester: Callable[[str, int], list[dict]] = harvest_github,
) -> dict:
    """Harvest independently so one inaccessible PR cannot erase useful labels."""

    records_by_repository: dict[str, list[dict]] = defaultdict(list)
    errors: list[dict[str, object]] = []
    harvested = 0
    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {
            pool.submit(
                harvester, pull_request.repository, pull_request.number
            ): pull_request
            for pull_request in pull_requests
        }
        for future in as_completed(futures):
            pull_request = futures[future]
            try:
                records_by_repository[pull_request.repository].extend(future.result())
                harvested += 1
            except Exception as error:
                errors.append(
                    {
                        "repository": pull_request.repository,
                        "number": pull_request.number,
                        "error": str(error),
                    }
                )

    fresh = 0
    updated = 0
    records: list[dict] = []
    outputs: list[str] = []
    for repository, repository_records in sorted(records_by_repository.items()):
        repository_records.sort(
            key=lambda record: (record.get("at") or "", record.get("reply_id") or 0)
        )
        path = _label_path(out_dir, repository)
        repo_fresh, repo_updated = upsert_records(path, repository_records)
        fresh += repo_fresh
        updated += repo_updated
        records.extend(repository_records)
        outputs.append(str(path))

    labels = Counter(str(record.get("label") or "unknown") for record in records)
    latest_label_at = max(
        (str(record.get("at")) for record in records if record.get("at")),
        default=None,
    )
    return {
        "pull_requests_discovered": len(pull_requests),
        "pull_requests_harvested": harvested,
        "pull_requests_failed": len(errors),
        "labels": len(records),
        "new": fresh,
        "updated": updated,
        "latest_label_at": latest_label_at,
        "by_label": dict(sorted(labels.items())),
        "errors": sorted(
            errors, key=lambda item: (str(item["repository"]), int(item["number"]))
        ),
        "outputs": outputs,
    }


def write_manifest(path: Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    temporary.replace(path)


def _date(value: str) -> date:
    try:
        return date.fromisoformat(value)
    except ValueError as error:
        raise argparse.ArgumentTypeError("expected YYYY-MM-DD") from error


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--owner", action="append", required=True)
    parser.add_argument("--author", default="@me")
    parser.add_argument("--since", required=True, type=_date)
    parser.add_argument("--until", required=True, type=_date)
    parser.add_argument("--limit", type=int, default=1000)
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--out-dir", type=Path, default=Path("eval/data/labels"))
    parser.add_argument(
        "--manifest",
        type=Path,
        default=Path("eval/data/labels/github-harvest.json"),
    )
    args = parser.parse_args()
    if args.until < args.since:
        parser.error("--until must be on or after --since")
    if args.limit <= 0:
        parser.error("--limit must be positive")
    if args.workers <= 0:
        parser.error("--workers must be positive")

    pull_requests = discover_pull_requests(
        args.owner, args.author, args.since, args.until, args.limit
    )
    summary = harvest_pull_requests(pull_requests, args.out_dir, args.workers)
    manifest = {
        "schema_version": "github-label-harvest-v1",
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "query": {
            "owners": sorted(set(args.owner)),
            "author": args.author,
            "since": args.since.isoformat(),
            "until": args.until.isoformat(),
        },
        # REST review comments include replies from both resolved and unresolved threads.
        "includes_resolved_threads": True,
        "pull_requests": [asdict(pull_request) for pull_request in pull_requests],
        **summary,
    }
    write_manifest(args.manifest, manifest)
    print(
        json.dumps(
            {
                "schema_version": manifest["schema_version"],
                "generated_at": manifest["generated_at"],
                "query": manifest["query"],
                "includes_resolved_threads": manifest["includes_resolved_threads"],
                "manifest": str(args.manifest),
                **summary,
            },
            ensure_ascii=False,
        )
    )
    return 1 if summary["pull_requests_failed"] else 0


if __name__ == "__main__":
    raise SystemExit(main())

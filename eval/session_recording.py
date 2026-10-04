"""Read the intact Session prefix and project native timeline execution facts."""
from __future__ import annotations

import json
from datetime import datetime
from pathlib import Path


def read_records(path: Path) -> tuple[list[dict], list[str]]:
    records: list[dict] = []
    gaps: list[str] = []
    with path.open("rb") as stream:
        for number, line in enumerate(stream, 1):
            if not line.strip():
                continue
            try:
                record = json.loads(line.decode("utf-8"))
                if not isinstance(record, dict):
                    raise ValueError("record must be an object")
            except (ValueError, UnicodeError):
                kind = "truncated final record" if not line.endswith(b"\n") else "corrupt record"
                gaps.append(f"line {number}: {kind}")
                break
            records.append(record)
    return records, gaps


def _finished(stage: dict) -> bool:
    end = stage.get("finished_at")
    return bool(end and not end.startswith("0001-01-01T00:00:00"))


def execution_facts(records: list[dict]) -> dict[str, dict]:
    """Outcome comes from completed execution stages, never generic success."""
    stages: dict[str, dict] = {}
    for record in records:
        if record.get("type") != "timeline_update":
            continue
        update = record.get("update") or {}
        for stage in update.get("Stages") or []:
            if stage.get("name") != "execution":
                continue
            identity = stage["id"]
            previous = stages.get(identity)
            if previous is not None:
                revision, old_revision = stage["revision"], previous["revision"]
                if revision < old_revision:
                    continue
                if revision == old_revision:
                    if stage != previous:
                        raise ValueError(f"conflicting execution stage: {identity}")
                    continue
                if _finished(previous):
                    raise ValueError(f"execution stage changed after completion: {identity}")
            stages[identity] = stage
    result = {}
    for identity, stage in stages.items():
        facts = dict(stage.get("fields") or {})
        if not _finished(stage):
            facts.pop("outcome", None)
        else:
            start = datetime.fromisoformat(stage["started_at"].replace("Z", "+00:00"))
            end = datetime.fromisoformat(stage["finished_at"].replace("Z", "+00:00"))
            facts["duration_ms"] = (stage.get("elapsed_ns") or (end - start).total_seconds() * 1e9) / 1e6
        facts["stage_id"] = identity
        result[identity] = facts
    return result

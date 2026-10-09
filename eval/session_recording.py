"""Read the intact Session prefix and project native timeline execution facts."""
from __future__ import annotations

import json
from collections import Counter
from dataclasses import dataclass
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


def timeline_stages(records: list[dict]) -> dict[str, dict]:
    """Merge stage revisions before projecting execution or cost facts."""
    stages: dict[str, dict] = {}
    for record in records:
        if record.get("type") != "timeline_update":
            continue
        update = record.get("update") or {}
        for stage in update.get("Stages") or []:
            identity = stage["id"]
            previous = stages.get(identity)
            if previous is not None:
                revision, old_revision = stage["revision"], previous["revision"]
                if revision < old_revision:
                    continue
                if revision == old_revision:
                    if stage != previous:
                        raise ValueError(f"conflicting timeline stage: {identity}")
                    continue
                if _finished(previous):
                    raise ValueError(f"timeline stage changed after completion: {identity}")
            stages[identity] = stage
    return stages


def execution_facts(records: list[dict]) -> dict[str, dict]:
    """Outcome comes from completed execution stages, never generic success."""
    stages = timeline_stages(records)
    result = {}
    for identity, stage in stages.items():
        if stage.get("name") != "execution":
            continue
        attributes = stage.get("attributes")
        if attributes is None:
            attributes = stage.get("fields")  # Sessions recorded before the upstream rename.
        facts = dict(attributes or {})
        if not _finished(stage):
            facts.pop("outcome", None)
        else:
            start = datetime.fromisoformat(stage["started_at"].replace("Z", "+00:00"))
            end = datetime.fromisoformat(stage["finished_at"].replace("Z", "+00:00"))
            facts["duration_ms"] = (stage.get("elapsed_ns") or (end - start).total_seconds() * 1e9) / 1e6
        facts["stage_id"] = identity
        result[identity] = facts
    return result


@dataclass
class Recording:
    """Intact recording evidence shared by discovery, comparison and replay."""

    records: list[dict]
    gaps: list[str]

    @classmethod
    def read(cls, path: Path) -> Recording:
        records, gaps = read_records(path)
        seen: set[str] = set()
        unique = []
        for record in records:
            identity = record.get("uuid")
            if identity and identity in seen:
                continue
            if identity:
                seen.add(identity)
            unique.append(record)
        for record in unique:
            if record.get("type") == "session_start" and record.get("schema_version", 11) != 11:
                gaps.append(f"unsupported session schema: {record.get('schema_version')}")
        try:
            timeline_stages(unique)
        except (ValueError, TypeError, KeyError) as error:
            gaps.append(str(error))
        return cls(unique, gaps)

    @property
    def start(self) -> dict:
        return next((r for r in self.records if r.get("type") == "session_start"), {})

    @property
    def end(self) -> dict | None:
        return next((r for r in reversed(self.records) if r.get("type") == "session_end"), None)

    def costs(self) -> dict:
        # Request identity is the stage ID, shared by its request and terminal
        # content. A scope/debrief is neither necessary nor sufficient for cost.
        calls: dict[str, dict] = {}
        for number, record in enumerate(self.records):
            kind = record.get("type")
            if kind not in ("llm_request", "llm_response", "llm_error"):
                continue
            identity = record.get("stage_id") or record.get("uuid") or f"unassociated:{number}"
            if identity not in calls or kind != "llm_request" or calls[identity]["type"] == "llm_request":
                calls[identity] = record
        totals = Counter()
        for record in calls.values():
            totals["rounds"] += 1
            kind = record["type"]
            totals["pending_calls"] += kind == "llm_request"
            totals["error_calls"] += kind == "llm_error"
            usage = record.get("usage")
            if kind == "llm_response" and isinstance(usage, dict):
                for key in ("prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens"):
                    totals[key] += usage.get(key, 0)
                totals["estimated_usage"] += record.get("usage_source") == "estimated"
            else:
                totals["unknown_usage"] += 1
        return {key: totals[key] for key in (
            "rounds", "prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens",
            "pending_calls", "error_calls", "estimated_usage", "unknown_usage")}


def encode_repo_path(p: str) -> str:
    """Port of internal/harness/session/persist.go encodeRepoPath (posix subset)."""
    p = p.lstrip("/\\").replace("/", "-").replace("\\", "-")
    return p or "empty"


def session_dir(repo: str) -> Path:
    return Path.home() / ".casecodereview" / "sessions" / encode_repo_path(str(Path(repo).resolve()))


def find_session(repo: str, tag: str) -> Path | None:
    """Locate the transcript this run wrote, by its unique eval_tag."""
    d = session_dir(repo)
    if not d.is_dir():
        return None
    for f in sorted(d.glob("*.jsonl"), key=lambda p: p.stat().st_mtime, reverse=True):
        try:
            with f.open(encoding="utf-8", errors="replace") as stream:
                first = stream.readline()
            if json.loads(first).get("eval_tag") == tag:
                return f
        except (OSError, json.JSONDecodeError):
            continue
    return None

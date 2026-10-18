"""Read the intact Session prefix and project native timeline execution facts."""
from __future__ import annotations

import json
import re
from collections import Counter
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path


def _read_manifest(path: Path) -> tuple[list[dict], list[str]]:
    records: list[dict] = []
    gaps: list[str] = []
    latest_snapshot = None
    timeline_id = None
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
            if record.get("type") == "timeline_snapshot":
                snapshot = record.get("snapshot")
                identity = record.get("timeline_id")
                if not isinstance(snapshot, dict) or not identity or snapshot.get("id") != identity or timeline_id not in (None, identity):
                    gaps.append(f"line {number}: inconsistent timeline ID")
                    break
                timeline_id = identity
                latest_snapshot = record
            else:
                records.append(record)
    if latest_snapshot is not None:
        records.append(latest_snapshot)
    return records, gaps


def _jsonl(path: Path, gaps: list[str]) -> list[dict]:
    records = []
    with path.open("rb") as stream:
        for number, line in enumerate(stream, 1):
            if not line.endswith(b"\n"):
                gaps.append(f"{path.name}:{number}: truncated final record")
                break
            try:
                record = json.loads(line)
                if not isinstance(record, dict):
                    raise ValueError("record must be an object")
                records.append(record)
            except (ValueError, UnicodeError) as error:
                gaps.append(f"{path.name}:{number}: corrupt record: {error}")
                break
    return records


def _timeline(path: Path, gaps: list[str]) -> dict | None:
    if not path.exists():
        return None
    identity = None
    operation = {}
    stages = {}
    for number, record in enumerate(_jsonl(path, gaps), 1):
        if record.get("version") != 1 or record.get("seq") != number or not record.get("id") or identity not in (None, record["id"]):
            gaps.append(f"{path.name}:{number}: invalid timeline envelope")
            break
        identity = record["id"]
        update = record["update"]
        if update.get("Operation"):
            for key, value in update["Operation"].items():
                if key in ("started_at", "finished_at") and (not value or value.startswith("0001-01-01")):
                    continue
                operation[key] = value
        for stage in (update.get("Stages") or []) + (update.get("Completed") or []):
            actor = json.dumps(stage.get("actor") or {}, sort_keys=True)
            stages[(stage["id"], actor)] = stage
    if not identity:
        gaps.append(f"{path.name}: missing timeline")
        return None
    return {"type": "timeline_snapshot", "timeline_id": identity,
            "snapshot": {**operation, "id": identity, "root_stage_id": f"operation:{identity}", "stages": list(stages.values())}}


def _reference(ref: dict) -> tuple:
    return tuple(ref.get(key) for key in ("session_id", "run_id", "execution_id", "attempt"))


def _wire(message: dict) -> dict:
    value = message.get("value", message)
    blocks = value.get("content") or []
    wire = {"role": value.get("role"), "content": "".join(b.get("text", "") for b in blocks if b.get("type") == "text")}
    thinking = "".join(b.get("thinking", "") for b in blocks if b.get("type") == "thinking")
    if thinking:
        wire["reasoning_content"] = thinking
    calls = []
    for block in blocks:
        call = block.get("tool_call")
        if call:
            args = call.get("args_raw_text") if call.get("args_invalid") else json.dumps(call.get("args", {}), ensure_ascii=False, separators=(",", ":"))
            calls.append({"id": call["id"], "type": "function", "function": {"name": call["name"], "arguments": args}})
    if calls:
        wire["tool_calls"] = calls
    tool_id = (value.get("metadata") or {}).get("tool_call_id")
    if tool_id:
        wire["tool_call_id"] = tool_id
    return wire


def _loop_records(path: Path, stages: dict, gaps: list[str]) -> list[dict]:
    refs = {}
    owners = {}
    for stage in stages.values():
        ref = stage.get("attributes", {}).get("session_ref")
        if not ref:
            continue
        if stage["name"] in ("model.attempt", "tool.execution", "llm.request"):
            refs[_reference(ref)] = stage
        owner = stage
        visited = set()
        while owner.get("parent_id") in stages and owner["id"] not in visited:
            visited.add(owner["id"])
            if owner["name"] == "execution":
                break
            owner = stages[owner["parent_id"]]
        owners[(ref["session_id"], ref["run_id"])] = owner
    result = []
    session_id = None
    calls, arguments = {}, {}
    ended = set()
    rounds, projections = Counter(), Counter()
    for number, record in enumerate(_jsonl(path, gaps), 1):
        data = record.get("data") or {}
        if record.get("seq") != number or not record.get("session_id") or session_id not in (None, record["session_id"]):
            gaps.append(f"{path.name}:{number}: invalid Session envelope")
            break
        session_id = record["session_id"]
        if number == 1 and (record.get("type") != "session_start" or data.get("version") != 1):
            gaps.append(f"{path.name}: unsupported Session version")
            break
        execution = record.get("execution") or {}
        ref = {"session_id": session_id, "run_id": record.get("run_id"), "execution_id": execution.get("ID"), "attempt": execution.get("Attempt", 0)}
        stage = refs.get(_reference(ref), {})
        owner = stage or owners.get((session_id, record.get("run_id")), {})
        facts, visited = {}, set()
        while owner and owner["id"] not in visited:
            visited.add(owner["id"])
            facts = {**owner.get("attributes", {}), **facts}
            owner = stages.get(owner.get("parent_id"))
        facts.update(call_id="/".join(str(value or "") for value in _reference(ref)), session_ref=ref, session_file=path.name, stage_id=stage.get("id", ""), timestamp=record.get("timestamp"), uuid=f"{session_id}/{number}")
        facts["taskType"] = facts.get("task_type")
        execution_id = facts.get("execution_id")
        kind = record["type"]
        call_key = (record.get("run_id"), data.get("tool_id"))
        if kind == "model_request":
            rounds[execution_id] += 1
            request = data["model_request"]
            facts.update(type="llm_request", request_no=rounds[execution_id], messages=[_wire(m) for m in request.get("messages", [])], tools=request.get("tools", []))
        elif kind == "model_exec_end":
            ended.add(_reference(ref))
            if data.get("error"):
                facts.update(type="llm_error", error=data["error"])
            else:
                message = data.get("message", {}).get("value", {})
                wire = _wire(message)
                facts.update(type="llm_response", content=wire.get("content", ""), reasoning=wire.get("reasoning_content", ""), stop_reason=message.get("stop_reason"), usage_source="missing")
                usage = message.get("usage")
                if usage:
                    facts.update(model=usage.get("model"), usage_source="reported", usage={"prompt_tokens": usage.get("input", 0) + usage.get("cache_write", 0), "completion_tokens": usage.get("output", 0), "cache_read_tokens": usage.get("cache_read", 0), "cache_write_tokens": usage.get("cache_write", 0)})
                facts["tool_calls"] = []
                for call in wire.get("tool_calls", []):
                    facts["tool_calls"].append({"id": call["id"], **call["function"]})
                    key = (record.get("run_id"), call["id"])
                    calls[key], arguments[key] = facts["call_id"], call["function"]["arguments"]
        elif kind == "tool_exec_end":
            ended.add(_reference(ref))
            tool_result = data.get("tool_result") or {}
            content = tool_result.get("content", "")
            if not isinstance(content, str):
                content = json.dumps(content, ensure_ascii=False, separators=(",", ":"))
            facts.update(type="tool_result", tool_name=data.get("tool"), tool_call_id=data.get("tool_id"), request_id=calls.get(call_key, ""), arguments=arguments.get(call_key, ""), result=content, ok=not data.get("is_error", False), metadata=tool_result.get("details"))
        elif kind == "context_projected":
            projections[execution_id] += 1
            facts.update(type=kind, projection_no=projections[execution_id], items=data.get("context_items", []))
        elif kind == "context_compacted":
            c = data.get("compaction") or {}
            facts.update(type=kind)
            for key in ("Reason", "Committed", "TokensBefore", "TokensAfter", "MessagesBefore", "MessagesAfter", "Summarized"):
                facts[re.sub(r"(?<!^)(?=[A-Z])", "_", key).lower()] = c.get(key)
        else:
            continue
        result.append(facts)
    for key, stage in refs.items():
        if key[0] == session_id and _finished(stage) and key not in ended:
            gaps.append(f"missing call result: {key}")
    return result


def read_records(path: Path) -> tuple[list[dict], list[str]]:
    records, gaps = _read_manifest(path)
    start = next((r for r in records if r.get("type") == "session_start"), {})
    if start.get("schema_version") != 13:
        gaps.append(f"unsupported review schema: {start.get('schema_version')}")
        return [], gaps
    snapshot = _timeline(path.with_suffix(".timeline"), gaps)
    if snapshot:
        records = [r for r in records if r.get("type") != "timeline_snapshot"] + [snapshot]
    stages = timeline_stages(records)
    loops = sorted(path.with_suffix(".sessions").glob("*.jsonl"))
    if loops and not any(r.get("type") == "timeline_snapshot" for r in records):
        gaps.append("missing review timeline")
    found = {loop.name for loop in loops}
    expected = {stage.get("attributes", {}).get("session_file") for stage in stages.values()}
    gaps.extend(f"missing local Session: {name}" for name in sorted(expected - found - {None, ""}))
    for loop in loops:
        records.extend(_loop_records(loop, stages, gaps))
    return records, gaps


def _finished(stage: dict) -> bool:
    end = stage.get("finished_at")
    return bool(end and not end.startswith("0001-01-01T00:00:00"))


def timeline_stages(records: list[dict]) -> dict[str, dict]:
    """Read the latest complete snapshot in the Session's append order."""
    stages: dict[str, dict] = {}
    timeline_id = None
    for record in records:
        if record.get("type") != "timeline_snapshot":
            continue
        snapshot = record["snapshot"]
        identity = record["timeline_id"]
        if not identity or snapshot["id"] != identity or timeline_id not in (None, identity):
            raise ValueError("inconsistent timeline ID")
        timeline_id = identity
        stages = {stage["id"]: stage for stage in snapshot.get("stages", [])}
    return stages


def execution_facts(records: list[dict]) -> dict[str, dict]:
    """Outcome comes from completed execution stages, never generic success."""
    stages = timeline_stages(records)
    result = {}
    for identity, stage in stages.items():
        if stage.get("name") != "execution":
            continue
        facts = dict(stage.get("attributes") or {})
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
            if record.get("type") == "session_start" and record.get("schema_version") != 13:
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

    @property
    def completion(self) -> dict:
        """Business coverage as sealed by CCR, independent of recording closure."""
        if self.end is None:
            return {"status": "running"}
        completion = self.end.get("completion")
        if isinstance(completion, dict) and completion.get("status"):
            return completion
        return {"status": "unknown"}

    def costs(self) -> dict:
        # Request identity is the stage ID, shared by its request and terminal
        # content. A scope/debrief is neither necessary nor sufficient for cost.
        calls: dict[str, dict] = {}
        for number, record in enumerate(self.records):
            kind = record.get("type")
            if kind not in ("llm_request", "llm_response", "llm_error"):
                continue
            identity = record.get("call_id") or record.get("stage_id") or record.get("uuid") or f"unassociated:{number}"
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

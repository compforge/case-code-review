"""Build linked Unit evidence for an offline case diagnosis, without judging it."""
from __future__ import annotations

import json
from pathlib import Path
import re
import subprocess

from spec_case import spec
from agent_ledger.observation import SessionView
from eval.eval_snapshot import canonical_sha256, review_sha256
from eval.session_recording import Recording


def call_nodes(nodes: list[dict]):
    for node in nodes:
        if node.get("kind") in ("model_call", "tool_call"):
            yield node
        yield from call_nodes(node.get("nodes", []))


def message_text(message: dict) -> tuple[str | None, str, str]:
    """Inspect the recorded codec envelope; leave unrecognized role placement unknown."""
    value = message.get("value", message)
    content = value.get("content", "")
    if isinstance(content, list):
        text = "\n".join(block.get("text", "") for block in content if isinstance(block, dict))
    elif isinstance(content, str):
        text = content
    else:
        text = json.dumps(content, ensure_ascii=False)
    return value.get("role"), text, message.get("type", "native")


@spec("Context outlines preserve message order and native evidence; excerpts and headings are not reconstructed prompts or causal judgments")
def context_outline(node: dict) -> dict:
    request = (node.get("request") or {}).get("input")
    outline = {"node_id": node["id"], "session": node.get("session"),
               "evidence": node.get("evidence", []), "messages": []}
    if not isinstance(request, dict) or not isinstance(request.get("messages"), list):
        outline["issue"] = "recorded request messages unavailable"
        return outline
    for index, message in enumerate(request["messages"]):
        role, text, native_type = message_text(message)
        headings = [line.strip() for line in text.splitlines()
                    if re.match(r"^\s*#{1,6}\s+", line)]
        outline["messages"].append({"index": index, "role": role, "native_type": native_type,
                                    "characters": len(text), "excerpt": text[:160],
                                    "excerpt_truncated": len(text) > 160,
                                    "headings": headings})
    # Tools remain at the request's tools location, outside messages.
    outline["tools"] = request.get("tools")
    return outline


def render_context(job: dict, contexts: list[dict]) -> str:
    lines = [f"# Context View · {job['job']['id']}", "",
             "Recorded requests; excerpts below are abbreviated. Exact requests and responses remain in the linked JobView JSON.", ""]
    for context in contexts:
        lines.extend([f"## ModelCall {context['node_id']}",
                      f"Session: {json.dumps(context['session'], ensure_ascii=False)}", ""])
        if context.get("issue"):
            lines.extend([context["issue"], ""])
            continue
        tools = context.get("tools")
        lines.append(f"Request tools (outside messages): {len(tools) if isinstance(tools, list) else 'unknown'}")
        for message in context["messages"]:
            lines.append(f"[{message['index']}] {message['role'] or 'unknown role'} · {message['native_type']} · {message['characters']} characters")
            for heading in message["headings"]:
                lines.append("  section: " + heading)
            lines.append("  excerpt: " + json.dumps(message["excerpt"], ensure_ascii=False)
                         + (" [display truncated]" if message["excerpt_truncated"] else ""))
        lines.append("")
    return "\n".join(lines)


def build_case_evidence(session: Path, unit_ids: list[str], out: Path, ccr: str) -> dict:
    """Use CCR's existing Job membership; never infer it from Lane proximity."""
    snapshot = review_sha256(session)
    recording = Recording.read(session)
    issues = list(recording.gaps)
    for record in recording.records:
        if record.get("artifact_kind") == "recording_gap":
            issues.append(f"review event {record.get('seq')}: local Session recording failed: "
                          + str((record.get("data") or {}).get("error", "unknown")))
    if recording.end is None:
        issues.append("missing session_end; review outcome remains unobserved")
    views = []
    for unit in dict.fromkeys(unit_ids):
        identity = canonical_sha256([snapshot, unit])[:20]
        completed = subprocess.run([ccr, "export", "--format", "job", "--unit", unit, str(session)],
                                   capture_output=True, text=True, timeout=120, check=True)
        job = json.loads(completed.stdout)
        if job.get("schema_version") != "agent-ledger/job@1" or job.get("attributes", {}).get("unit_id") != unit:
            raise ValueError(f"invalid or mismatched JobView for {unit}")
        calls = list(call_nodes(job.get("nodes", [])))
        contexts = [context_outline(node) for node in calls if node["kind"] == "model_call"]
        out.mkdir(parents=True, exist_ok=True)
        job_path, context_path = out / f"{identity}.job.json", out / f"{identity}.context.json"
        job_path.write_text(json.dumps(job, ensure_ascii=False, indent=2))
        context_path.write_text(json.dumps(contexts, ensure_ascii=False, indent=2))
        text_path = out / f"{identity}.context.md"
        text_path.write_text(render_context(job, contexts))
        sessions = {}
        for node in calls:
            ref = node.get("session")
            if not ref:
                continue
            key = (ref["source"], ref["id"])
            sessions.setdefault(key, {"session": ref, "nodes": []})["nodes"].append(node)
        session_paths = []
        for fragment in sessions.values():
            view = SessionView.model_validate({
                **fragment, "issues": job.get("issues", []),
                "attributes": {"selection": "Job-owned calls only, not the entire Session",
                               "job": job["job"], "ordering": "grouping does not establish dependency"}})
            name = canonical_sha256([snapshot, job["job"], fragment["session"]])[:20]
            path = out / f"{name}.session.json"
            path.write_text(view.model_dump_json(indent=2))
            session_paths.append(str(path.resolve()))
        views.append({"unit_id": unit, "job": job["job"], "status": job.get("status"),
                      "issues": job.get("issues", []), "job_view": str(job_path.resolve()),
                      "context_view": str(context_path.resolve()), "context_text": str(text_path.resolve()),
                      "session_views": session_paths,
                      "session_calls": [{key: node.get(key) for key in ("session", "id", "kind", "evidence")}
                                        for node in calls]})
    return {"session_sha256": snapshot, "review_id": recording.start.get("sessionId"),
            "producer_version": recording.start.get("tool_version"), "target_head": recording.start.get("git_head"),
            "recording_closed": recording.end is not None, "issues": issues,
            "review_timing": {"started_at": recording.start.get("timestamp"),
                              "finished_at": (recording.end or {}).get("timestamp")},
            "review_cost": {"scope": "whole recording, not attributable to one Unit", **recording.costs()},
            "units": views}

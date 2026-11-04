"""Builders for domain evaluation cases; wire-format tests use raw ATIF documents."""

from copy import deepcopy
import json


def session_documents(review: dict) -> list[dict]:
    documents = []
    for session in review["sessions"]:
        document = deepcopy(session)
        document["schema_version"] = "ATIF-v1.8"
        document.setdefault(
            "agent",
            review.get("agent", {"name": "case-code-review", "version": "test"}),
        )
        document.setdefault("session_id", document["trajectory_id"])
        document["extra"] = {**review.get("extra", {}), **document.get("extra", {})}
        document["extra"].setdefault("review_id", review.get("session_id", "review"))
        document["extra"].setdefault("scope_id", document["trajectory_id"])
        for index, step in enumerate(document["steps"], 1):
            step["step_id"] = index
            step.setdefault("message", "")
            if step.get("source") == "agent":
                step.setdefault("llm_call_count", 1)
            for call in step.get("tool_calls", []):
                call.setdefault("arguments", {})
            for result in (step.get("observation") or {}).get("results", []):
                result.setdefault("extra", {}).setdefault("ok", True)
        documents.append(document)
    return documents


def session_jsonl(review: dict) -> str:
    return "\n".join(json.dumps(item) for item in session_documents(review))

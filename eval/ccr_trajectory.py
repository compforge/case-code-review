"""CCR ATIF projection and deterministic trajectory evaluators.

ATIF is the persisted/export boundary.  This module is the only place that
knows its shape; judges consume ``trajectory_harness.Trajectory`` instead.
"""

from __future__ import annotations

import json
from collections import Counter
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any

from trajectory_harness import Evaluation, Step, Trajectory


class ATIFTrajectoryLoader:
    """Project CCR's ATIF root records into one canonical trajectory per scope."""

    def load(self, source: str | Path) -> list[Trajectory]:
        return self.loads(Path(source).read_text(encoding="utf-8"), source=str(source))

    def loads(self, text: str, *, source: str = "") -> list[Trajectory]:
        roots = [json.loads(line) for line in text.splitlines() if line.strip()]
        trajectories = []
        for root in roots:
            root_meta = {
                "session_id": root.get("session_id"),
                "agent": root.get("agent") or {},
                **(root.get("extra") or {}),
            }
            for chain in root.get("subagent_trajectories") or []:
                trajectories.append(self._chain(chain, root_meta, source))
        return trajectories

    def _chain(
        self, chain: dict[str, Any], root_meta: dict[str, Any], source: str
    ) -> Trajectory:
        steps: list[Step] = []
        for raw in chain.get("steps") or []:
            step_id = str(raw.get("step_id", len(steps) + 1))
            start_ms = _timestamp_ms(raw.get("timestamp"))
            metrics = raw.get("metrics") or {}
            duration_ms = float((metrics.get("extra") or {}).get("duration_ms") or 0)
            source_role = raw.get("source") or ""
            if source_role != "agent":
                steps.append(
                    Step(
                        step_id=step_id,
                        parent_step_id=None,
                        operation="context",
                        name=source_role or "context",
                        start_ms=start_ms,
                        duration_ms=0,
                        output_messages=(_message(source_role, raw.get("message")),),
                        attributes=dict(raw.get("extra") or {}),
                    )
                )
                continue

            calls = raw.get("tool_calls") or []
            steps.append(
                Step(
                    step_id=step_id,
                    parent_step_id=None,
                    operation="inference",
                    name=str(raw.get("model_name") or "model"),
                    start_ms=start_ms,
                    duration_ms=duration_ms,
                    status="error" if (raw.get("extra") or {}).get("llm_error") else "",
                    output_messages=(_assistant_message(raw.get("message"), calls),),
                    attributes={
                        **dict(raw.get("extra") or {}),
                        "prompt_tokens": metrics.get("prompt_tokens", 0),
                        "completion_tokens": metrics.get("completion_tokens", 0),
                        "cached_tokens": metrics.get("cached_tokens", 0),
                    },
                )
            )
            results = (raw.get("observation") or {}).get("results") or []
            result_by_id = {
                result.get("source_call_id"): result
                for result in results
                if result.get("source_call_id")
            }
            for index, call in enumerate(calls, start=1):
                result = result_by_id.get(call.get("tool_call_id"))
                if result is None and index <= len(results):
                    result = results[index - 1]
                result = result or {}
                extra = result.get("extra") or {}
                name = str(call.get("function_name") or extra.get("tool_name") or "")
                arguments = call.get("arguments")
                if arguments is None:
                    arguments = _json_value(extra.get("arguments"))
                call_id = call.get("tool_call_id") or result.get("source_call_id")
                steps.append(
                    Step(
                        step_id=f"{step_id}:tool:{index}",
                        parent_step_id=step_id,
                        operation="execute_tool",
                        name=name,
                        start_ms=start_ms + duration_ms,
                        duration_ms=0,
                        status="error" if extra.get("ok") is False else "",
                        input_messages=(_tool_call_message(call_id, name, arguments),),
                        output_messages=(
                            _tool_result_message(call_id, result.get("content")),
                        ),
                        attributes={"ok": extra.get("ok", True)},
                    )
                )

        metadata = {
            **root_meta,
            **(chain.get("extra") or {}),
            "final_metrics": chain.get("final_metrics") or {},
            "format": "atif",
        }
        return Trajectory(
            trajectory_id=str(chain.get("trajectory_id") or ""),
            steps=tuple(steps),
            source=source,
            metadata=metadata,
        )


@dataclass(frozen=True, slots=True)
class ToolFailureEvaluator:
    name: str = "tool_success"
    weight: float = 1.0

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        calls = _tool_steps(trajectory)
        if not calls:
            return _not_evaluated(self.name, "Trajectory contains no tool calls.")
        failed = [step.step_id for step in calls if step.status == "error"]
        return _ratio_evaluation(
            self.name,
            len(calls) - len(failed),
            len(calls),
            failed,
            "tool calls succeeded",
        )


@dataclass(frozen=True, slots=True)
class EmptySearchEvaluator:
    name: str = "non_empty_search"
    weight: float = 1.0
    minimum_result_chars: int = 32

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        searches = [step for step in _tool_steps(trajectory) if step.name == "code_search"]
        if not searches:
            return _not_evaluated(self.name, "Trajectory contains no code_search calls.")
        empty = [
            step.step_id
            for step in searches
            if len(_tool_response(step).strip()) < self.minimum_result_chars
        ]
        return _ratio_evaluation(
            self.name,
            len(searches) - len(empty),
            len(searches),
            empty,
            "searches returned a non-empty result",
        )


@dataclass(frozen=True, slots=True)
class UnitCompletionEvaluator:
    """A review scope is complete only after the model calls task_done."""

    name: str = "unit_completion"
    weight: float = 1.0

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        calls = _tool_steps(trajectory)
        if not calls and not any(step.operation == "inference" for step in trajectory.steps):
            return _not_evaluated(self.name, "Trajectory contains no model execution.")
        done = [step.step_id for step in calls if step.name == "task_done"]
        return Evaluation(
            name=self.name,
            score=1.0 if done else 0.0,
            label="pass" if done else "fail",
            explanation=(
                "Review scope called task_done."
                if done
                else "Review scope ended without task_done."
            ),
            step_ids=tuple(done),
        )


def repeated_file_reads(trajectory: Trajectory) -> dict[str, int]:
    """Count path-level repeats even when line ranges differ."""

    reads: Counter[str] = Counter()
    for step in _tool_steps(trajectory):
        if step.name != "file_read":
            continue
        arguments = _tool_arguments(step)
        paths = arguments.get("file_paths") or [arguments.get("file_path", "?")]
        if isinstance(paths, str):
            paths = [paths]
        reads.update(str(path) for path in paths)
    return {path: count for path, count in reads.items() if count > 1}


def tool_frequencies(trajectory: Trajectory) -> dict[str, int]:
    return dict(Counter(step.name for step in _tool_steps(trajectory)))


def _tool_steps(trajectory: Trajectory) -> list[Step]:
    return [step for step in trajectory.steps if step.operation == "execute_tool"]


def _tool_arguments(step: Step) -> dict[str, Any]:
    for message in step.input_messages:
        for part in message.get("parts") or []:
            if part.get("type") == "tool_call":
                value = part.get("arguments")
                return value if isinstance(value, dict) else {}
    return {}


def _tool_response(step: Step) -> str:
    for message in step.output_messages:
        for part in message.get("parts") or []:
            if part.get("type") == "tool_call_response":
                value = part.get("response")
                return value if isinstance(value, str) else json.dumps(value)
    return ""


def _not_evaluated(name: str, explanation: str) -> Evaluation:
    return Evaluation(name=name, score=None, label="not_evaluated", explanation=explanation)


def _ratio_evaluation(
    name: str,
    passed: int,
    total: int,
    failed_step_ids: list[str],
    description: str,
) -> Evaluation:
    score = round(passed / total, 3)
    return Evaluation(
        name=name,
        score=score,
        label="pass" if passed == total else "fail",
        explanation=f"{passed} of {total} {description}.",
        step_ids=tuple(failed_step_ids),
    )


def _timestamp_ms(value: Any) -> float:
    if not value:
        return 0
    try:
        return datetime.fromisoformat(str(value).replace("Z", "+00:00")).timestamp() * 1000
    except ValueError:
        return 0


def _message(role: str, content: Any) -> dict[str, Any]:
    return {"role": role, "parts": [{"type": "text", "content": content or ""}]}


def _assistant_message(content: Any, calls: list[dict[str, Any]]) -> dict[str, Any]:
    parts = []
    if content:
        parts.append({"type": "text", "content": content})
    parts.extend(
        {
            "type": "tool_call",
            "id": call.get("tool_call_id"),
            "name": call.get("function_name") or "",
            "arguments": call.get("arguments") or {},
        }
        for call in calls
    )
    return {"role": "assistant", "parts": parts}


def _tool_call_message(
    call_id: Any, name: str, arguments: Any
) -> dict[str, Any]:
    return {
        "role": "assistant",
        "parts": [
            {
                "type": "tool_call",
                "id": call_id,
                "name": name,
                "arguments": arguments or {},
            }
        ],
    }


def _tool_result_message(call_id: Any, response: Any) -> dict[str, Any]:
    return {
        "role": "tool",
        "parts": [
            {"type": "tool_call_response", "id": call_id, "response": response or ""}
        ],
    }


def _json_value(value: Any) -> Any:
    if not isinstance(value, str):
        return value
    try:
        return json.loads(value)
    except json.JSONDecodeError:
        return {"raw": value}

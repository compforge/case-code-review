"""CCR evaluation accessors over canonical ATIF v1.8 facts."""

from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any

from atif import ContentPart, ObservationResult, Step, ToolCall, Trajectory
from trajectory_harness import (
    ATIFJsonLoader,
    ExecutionResult,
    Failure,
    MeasurerSpec,
    MeasurementSpec,
    MeasurementResult,
)
from trajectory_harness.model import step_duration_ms, step_failure, step_operation


class ATIFTrajectoryLoader:
    """Validate native ATIF and attach only non-standard evaluation provenance."""

    def load(self, source: str | Path) -> list[Trajectory]:
        return self.loads(Path(source).read_text(encoding="utf-8"), source=str(source))

    # +spec=ATIF standard fields and step identities survive loading without synthetic tool steps
    # +link=repo://eval/trajectory/README.md
    def loads(self, text: str, *, source: str = "") -> list[Trajectory]:
        trajectories = ATIFJsonLoader().loads(text, source=source)
        for trajectory in trajectories:
            for step in trajectory.steps:
                extra = dict(step.extra or {})
                extension = dict(extra.get("quality_harness") or {})
                if "request_messages" in extra:
                    extension["input_messages"] = extra["request_messages"]
                duration = (
                    (step.metrics.extra or {}).get("duration_ms")
                    if step.metrics
                    else None
                )
                duration = extra.get("duration_ms", duration)
                if duration is not None:
                    extension["duration_ms"] = duration
                if step.timestamp:
                    extension["start_ms"] = (
                        datetime.fromisoformat(
                            step.timestamp.replace("Z", "+00:00")
                        ).timestamp()
                        * 1000
                    )
                failure = _failure_from_value(extra.get("failure"))
                if failure:
                    extension["failure"] = failure.to_dict()
                    extension["status"] = "error"
                elif extra.get("llm_error"):
                    extension["status"] = "error"
                if extension:
                    step.extra = {**extra, "quality_harness": extension}
            extra = dict(trajectory.extra or {})
            extension = dict(extra.get("quality_harness") or {})
            metadata = {
                key: value for key, value in extra.items() if key != "quality_harness"
            }
            executions = metadata.get("executions") or []
            if executions:
                outcomes = [item.get("outcome") or "unknown" for item in executions]
                metadata["execution_outcome"] = next(
                    (value for value in outcomes if value != "completed"), "completed"
                )
                metadata["execution_reason"] = "; ".join(
                    str(item.get("reason") or "")
                    for item in executions
                    if item.get("outcome") != "completed"
                )
            extension["metadata"] = {**extension.get("metadata", {}), **metadata}
            extension["generation"] = _generation_provenance(
                {**metadata, "agent": trajectory.agent.model_dump(exclude_none=True)},
                trajectory.steps,
            )
            execution = _execution_result(metadata, trajectory.steps)
            if execution:
                extension["execution"] = execution.to_dict()
            trajectory.extra = {**extra, "quality_harness": extension}
        return trajectories


@dataclass(frozen=True, slots=True)
class ToolObservation:
    """A derived call/result association; evidence points to the original ATIF step."""

    step: Step
    call: ToolCall
    results: tuple[ObservationResult, ...]
    index: int

    @property
    def key(self) -> str:
        return f"{self.step.step_id}:{self.index}"

    @property
    def failed(self) -> bool:
        return any((result.extra or {}).get("ok") is False for result in self.results)

    @property
    def succeeded(self) -> bool:
        return bool(self.results) and all(
            (result.extra or {}).get("ok") is True for result in self.results
        )

    @property
    def text(self) -> str:
        return "\n".join(content_text(result.content) for result in self.results)


def content_text(content: str | list[ContentPart] | None) -> str:
    """Extract recorded text only; media references are not a transcription."""
    if isinstance(content, str):
        return content
    return "\n".join(
        part.text
        for part in content or ()
        if part.type == "text" and part.text is not None
    )


# +spec=Only a unique matching call ID associates an observed result; missing or conflicting evidence is not success
# +link=repo://eval/trajectory/README.md
def tool_observations(trajectory: Trajectory) -> list[ToolObservation]:
    observed = []
    for step in trajectory.steps:
        calls = step.tool_calls or []
        results = step.observation.results if step.observation else []
        for index, call in enumerate(calls):
            if (
                sum(candidate.tool_call_id == call.tool_call_id for candidate in calls)
                != 1
            ):
                continue
            matched = tuple(
                result
                for result in results
                if result.source_call_id == call.tool_call_id
            )
            if matched:
                observed.append(ToolObservation(step, call, matched, index))
    return observed


def _failure_from_value(value: Any) -> Failure | None:
    if not isinstance(value, dict):
        return None
    kind = str(value.get("kind") or "")
    phase = str(value.get("phase") or "")
    error_type = str(value.get("error_type") or "")
    if not kind or not phase or not error_type:
        return None
    return Failure(
        kind=kind,
        phase=phase,
        error_type=error_type,
        code=str(value.get("code") or ""),
        message=str(value.get("message") or ""),
    )


def _generation_provenance(
    root_meta: dict[str, Any], steps: list[Step]
) -> dict[str, str]:
    agent = root_meta.get("agent") or {}
    name = str(agent.get("name") or "")
    version = str(agent.get("version") or "")
    generation = {}
    if name:
        generation["agent_revision"] = (
            f"{name}@{version}" if version and version != "unknown" else name
        )
    models = sorted(
        {
            step.model_name
            for step in steps
            if step_operation(step) == "inference" and step.model_name
        }
    )
    configured_model = str(agent.get("model_name") or "")
    if models:
        generation["model"] = ",".join(models)
    elif configured_model:
        generation["model"] = configured_model
    loop_config = {
        key: root_meta[key] for key in ("features", "params") if root_meta.get(key)
    }
    if loop_config:
        generation["loop_config"] = json.dumps(
            loop_config,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        )
    return generation


def _execution_result(
    metadata: dict[str, Any], steps: list[Step]
) -> ExecutionResult | None:
    raw_outcome = str(metadata.get("execution_outcome") or "")
    if not raw_outcome:
        return None

    outcome = {
        "completed": "completed",
        "timeout": "timeout",
        "aborted": "canceled",
        "canceled": "canceled",
        "unknown": "unknown",
    }.get(raw_outcome, "failed")
    reason = str(metadata.get("execution_reason") or "")
    failure = None
    if raw_outcome == "timeout":
        failure = Failure(
            kind="workflow", phase="", error_type="timeout", message=reason
        )
    elif raw_outcome == "llm_error":
        failure = next(
            (
                step_failure(step)
                for step in reversed(steps)
                if step_failure(step) is not None
            ),
            Failure(
                kind="llm",
                phase="unknown",
                error_type="unknown",
                message=reason,
            ),
        )
    return ExecutionResult(
        outcome=outcome,
        duration_ms=sum(
            float(item.get("duration_ms") or 0)
            for item in metadata.get("executions", [])
        )
        if metadata.get("executions")
        else float(
            metadata.get("execution_duration_ms")
            or sum(step_duration_ms(step) for step in steps)
        ),
        failure=failure,
    )


@dataclass(frozen=True, slots=True)
class CCRToolUsageMeasurer:
    """Measure recorded tool outcomes using CCR's explicit result metadata."""

    spec: MeasurerSpec = MeasurerSpec(
        measurer_id="tool_usage",
        title="Tool usage",
        description="Count native tool outcomes, including multiple calls within one ATIF step.",
        category="cost",
        owner="case-code-review",
        measurements=(
            MeasurementSpec(
                "tool_call_count",
                "count",
                "Uniquely associated recorded tool executions.",
                "neutral",
                ("sum", "mean"),
            ),
            MeasurementSpec(
                "failed_tool_call_count",
                "count",
                "Executions with an explicit failure result.",
                "lower_is_better",
                ("sum", "mean"),
            ),
            MeasurementSpec(
                "result_bytes",
                "byte",
                "Size of recorded ATIF result content.",
                "neutral",
                ("sum", "mean"),
            ),
            MeasurementSpec(
                "status_reported_call_count",
                "count",
                "Executions with explicit success or failure.",
                "neutral",
                ("sum",),
            ),
            MeasurementSpec(
                "tool_failure_ratio",
                "ratio",
                "Failures divided by executions with known status.",
                "lower_is_better",
                ("mean",),
            ),
        ),
    )

    def measure(self, trajectory: Trajectory) -> MeasurementResult:
        calls = tool_observations(trajectory)
        if not calls:
            return MeasurementResult(
                measurer_id=self.spec.measurer_id,
                status="not_applicable",
                explanation="No uniquely associated tool outcomes.",
            )
        known = [call for call in calls if call.failed or call.succeeded]
        failed = sum(call.failed for call in calls)
        values = {
            "tool_call_count": len(calls),
            "failed_tool_call_count": failed,
            "status_reported_call_count": len(known),
            "result_bytes": sum(
                len(
                    json.dumps(
                        [
                            result.model_dump(exclude_none=True)
                            for result in call.results
                        ],
                        ensure_ascii=False,
                    ).encode()
                )
                for call in calls
            ),
        }
        if known:
            values["tool_failure_ratio"] = round(failed / len(known), 6)
        return MeasurementResult(
            measurer_id=self.spec.measurer_id,
            status="measured",
            measurements=values,
            explanation=f"Observed {len(calls)} tool executions; {len(known)} reported status.",
            step_ids=tuple(dict.fromkeys(str(call.step.step_id) for call in calls)),
        )

"""CCR ATIF projection and deterministic trajectory evaluators.

ATIF is the persisted/export boundary.  This module is the only place that
knows its shape; judges consume ``trajectory_harness.Trajectory`` instead.
"""

from __future__ import annotations

import json
import re
from collections import Counter
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any

from trajectory_harness import Evaluation, Step, Trajectory

REVIEW1 = "review1"
REVIEW2 = "review2"
UNKNOWN_STAGE = "unknown"


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
class FileReadCoverageEvaluator:
    """Score how much file_read output adds coverage not seen earlier."""

    name: str = "file_read_coverage"
    # Coverage is diagnostic: adding it must not silently rebaseline the
    # overall quality score produced by the established evaluators.
    weight: float = 0.0

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        covered: dict[str, list[tuple[int, int]]] = {}
        total_lines = 0
        novel_lines = 0
        overlapping_steps: list[str] = []

        for step in _tool_steps(trajectory):
            if step.name != "file_read" or step.status == "error":
                continue
            read_range = _file_read_range(step)
            if read_range is None:
                continue
            path, start, end = read_range
            delivered = end - start + 1
            prior = covered.setdefault(path, [])
            overlap = _covered_lines(prior, start, end)
            if overlap:
                overlapping_steps.append(step.step_id)
            total_lines += delivered
            novel_lines += delivered - overlap
            prior.append((start, end))
            covered[path] = _merge_ranges(prior)

        if total_lines == 0:
            return _not_evaluated(
                self.name, "Trajectory contains no successful ranged file_read output."
            )
        score = round(novel_lines / total_lines, 3)
        return Evaluation(
            name=self.name,
            score=score,
            label="pass" if novel_lines == total_lines else "fail",
            explanation=(
                f"{novel_lines} of {total_lines} returned file lines added new coverage."
            ),
            step_ids=tuple(overlapping_steps),
        )


@dataclass(frozen=True, slots=True)
class PromptFileCoverageEvaluator:
    """Measure file_read content already visible in initial File messages."""

    name: str = "file_read_prompt_novelty"
    weight: float = 0.0

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        overlap = prompt_file_read_overlap(trajectory)
        if overlap["total_lines"] == 0:
            return _not_evaluated(
                self.name, "Trajectory contains no successful ranged file_read output."
            )
        novel = overlap["total_lines"] - overlap["covered_lines"]
        score = round(novel / overlap["total_lines"], 3)
        return Evaluation(
            name=self.name,
            score=score,
            label="pass" if overlap["covered_lines"] == 0 else "fail",
            explanation=(
                f"{overlap['covered_lines']} of {overlap['total_lines']} returned file "
                "lines were already visible in initial File messages."
            ),
            step_ids=tuple(overlap["overlapping_steps"]),
        )


@dataclass(frozen=True, slots=True)
class FileReadFragmentationEvaluator:
    """Detect adjacent or overlapping ranges that fit in fewer reads."""

    name: str = "file_read_fragmentation"
    weight: float = 0.0

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        fragmentation = file_read_fragmentation(trajectory)
        calls = fragmentation["calls"]
        if calls == 0:
            return _not_evaluated(self.name, "Trajectory contains no ranged file_read output.")
        merged = fragmentation["minimal_ranges"]
        return Evaluation(
            name=self.name,
            score=round(merged / calls, 3),
            label="pass" if calls == merged else "fail",
            explanation=(
                f"{calls} file reads collapse to {merged} adjacent or overlapping ranges; "
                f"{fragmentation['mergeable_reads']} reads could merge if their targets "
                "were known upfront."
            ),
            step_ids=tuple(fragmentation["fragmented_steps"]),
        )


@dataclass(frozen=True, slots=True)
class HypothesisCompletionEvaluator:
    """A Review 1 Unit completes by successfully submitting Hypotheses."""

    name: str = "hypothesis_submission"
    weight: float = 1.0

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        calls = _tool_steps(trajectory)
        if not calls and not any(step.operation == "inference" for step in trajectory.steps):
            return _not_evaluated(self.name, "Trajectory contains no model execution.")
        submissions = [step for step in calls if step.name == "submit_hypotheses"]
        completed = [step.step_id for step in submissions if _hypotheses_completed(step)]
        return Evaluation(
            name=self.name,
            score=1.0 if completed else 0.0,
            label="pass" if completed else "fail",
            explanation=(
                "Review 1 submitted its Hypotheses."
                if completed
                else "Review 1 ended without a valid Hypothesis submission."
            ),
            step_ids=tuple(completed),
        )


@dataclass(frozen=True, slots=True)
class AssessmentCompletionEvaluator:
    """A Review 2 execution completes by submitting an Assessment."""

    name: str = "assessment_submission"
    weight: float = 1.0

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> Evaluation:
        del reference
        calls = _tool_steps(trajectory)
        if not calls and not any(step.operation == "inference" for step in trajectory.steps):
            return _not_evaluated(self.name, "Trajectory contains no model execution.")
        submissions = [step for step in calls if step.name == "submit_assessments"]
        if not submissions:
            return Evaluation(
                name=self.name,
                score=0.0,
                label="fail",
                explanation="Review 2 ended without submitting an Assessment.",
            )
        completed = [step for step in submissions if _assessment_completed(step)]
        failed = [step.step_id for step in submissions if step not in completed]
        return _ratio_evaluation(
            self.name,
            len(completed),
            len(submissions),
            failed,
            "Assessment submissions completed their Review 2 execution",
        )


def review_stage(trajectory: Trajectory) -> str:
    """Classify a CCR scope without relying on trajectory-id naming."""

    scope_kind = trajectory.metadata.get("scope_kind")
    if scope_kind == "unit":
        return REVIEW1
    if scope_kind == "lane":
        return REVIEW2
    if any(
        step.attributes.get("task_type") == "hypothesis_review_task"
        for step in trajectory.steps
        if step.operation == "inference"
    ):
        return REVIEW2
    return UNKNOWN_STAGE


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


def file_read_stats(trajectory: Trajectory) -> dict[str, float | int]:
    """Describe how file reads are spread across model turns."""

    reads = [step for step in _tool_steps(trajectory) if step.name == "file_read"]
    if not reads:
        return {"calls": 0, "rounds": 0, "average_batch": 0.0, "max_batch": 0}
    batches = Counter(step.parent_step_id for step in reads)
    return {
        "calls": len(reads),
        "rounds": len(batches),
        "average_batch": round(len(reads) / len(batches), 2),
        "max_batch": max(batches.values()),
    }


def prompt_file_read_overlap(trajectory: Trajectory) -> dict[str, Any]:
    """Classify reads by overlap with File messages supplied before the loop."""

    context_ranges = _context_file_ranges(trajectory)
    total_lines = 0
    covered_lines = 0
    fully_covered = 0
    partially_covered = 0
    new_context = 0
    runtime_covered = 0
    blocked = 0
    failed = 0
    unmeasured = 0
    overlapping_steps: list[str] = []
    for step in _tool_steps(trajectory):
        if step.name != "file_read":
            continue
        if step.status == "error":
            failed += 1
            continue
        available = _already_available_range(step)
        if available is not None:
            source, _, start, end = available
            if source == "initial":
                delivered = end - start + 1
                total_lines += delivered
                covered_lines += delivered
                fully_covered += 1
                overlapping_steps.append(step.step_id)
            else:
                runtime_covered += 1
            continue
        if _tool_response(step).startswith("Investigation is closed."):
            blocked += 1
            continue
        read_range = _file_read_range(step)
        if read_range is None:
            unmeasured += 1
            continue
        path, start, end = read_range
        delivered = end - start + 1
        overlap = _covered_lines(context_ranges.get(path, []), start, end)
        total_lines += delivered
        covered_lines += overlap
        if overlap == delivered:
            fully_covered += 1
            overlapping_steps.append(step.step_id)
        elif overlap:
            partially_covered += 1
            overlapping_steps.append(step.step_id)
        else:
            new_context += 1
    return {
        "calls": (
            fully_covered
            + partially_covered
            + new_context
            + runtime_covered
            + blocked
            + failed
            + unmeasured
        ),
        "fully_covered": fully_covered,
        "partially_covered": partially_covered,
        "new_context": new_context,
        "runtime_covered": runtime_covered,
        "blocked": blocked,
        "failed": failed,
        "unmeasured": unmeasured,
        "covered_lines": covered_lines,
        "total_lines": total_lines,
        "overlap_rate": round(covered_lines / total_lines, 3) if total_lines else 0.0,
        "overlapping_steps": overlapping_steps,
    }


def file_read_fragmentation(trajectory: Trajectory) -> dict[str, Any]:
    """Return the conservative merge potential of observed file ranges."""

    by_path: dict[str, list[tuple[int, int, str]]] = {}
    for step in _tool_steps(trajectory):
        if step.name != "file_read" or step.status == "error":
            continue
        read_range = _file_read_observed_range(step)
        if read_range is None:
            continue
        path, start, end = read_range
        by_path.setdefault(path, []).append((start, end, step.step_id))

    calls = sum(len(ranges) for ranges in by_path.values())
    minimal_ranges = 0
    fragmented_steps: list[str] = []
    for ranges in by_path.values():
        current_end = 0
        current_start = 0
        for start, end, step_id in sorted(ranges):
            mergeable = (
                current_start > 0
                and start <= current_end + 1
                and max(current_end, end) - current_start + 1 <= 500
            )
            if mergeable:
                current_end = max(current_end, end)
                fragmented_steps.append(step_id)
                continue
            minimal_ranges += 1
            current_start, current_end = start, end
    return {
        "calls": calls,
        "minimal_ranges": minimal_ranges,
        "mergeable_reads": calls - minimal_ranges,
        "fragmented_steps": fragmented_steps,
    }


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


def _file_read_range(step: Step) -> tuple[str, int, int] | None:
    response = _tool_response(step)
    path_match = re.search(r"(?m)^File:\s*(.+?)\s+\(Total lines:", response)
    range_match = re.search(r"(?m)^LINE_RANGE:\s*(\d+)-(\d+)\s*$", response)
    if not range_match:
        return None
    arguments = _tool_arguments(step)
    path = path_match.group(1) if path_match else arguments.get("file_path")
    if not path:
        return None
    start, end = map(int, range_match.groups())
    if start <= 0 or end < start:
        return None
    return str(path), start, end


def _file_read_observed_range(step: Step) -> tuple[str, int, int] | None:
    read_range = _file_read_range(step)
    if read_range is not None:
        return read_range
    available = _already_available_range(step)
    if available is None:
        return None
    _, path, start, end = available
    return path, start, end


def _already_available_range(step: Step) -> tuple[str, str, int, int] | None:
    match = re.match(
        r"Already available in the current context from "
        r"(the initial source context|an earlier file_read result): "
        r"(.+?) lines (\d+)-(\d+)\.",
        _tool_response(step),
    )
    if not match:
        return None
    source = "initial" if match.group(1).startswith("the initial") else "runtime"
    return source, match.group(2), int(match.group(3)), int(match.group(4))


def _context_file_ranges(trajectory: Trajectory) -> dict[str, list[tuple[int, int]]]:
    ranges: dict[str, list[tuple[int, int]]] = {}
    for step in trajectory.steps:
        if step.operation != "context":
            continue
        for message in step.output_messages:
            for part in message.get("parts") or []:
                content = part.get("content")
                if not isinstance(content, str):
                    continue
                lines = content.splitlines()
                if not lines:
                    continue
                header = re.match(r"^File:\s*(.+?)\s+\(Total lines:\s*(\d+)\)$", lines[0])
                if not header:
                    continue
                path, total = header.group(1), int(header.group(2))
                start, end = 1, total
                for line in lines[1:6]:
                    visible = re.match(r"^LINE_RANGE:\s*(\d+)-(\d+)$", line)
                    if visible:
                        start, end = map(int, visible.groups())
                        break
                    if re.match(r"^\d+\|", line):
                        break
                ranges.setdefault(path, []).append((start, end))
    return {path: _merge_ranges(items) for path, items in ranges.items()}


def _covered_lines(ranges: list[tuple[int, int]], start: int, end: int) -> int:
    return sum(max(0, min(end, right) - max(start, left) + 1) for left, right in ranges)


def _merge_ranges(ranges: list[tuple[int, int]]) -> list[tuple[int, int]]:
    merged: list[tuple[int, int]] = []
    for start, end in sorted(ranges):
        if not merged or start > merged[-1][1] + 1:
            merged.append((start, end))
            continue
        merged[-1] = (merged[-1][0], max(merged[-1][1], end))
    return merged


def _assessment_completed(step: Step) -> bool:
    try:
        result = json.loads(_tool_response(step))
    except json.JSONDecodeError:
        return False
    return bool(result.get("accepted")) and not result.get("remaining")


def _hypotheses_completed(step: Step) -> bool:
    return _tool_response(step).startswith("Unit review completed;")


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

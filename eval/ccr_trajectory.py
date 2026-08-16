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
from typing import Any, Callable

from trajectory_harness import (
    DiagnosticSignal,
    EvaluationResult,
    EvaluatorSpec,
    ExecutionResult,
    Failure,
    MeasurementSpec,
    Step,
    Trajectory,
)

REVIEW1 = "review1"
REVIEW2 = "review2"
UNKNOWN_STAGE = "unknown"


def _evaluator_spec(
    evaluator_id: str,
    title: str,
    description: str,
    measurements: tuple[MeasurementSpec, ...] = (),
) -> EvaluatorSpec:
    return EvaluatorSpec(
        evaluator_id=evaluator_id,
        title=title,
        description=description,
        kind="domain",
        owner="case-code-review",
        measurements=measurements,
    )


@dataclass(frozen=True, slots=True)
class ContextDemand:
    """One information need inferred from a trajectory step by an eval operator."""

    kind: str
    identity: str
    signal: str


ContextDemandExtractor = Callable[[Step], list[ContextDemand]]
_CONTEXT_DEMAND_EXTRACTORS: dict[str, ContextDemandExtractor] = {}


def register_context_demand_extractor(
    tool_name: str,
) -> Callable[[ContextDemandExtractor], ContextDemandExtractor]:
    """Register one tool-specific projection without teaching Harness the tool."""

    def register(extractor: ContextDemandExtractor) -> ContextDemandExtractor:
        _CONTEXT_DEMAND_EXTRACTORS[tool_name] = extractor
        return extractor

    return register


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
            attributes = {
                **dict(raw.get("extra") or {}),
                "prompt_tokens": metrics.get("prompt_tokens", 0),
                "completion_tokens": metrics.get("completion_tokens", 0),
                "cached_tokens": metrics.get("cached_tokens", 0),
            }
            if reasoning := raw.get("reasoning_content"):
                attributes["reasoning_content"] = reasoning
            failure = _failure_from_value(attributes.get("failure"))
            if failure is None:
                failure = _legacy_llm_failure(str(attributes.get("llm_error") or ""))
            steps.append(
                Step(
                    step_id=step_id,
                    parent_step_id=None,
                    operation="inference",
                    name=str(raw.get("model_name") or "model"),
                    start_ms=start_ms,
                    duration_ms=duration_ms,
                    status="error"
                    if failure is not None or attributes.get("llm_error")
                    else "",
                    failure=failure,
                    output_messages=(_assistant_message(raw.get("message"), calls),),
                    attributes=attributes,
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
                        attributes={**extra, "ok": extra.get("ok", True)},
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
            execution=_execution_result(metadata, steps),
            source=source,
            metadata=metadata,
        )


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


def _legacy_llm_failure(message: str) -> Failure | None:
    # Older CCR sessions only persisted the router's stable error prefix. Keep
    # this exact match at the source Loader boundary; unknown text stays unknown.
    if not message.startswith("llm routing call timeout exceeded"):
        return None
    return Failure(
        kind="llm",
        phase="routing",
        error_type="timeout",
        code="routing_budget_exhausted",
        message=message,
    )


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
            (step.failure for step in reversed(steps) if step.failure is not None),
            Failure(
                kind="llm",
                phase="unknown",
                error_type="unknown",
                message=reason,
            ),
        )
    return ExecutionResult(outcome=outcome, failure=failure)


@dataclass(frozen=True, slots=True)
class ToolFailureEvaluator:
    spec: EvaluatorSpec = _evaluator_spec(
        "tool_success",
        "Tool success",
        "Measure successful tool executions in one CCR trajectory.",
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        calls = _tool_steps(trajectory)
        if not calls:
            return _not_evaluated(
                self.spec.evaluator_id, "Trajectory contains no tool calls."
            )
        failed = [step.step_id for step in calls if step.status == "error"]
        return _ratio_evaluation(
            self.spec.evaluator_id,
            len(calls) - len(failed),
            len(calls),
            failed,
            "tool calls succeeded",
        )


@dataclass(frozen=True, slots=True)
class SearchScopeEvaluator:
    """Reject failed searches and empty scopes without penalizing valid absence."""

    spec: EvaluatorSpec = _evaluator_spec(
        "search_scope_validity",
        "Search scope validity",
        "Measure whether code searches execute against a valid non-empty scope.",
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        observations = _code_search_observations(trajectory)
        if not observations:
            return _not_evaluated(
                self.spec.evaluator_id,
                "Trajectory contains no search_code calls.",
            )
        evaluated = [item for item in observations if item["outcome"] != "scope_unknown"]
        if not evaluated:
            return _not_evaluated(
                self.spec.evaluator_id,
                "Searches returned legacy or unmeasured empty scopes.",
            )
        failed = [
            item["step_id"]
            for item in evaluated
            if item["outcome"] in {"scope_miss", "tool_failure"}
        ]
        return _ratio_evaluation(
            self.spec.evaluator_id,
            len(evaluated) - len(failed),
            len(evaluated),
            failed,
            "searches executed against a non-empty scope",
        )


@dataclass(frozen=True, slots=True)
class FileReadCoverageEvaluator:
    """Score how much read_files output adds coverage not seen earlier."""

    spec: EvaluatorSpec = _evaluator_spec(
        "file_read_coverage",
        "File read coverage",
        "Measure how much read_files output adds previously unseen line coverage.",
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        covered: dict[str, list[tuple[int, int]]] = {}
        total_lines = 0
        novel_lines = 0
        overlapping_steps: list[str] = []

        for step in _tool_steps(trajectory):
            if step.name != "read_files" or step.status == "error":
                continue
            for path, start, end in _file_read_ranges(step):
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
                self.spec.evaluator_id,
                "Trajectory contains no successful ranged read_files output.",
            )
        score = round(novel_lines / total_lines, 3)
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            score=score,
            verdict="pass" if novel_lines == total_lines else "fail",
            explanation=(
                f"{novel_lines} of {total_lines} returned file lines added new coverage."
            ),
            step_ids=tuple(overlapping_steps),
        )


@dataclass(frozen=True, slots=True)
class PromptFileCoverageEvaluator:
    """Measure read_files content already visible in initial File messages."""

    spec: EvaluatorSpec = _evaluator_spec(
        "file_read_prompt_novelty",
        "File read prompt novelty",
        "Measure whether read_files output was already present in initial context.",
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        overlap = prompt_file_read_overlap(trajectory)
        if overlap["total_lines"] == 0:
            return _not_evaluated(
                self.spec.evaluator_id,
                "Trajectory contains no successful ranged read_files output.",
            )
        novel = overlap["total_lines"] - overlap["covered_lines"]
        score = round(novel / overlap["total_lines"], 3)
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            score=score,
            verdict="pass" if overlap["covered_lines"] == 0 else "fail",
            explanation=(
                f"{overlap['covered_lines']} of {overlap['total_lines']} returned file "
                "lines were already visible in initial File messages."
            ),
            step_ids=tuple(overlap["overlapping_steps"]),
        )


@dataclass(frozen=True, slots=True)
class AdjacentFileReadsEvaluator:
    """Describe adjacent reads without assuming they were knowable upfront."""

    spec: EvaluatorSpec = _evaluator_spec(
        "adjacent_file_reads",
        "Adjacent file reads",
        "Observe adjacent or overlapping file ranges and when they became available.",
        (
            MeasurementSpec("read_range_count", "count", "Observed file ranges."),
            MeasurementSpec(
                "mergeable_range_count",
                "count",
                "Adjacent or overlapping ranges that fit in one 500-line range.",
            ),
            MeasurementSpec(
                "cross_turn_mergeable_range_count",
                "count",
                "Mergeable ranges requested in different inference turns.",
            ),
            MeasurementSpec(
                "same_turn_mergeable_range_count",
                "count",
                "Mergeable ranges requested by separate calls in one inference turn.",
            ),
            MeasurementSpec(
                "same_call_mergeable_range_count",
                "count",
                "Mergeable ranges already batched in one tool call.",
            ),
        ),
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        stats = adjacent_file_read_stats(trajectory)
        range_count = stats["read_range_count"]
        if range_count == 0:
            return _not_evaluated(
                self.spec.evaluator_id,
                "Trajectory contains no ranged read_files output.",
            )
        mergeable = stats["mergeable_range_count"]
        measurements = {
            name: stats[name]
            for name in (
                "read_range_count",
                "mergeable_range_count",
                "cross_turn_mergeable_range_count",
                "same_turn_mergeable_range_count",
                "same_call_mergeable_range_count",
            )
        }
        signals = ()
        if mergeable:
            signals = (
                DiagnosticSignal(
                    code="adjacent_file_reads",
                    severity="info",
                    summary=(
                        f"{mergeable} of {range_count} file ranges are adjacent or "
                        "overlapping."
                    ),
                    step_ids=tuple(stats["adjacent_step_ids"]),
                    hypotheses=(
                        "The agent may be navigating a file incrementally across dependent turns.",
                        "A symbol-aware read may collapse search and source retrieval.",
                        "Ranges issued in the same turn may benefit from batching.",
                    ),
                ),
            )
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            measurements=measurements,
            explanation=(
                f"{range_count} observed file ranges contain {mergeable} adjacent or "
                "overlapping range(s); "
                f"{stats['cross_turn_mergeable_range_count']} were requested "
                "across different inference turns."
            ),
            step_ids=tuple(stats["adjacent_step_ids"]),
            signals=signals,
        )


@dataclass(frozen=True, slots=True)
class FileReadBatchingEvaluator:
    """Detect read_files calls that the model emitted in the same response."""

    spec: EvaluatorSpec = _evaluator_spec(
        "file_read_batching",
        "File read batching",
        "Measure read_files batching and detect same-turn calls that could share reads[].",
        (
            MeasurementSpec("read_call_count", "count", "Observed read_files calls."),
            MeasurementSpec("read_range_count", "count", "Observed file ranges."),
            MeasurementSpec(
                "average_batch_size", "count", "File ranges per read_files call."
            ),
            MeasurementSpec(
                "same_turn_unbatched_call_count",
                "count",
                "Extra read_files calls emitted by the same inference response.",
                "lower_is_better",
            ),
            MeasurementSpec(
                "same_turn_unbatched_call_rate",
                "ratio",
                "Same-turn extra calls divided by read_files calls.",
                "lower_is_better",
            ),
        ),
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        stats = file_read_stats(trajectory)
        if stats["calls"] == 0:
            return _not_evaluated(
                self.spec.evaluator_id, "Trajectory contains no read_files calls."
            )
        batching = same_turn_file_read_batching(trajectory)
        extra_calls = batching["extra_calls"]
        measurements = {
            "read_call_count": stats["calls"],
            "read_range_count": stats["requests"],
            "average_batch_size": stats["average_batch"],
            "same_turn_unbatched_call_count": extra_calls,
            "same_turn_unbatched_call_rate": round(extra_calls / stats["calls"], 3),
        }
        signals = ()
        if extra_calls:
            signals = (
                DiagnosticSignal(
                    code="unbatched_same_turn_reads",
                    severity="warning",
                    summary=(
                        f"{extra_calls} read_files call(s) could join another call "
                        "from the same inference response."
                    ),
                    step_ids=tuple(batching["step_ids"]),
                    hypotheses=(
                        "The tool description may not make reads[] batching salient enough.",
                        "The model may prefer multiple tool calls despite knowing all targets.",
                    ),
                ),
            )
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            verdict="warning" if extra_calls else "pass",
            measurements=measurements,
            explanation=(
                f"{stats['requests']} ranges used {stats['calls']} read_files calls; "
                f"{extra_calls} same-turn extra call(s) were observed."
            ),
            step_ids=tuple(batching["step_ids"]),
            signals=signals,
        )


@dataclass(frozen=True, slots=True)
class SearchThenReadEvaluator:
    """Observe later reads that cover an earlier search hit."""

    spec: EvaluatorSpec = _evaluator_spec(
        "search_then_read",
        "Search then read",
        "Measure source reads that follow and cover a search_code hit.",
        (
            MeasurementSpec("read_range_count", "count", "Observed file ranges."),
            MeasurementSpec(
                "search_then_read_range_count",
                "count",
                "Read ranges covering an earlier search hit.",
            ),
            MeasurementSpec(
                "search_then_read_rate",
                "ratio",
                "Search-linked ranges divided by observed read ranges.",
            ),
            MeasurementSpec(
                "identifier_search_then_read_range_count",
                "count",
                "Search-linked ranges reached through an identifier-shaped query.",
            ),
            MeasurementSpec(
                "identifier_search_then_read_rate",
                "ratio",
                "Identifier search-linked ranges divided by observed read ranges.",
            ),
        ),
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        stats = search_then_read_stats(trajectory)
        if not stats["search_calls"] or not stats["read_range_count"]:
            return _not_evaluated(
                self.spec.evaluator_id,
                "Trajectory needs both successful search_code and ranged read_files output.",
            )
        linked = stats["search_then_read_range_count"]
        measurements = {
            name: stats[name]
            for name in (
                "read_range_count",
                "search_then_read_range_count",
                "search_then_read_rate",
                "identifier_search_then_read_range_count",
                "identifier_search_then_read_rate",
            )
        }
        signals = ()
        if linked:
            signals = (
                DiagnosticSignal(
                    code="search_then_read",
                    severity="info",
                    summary=(
                        f"{linked} read range(s) cover a hit returned by an earlier "
                        "search_code call."
                    ),
                    step_ids=tuple(stats["step_ids"]),
                    hypotheses=(
                        "Search results may be serving as navigation before source retrieval.",
                        "Identifier-shaped queries may benefit from a symbol-aware read tool.",
                        "Predictable definitions may belong in initial context.",
                    ),
                ),
            )
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            measurements=measurements,
            explanation=(
                f"{linked} of {stats['read_range_count']} read range(s) cover an "
                "earlier search hit."
            ),
            step_ids=tuple(stats["step_ids"]),
            signals=signals,
        )


@dataclass(frozen=True, slots=True)
class ReviewCompletionEvaluator:
    """Score the authoritative Execution outcome, independent of result yield."""

    spec: EvaluatorSpec = _evaluator_spec(
        "review_completion",
        "Review completion",
        "Check the authoritative execution outcome of one CCR review scope.",
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        outcome = str(trajectory.metadata.get("execution_outcome") or "")
        if not outcome:
            return _not_evaluated(
                self.spec.evaluator_id,
                "Trajectory does not carry an authoritative Execution outcome.",
            )
        completed = outcome == "completed"
        reason = str(trajectory.metadata.get("execution_reason") or "")
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            score=1.0 if completed else 0.0,
            verdict="pass" if completed else "fail",
            explanation=(
                "Review execution completed."
                if completed
                else f"Review execution ended as {outcome}{': ' + reason if reason else ''}."
            ),
        )


@dataclass(frozen=True, slots=True)
class AssessmentCompletionEvaluator:
    """A Review 2 execution completes by submitting an Assessment."""

    spec: EvaluatorSpec = _evaluator_spec(
        "assessment_submission",
        "Assessment submission",
        "Measure whether Review 2 submitted and completed its Assessments.",
    )

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        calls = _tool_steps(trajectory)
        if not calls and not any(step.operation == "inference" for step in trajectory.steps):
            return _not_evaluated(
                self.spec.evaluator_id, "Trajectory contains no model execution."
            )
        submissions = [step for step in calls if step.name == "submit_assessment"]
        if not submissions:
            return EvaluationResult(
                evaluator_id=self.spec.evaluator_id,
                status="evaluated",
                score=0.0,
                verdict="fail",
                explanation="Review 2 ended without submitting an Assessment.",
            )
        completed = [step for step in submissions if _assessment_completed(step)]
        failed = [step.step_id for step in submissions if step not in completed]
        return _ratio_evaluation(
            self.spec.evaluator_id,
            len(completed),
            len(submissions),
            failed,
            "Assessment submissions completed their Review 2 execution",
        )


@dataclass(frozen=True, slots=True)
class RoundEfficiencyEvaluator:
    """Score inference rounds per Unit or completed Lane assessment."""

    spec: EvaluatorSpec = _evaluator_spec(
        "round_efficiency",
        "Round efficiency",
        "Measure inference rounds per Unit or completed Lane assessment.",
    )
    target_rounds_per_item: int = 12

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        rounds = sum(step.operation == "inference" for step in trajectory.steps)
        if rounds == 0:
            return _not_evaluated(
                self.spec.evaluator_id, "Trajectory contains no model execution."
            )
        items = _review_work_items(trajectory)
        budget = self.target_rounds_per_item * items
        score = min(1.0, round(budget / rounds, 3))
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            score=score,
            verdict="pass" if rounds <= budget else "fail",
            explanation=(
                f"{rounds} inference rounds for {items} review item(s); "
                f"target is at most {budget} ({self.target_rounds_per_item} per item)."
            ),
        )


@dataclass(frozen=True, slots=True)
class DurationEfficiencyEvaluator:
    """Score model/tool duration per Unit or completed Lane assessment."""

    spec: EvaluatorSpec = _evaluator_spec(
        "duration_efficiency",
        "Duration efficiency",
        "Measure model and tool duration per Unit or completed Lane assessment.",
    )
    review1_seconds_per_item: int = 180
    review2_seconds_per_item: int = 120

    def evaluate(
        self, trajectory: Trajectory, reference: Trajectory | None = None
    ) -> EvaluationResult:
        del reference
        duration = round(sum(step.duration_ms for step in trajectory.steps) / 1000)
        if duration == 0:
            return _not_evaluated(
                self.spec.evaluator_id, "Trajectory contains no recorded duration."
            )
        items = _review_work_items(trajectory)
        per_item = (
            self.review2_seconds_per_item
            if review_stage(trajectory) == REVIEW2
            else self.review1_seconds_per_item
        )
        budget = per_item * items
        score = min(1.0, round(budget / duration, 3))
        return EvaluationResult(
            evaluator_id=self.spec.evaluator_id,
            status="evaluated",
            score=score,
            verdict="pass" if duration <= budget else "fail",
            explanation=(
                f"{duration}s recorded duration for {items} review item(s); "
                f"target is at most {budget}s ({per_item}s per item)."
            ),
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


def _review_work_items(trajectory: Trajectory) -> int:
    """Normalize a long-lived Review 2 Lane by the assessments it consumed."""

    if review_stage(trajectory) != REVIEW2:
        return 1
    return max(assessment_count(trajectory), 1)


def assessment_count(trajectory: Trajectory) -> int:
    """Count accepted Assessments represented by a Review 2 trajectory."""

    if review_stage(trajectory) != REVIEW2:
        return 0
    accepted = set()
    completed_submissions = 0
    for step in _tool_steps(trajectory):
        if step.name != "submit_assessment" or step.status == "error":
            continue
        try:
            result = json.loads(_tool_response(step))
        except json.JSONDecodeError:
            continue
        values = result.get("accepted") or []
        if isinstance(values, list):
            accepted.update(str(value) for value in values)
        elif values:
            accepted.add(str(values))
        if _assessment_completed(step):
            completed_submissions += 1
    return max(len(accepted), completed_submissions)


def repeated_file_reads(trajectory: Trajectory) -> dict[str, int]:
    """Count path-level repeats even when line ranges differ."""

    reads: Counter[str] = Counter()
    for step in _tool_steps(trajectory):
        if step.name != "read_files":
            continue
        reads.update(
            str(request.get("file_path") or "?")
            for request in _file_read_requests(step)
        )
    return {path: count for path, count in reads.items() if count > 1}


def hypothesis_yield(trajectory: Trajectory) -> int:
    """Count accepted Review 1 outputs without treating a clean zero as failure."""

    accepted = 0
    for step in _tool_steps(trajectory):
        if step.name != "submit_hypothesis" or step.status == "error":
            continue
        if not _hypotheses_accepted(step):
            continue
        accepted += 1
    return accepted


@register_context_demand_extractor("read_files")
def _current_file_demands(step: Step) -> list[ContextDemand]:
    return [
        ContextDemand("file", str(request.get("file_path") or "?"), "source_request")
        for request in _file_read_requests(step)
    ]


@register_context_demand_extractor("read_base_files")
def _baseline_file_demands(step: Step) -> list[ContextDemand]:
    return [
        ContextDemand("baseline_file", str(request.get("file_path") or "?"), "baseline_request")
        for request in _file_read_requests(step)
    ]


@register_context_demand_extractor("read_diffs")
def _diff_demands(step: Step) -> list[ContextDemand]:
    paths = _tool_arguments(step).get("paths") or []
    return [ContextDemand("diff", str(path), "diff_request") for path in paths]


@register_context_demand_extractor("search_code")
def _search_result_demands(step: Step) -> list[ContextDemand]:
    paths = re.findall(r"(?m)^File: (.+)$", _tool_response(step))
    return [ContextDemand("file", path.strip(), "search_hit") for path in paths]


@register_context_demand_extractor("file_find")
def _file_discovery_demands(step: Step) -> list[ContextDemand]:
    paths = []
    for line in _tool_response(step).splitlines():
        value = line.strip()
        if value and not value.lower().startswith(("error:", "no matching", "file was not found")):
            paths.append(ContextDemand("file", value, "file_discovery"))
    return paths


def context_demands(trajectory: Trajectory) -> list[ContextDemand]:
    """Project raw tool steps into generic information demand signals."""

    demands = []
    for step in _tool_steps(trajectory):
        extractor = _CONTEXT_DEMAND_EXTRACTORS.get(step.name)
        if extractor is not None:
            demands.extend(extractor(step))
    return demands


def initial_context_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Join policy exposure with demand inferred by registered step operators.

    An admitted item with no later demand is intentionally neutral: the
    initial context may have prevented the call. Strategy cost/benefit belongs
    in corpus A/B, not in a per-trajectory "unused" penalty.
    """

    inventory: dict[tuple[str, str], dict[str, Any]] = {}
    view_rank = {"source": 3, "outline": 2, "reference": 1}

    for item in trajectory.metadata.get("initial_context") or []:
        if not isinstance(item, dict):
            continue
        kind = str(item.get("kind") or "unknown")
        identity = str(item.get("identity") or "")
        if not identity:
            continue
        key = (kind, identity)
        current = inventory.get(key)
        if current is None or view_rank.get(str(item.get("representation")), 0) > view_rank.get(
            str(current.get("representation")), 0
        ):
            inventory[key] = item

    admitted: Counter[str] = Counter()
    demand: dict[str, Counter[str]] = {}
    by_reason: dict[str, Counter[str]] = {}

    def reason_counts(reason: str) -> Counter[str]:
        return by_reason.setdefault(reason, Counter())

    for item in inventory.values():
        representation = str(item.get("representation") or "unknown")
        reason = str(item.get("reason") or "unknown")
        admitted[representation] += 1
        reason_counts(reason)["admitted"] += 1

    for item in context_demands(trajectory):
        admitted_item = inventory.get((item.kind, item.identity))
        representation = "missing"
        reason = "missing"
        if admitted_item is not None:
            representation = str(admitted_item.get("representation") or "unknown")
            reason = str(admitted_item.get("reason") or "unknown")
        demand.setdefault(item.signal, Counter())[representation] += 1
        reason_counts(reason)[item.signal] += 1

    return {
        "admitted": dict(admitted),
        "demand": {signal: dict(values) for signal, values in demand.items()},
        "by_reason": {reason: dict(values) for reason, values in by_reason.items()},
    }


def file_read_stats(trajectory: Trajectory) -> dict[str, float | int]:
    """Describe how file reads are spread across model turns."""

    calls = [step for step in _tool_steps(trajectory) if step.name == "read_files"]
    if not calls:
        return {
            "calls": 0,
            "requests": 0,
            "rounds": 0,
            "average_batch": 0.0,
            "max_batch": 0,
            "calls_per_round": 0.0,
        }
    batches = [max(len(_file_read_requests(step)), 1) for step in calls]
    rounds = len({step.parent_step_id for step in calls})
    requests = sum(batches)
    return {
        "calls": len(calls),
        "requests": requests,
        "rounds": rounds,
        "average_batch": round(requests / len(calls), 2),
        "max_batch": max(batches),
        "calls_per_round": round(len(calls) / rounds, 2),
    }


def code_search_stats(trajectory: Trajectory) -> dict[str, float | int]:
    """Describe query batching separately from tool-call frequency."""

    calls = [step for step in _tool_steps(trajectory) if step.name == "search_code"]
    if not calls:
        return {
            "calls": 0,
            "requests": 0,
            "rounds": 0,
            "average_batch": 0.0,
            "max_batch": 0,
            "calls_per_round": 0.0,
            "hits": 0,
            "valid_empty": 0,
            "scope_miss": 0,
            "scope_unknown": 0,
            "tool_failure": 0,
            "repeated_empty": 0,
        }
    batches = [max(len(_code_search_requests(step)), 1) for step in calls]
    rounds = len({step.parent_step_id for step in calls})
    requests = sum(batches)
    observations = _code_search_observations(trajectory)
    outcomes = Counter(item["outcome"] for item in observations)
    repeated_empty = 0
    seen_empty: Counter[str] = Counter()
    for item in observations:
        if item["outcome"] not in {"valid_empty", "scope_miss", "scope_unknown"}:
            continue
        seen_empty[item["request_key"]] += 1
        if seen_empty[item["request_key"]] > 1:
            repeated_empty += 1
    return {
        "calls": len(calls),
        "requests": requests,
        "rounds": rounds,
        "average_batch": round(requests / len(calls), 2),
        "max_batch": max(batches),
        "calls_per_round": round(len(calls) / rounds, 2),
        "hits": outcomes["hit"],
        "valid_empty": outcomes["valid_empty"],
        "scope_miss": outcomes["scope_miss"],
        "scope_unknown": outcomes["scope_unknown"],
        "tool_failure": outcomes["tool_failure"],
        "repeated_empty": repeated_empty,
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
        if step.name != "read_files":
            continue
        request_count = max(len(_file_read_requests(step)), 1)
        if step.status == "error":
            failed += request_count
            continue
        if _tool_response(step).startswith("Investigation is closed."):
            blocked += request_count
            continue
        available_ranges = _already_available_ranges(step)
        for available in available_ranges:
            source, _, start, end = available
            if source == "initial":
                delivered = end - start + 1
                total_lines += delivered
                covered_lines += delivered
                fully_covered += 1
                overlapping_steps.append(step.step_id)
            else:
                runtime_covered += 1
        ranges = _file_read_ranges(step)
        for path, start, end in ranges:
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
        unmeasured += max(0, request_count - len(available_ranges) - len(ranges))
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


def adjacent_file_read_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Describe conservative merge potential and when ranges became knowable.

    Cross-turn adjacency is navigation evidence, not proof that the earlier call
    could have requested a range discovered only after seeing its result.
    """

    by_path: dict[str, list[tuple[int, int, str]]] = {}
    tool_steps = _tool_steps(trajectory)
    parent_by_step = {step.step_id: step.parent_step_id for step in tool_steps}
    for step in tool_steps:
        if step.name != "read_files" or step.status == "error":
            continue
        for path, start, end in _file_read_observed_ranges(step):
            by_path.setdefault(path, []).append((start, end, step.step_id))

    range_count = sum(len(ranges) for ranges in by_path.values())
    minimal_ranges = 0
    same_call = 0
    same_turn = 0
    cross_turn = 0
    adjacent_step_ids: list[str] = []
    for ranges in by_path.values():
        current_end = 0
        current_start = 0
        current_step_id = ""
        for start, end, step_id in sorted(ranges):
            mergeable = (
                current_start > 0
                and start <= current_end + 1
                and max(current_end, end) - current_start + 1 <= 500
            )
            if mergeable:
                current_end = max(current_end, end)
                adjacent_step_ids.append(step_id)
                if step_id == current_step_id:
                    same_call += 1
                elif parent_by_step.get(step_id) == parent_by_step.get(current_step_id):
                    same_turn += 1
                else:
                    cross_turn += 1
                current_step_id = step_id
                continue
            minimal_ranges += 1
            current_start, current_end = start, end
            current_step_id = step_id
    return {
        "read_range_count": range_count,
        "minimal_ranges": minimal_ranges,
        "mergeable_range_count": range_count - minimal_ranges,
        "cross_turn_mergeable_range_count": cross_turn,
        "same_turn_mergeable_range_count": same_turn,
        "same_call_mergeable_range_count": same_call,
        "adjacent_step_ids": list(dict.fromkeys(adjacent_step_ids)),
    }


def same_turn_file_read_batching(trajectory: Trajectory) -> dict[str, Any]:
    """Count extra read_files calls emitted by one inference response."""

    by_parent: dict[str, list[str]] = {}
    for step in _tool_steps(trajectory):
        if step.name != "read_files" or not step.parent_step_id:
            continue
        by_parent.setdefault(step.parent_step_id, []).append(step.step_id)
    groups = [step_ids for step_ids in by_parent.values() if len(step_ids) > 1]
    return {
        "extra_calls": sum(len(step_ids) - 1 for step_ids in groups),
        "step_ids": [step_id for group in groups for step_id in group],
    }


def search_then_read_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Link a read range to earlier search hits it actually covers."""

    search_calls = 0
    read_range_count = 0
    linked_ranges = 0
    identifier_linked_ranges = 0
    linked_step_ids: list[str] = []
    hits_by_path: dict[str, list[tuple[int, str, str, str]]] = {}
    identifier = re.compile(r"^[A-Za-z_][A-Za-z0-9_.$]*$")

    for step in _tool_steps(trajectory):
        if step.status == "error":
            continue
        if step.name == "search_code":
            search_calls += 1
            requests = _code_search_requests(step) or [{}]
            for index, result in enumerate(_code_search_result_parts(step)):
                query = str(
                    requests[index].get("query")
                    if index < len(requests)
                    else ""
                )
                file_matches = list(re.finditer(r"(?m)^File:\s*(.+?)\s*$", result))
                for file_index, match in enumerate(file_matches):
                    end = (
                        file_matches[file_index + 1].start()
                        if file_index + 1 < len(file_matches)
                        else len(result)
                    )
                    for line_match in re.finditer(
                        r"(?m)^(\d+)\|", result[match.end() : end]
                    ):
                        hits_by_path.setdefault(match.group(1), []).append(
                            (
                                int(line_match.group(1)),
                                query,
                                step.step_id,
                                step.parent_step_id,
                            )
                        )
            continue
        if step.name != "read_files":
            continue
        for path, start, end in _file_read_observed_ranges(step):
            read_range_count += 1
            hits = [
                hit
                for hit in hits_by_path.get(path, [])
                if start <= hit[0] <= end and hit[3] != step.parent_step_id
            ]
            if not hits:
                continue
            linked_ranges += 1
            linked_step_ids.extend(hit[2] for hit in hits)
            linked_step_ids.append(step.step_id)
            if any(identifier.fullmatch(hit[1]) for hit in hits):
                identifier_linked_ranges += 1

    return {
        "search_calls": search_calls,
        "read_range_count": read_range_count,
        "search_then_read_range_count": linked_ranges,
        "search_then_read_rate": round(linked_ranges / read_range_count, 3)
        if read_range_count
        else 0.0,
        "identifier_search_then_read_range_count": identifier_linked_ranges,
        "identifier_search_then_read_rate": round(
            identifier_linked_ranges / read_range_count, 3
        )
        if read_range_count
        else 0.0,
        "step_ids": list(dict.fromkeys(linked_step_ids)),
    }


def tool_frequencies(trajectory: Trajectory) -> dict[str, int]:
    return dict(Counter(step.name for step in _tool_steps(trajectory)))


def empty_tool_argument_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Count empty payload failures and attribute them to tool and model."""

    inference_models = {
        step.step_id: step.name
        for step in trajectory.steps
        if step.operation == "inference"
    }
    empty = [
        step
        for step in _tool_steps(trajectory)
        if step.status == "error" and _tool_argument_value(step) == {}
    ]
    return {
        "count": len(empty),
        "by_tool": dict(Counter(step.name for step in empty)),
        "by_model": dict(
            Counter(
                inference_models.get(step.parent_step_id, "unknown") for step in empty
            )
        ),
        "step_ids": [step.step_id for step in empty],
    }


def _tool_steps(trajectory: Trajectory) -> list[Step]:
    return [step for step in trajectory.steps if step.operation == "execute_tool"]


def _tool_argument_value(step: Step) -> Any:
    for message in step.input_messages:
        for part in message.get("parts") or []:
            if part.get("type") == "tool_call":
                return part.get("arguments")
    return None


def _tool_arguments(step: Step) -> dict[str, Any]:
    value = _tool_argument_value(step)
    return value if isinstance(value, dict) else {}


def _tool_response(step: Step) -> str:
    for message in step.output_messages:
        for part in message.get("parts") or []:
            if part.get("type") == "tool_call_response":
                value = part.get("response")
                return value if isinstance(value, str) else json.dumps(value)
    return ""


def _file_read_requests(step: Step) -> list[dict[str, Any]]:
    arguments = _tool_arguments(step)
    reads = arguments.get("reads")
    if isinstance(reads, list):
        return [item for item in reads if isinstance(item, dict)]
    # Historical ATIF remains analyzable even though the runtime no longer
    # accepts the former singular tool shape.
    if arguments.get("file_path"):
        return [arguments]
    return []


def _code_search_requests(step: Step) -> list[dict[str, Any]]:
    arguments = _tool_arguments(step)
    searches = arguments.get("searches")
    if isinstance(searches, list):
        return [item for item in searches if isinstance(item, dict)]
    # Historical ATIF remains analyzable even though the runtime only accepts
    # searches[]. This is a trace reader, not a public execution entrypoint.
    if "query" in arguments:
        return [arguments]
    return []


def _code_search_result_parts(step: Step) -> list[str]:
    response = _tool_response(step)
    markers = list(
        re.finditer(r"(?m)^===== CODE_SEARCH RESULT \d+/\d+ =====\n", response)
    )
    if not markers or markers[0].start() != 0:
        return [response]
    return [
        response[marker.end() : markers[index + 1].start() if index + 1 < len(markers) else len(response)].strip()
        for index, marker in enumerate(markers)
    ]


def _code_search_observations(trajectory: Trajectory) -> list[dict[str, str]]:
    """Classify mechanically knowable search outcomes without guessing intent.

    A valid zero-hit search is not automatically useful, but it is also not a
    failure. Distinguishing useful negative evidence from a weak query remains
    a trajectory/judge concern; this layer only proves whether the scope ran.
    """

    observations = []
    for step in _tool_steps(trajectory):
        if step.name != "search_code":
            continue
        requests = _code_search_requests(step) or [{}]
        results = _code_search_result_parts(step)
        for index, request in enumerate(requests):
            text = results[index].strip() if index < len(results) else ""
            outcome = "hit"
            if step.status == "error" or not text or text.startswith(
                ("Error:", "search_code timed out")
            ):
                outcome = "tool_failure"
            else:
                first_line = text.splitlines()[0]
                prefix = "Search outcome: "
                metadata = None
                has_metadata = first_line.startswith(prefix)
                if has_metadata:
                    try:
                        metadata = json.loads(first_line.removeprefix(prefix))
                    except json.JSONDecodeError:
                        outcome = "tool_failure"
                if metadata is not None:
                    outcome = {
                        "no_matches": "valid_empty",
                        "scope_empty": "scope_miss",
                        "scope_unknown": "scope_unknown",
                    }.get(str(metadata.get("status")), "tool_failure")
                elif not has_metadata and "no matches found" in text.lower():
                    outcome = "scope_unknown"

            request_key = json.dumps(
                {
                    "query": request.get("query"),
                    "syntax": request.get("syntax") or "literal",
                    "case_sensitive": bool(request.get("case_sensitive")),
                    "file_patterns": request.get("file_patterns") or [],
                },
                sort_keys=True,
                separators=(",", ":"),
            )
            observations.append(
                {
                    "step_id": f"{step.step_id}:{index + 1}",
                    "outcome": outcome,
                    "request_key": request_key,
                }
            )
    return observations


def _file_read_result_parts(step: Step) -> list[str]:
    response = _tool_response(step)
    markers = list(
        re.finditer(r"(?m)^===== FILE_READ RESULT \d+/\d+ =====\n", response)
    )
    if not markers or markers[0].start() != 0:
        return [response]
    return [
        response[marker.end() : markers[index + 1].start() if index + 1 < len(markers) else len(response)].strip()
        for index, marker in enumerate(markers)
    ]


def _file_read_ranges(step: Step) -> list[tuple[str, int, int]]:
    requests = _file_read_requests(step)
    ranges: list[tuple[str, int, int]] = []
    for index, response in enumerate(_file_read_result_parts(step)):
        path_match = re.search(r"(?m)^File:\s*(.+?)\s+\(Total lines:", response)
        range_match = re.search(r"(?m)^LINE_RANGE:\s*(\d+)-(\d+)\s*$", response)
        if not range_match:
            continue
        requested_path = requests[index].get("file_path") if index < len(requests) else None
        path = path_match.group(1) if path_match else requested_path
        if not path:
            continue
        start, end = map(int, range_match.groups())
        if start > 0 and end >= start:
            ranges.append((str(path), start, end))
    return ranges


def _file_read_observed_ranges(step: Step) -> list[tuple[str, int, int]]:
    ranges = _file_read_ranges(step)
    ranges.extend((path, start, end) for _, path, start, end in _already_available_ranges(step))
    return ranges


def _already_available_ranges(step: Step) -> list[tuple[str, str, int, int]]:
    ranges = []
    for response in _file_read_result_parts(step):
        match = re.search(
            r"Already available in the current context from "
            r"(the initial source context|an earlier read_files result): "
            r"(.+?) lines (\d+)-(\d+)\.",
            response,
        )
        if not match:
            continue
        source = "initial" if match.group(1).startswith("the initial") else "runtime"
        ranges.append((source, match.group(2), int(match.group(3)), int(match.group(4))))
    return ranges


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


def _hypotheses_accepted(step: Step) -> bool:
    response = _tool_response(step)
    return response.startswith("Hypothesis accepted for independent review.")


def _not_evaluated(evaluator_id: str, explanation: str) -> EvaluationResult:
    return EvaluationResult(
        evaluator_id=evaluator_id,
        status="not_applicable",
        explanation=explanation,
    )


def _ratio_evaluation(
    name: str,
    passed: int,
    total: int,
    failed_step_ids: list[str],
    description: str,
) -> EvaluationResult:
    score = round(passed / total, 3)
    return EvaluationResult(
        evaluator_id=name,
        status="evaluated",
        score=score,
        verdict="pass" if passed == total else "fail",
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

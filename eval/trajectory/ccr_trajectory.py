"""Review-specific evaluation of canonical ATIF trajectories."""

from __future__ import annotations

import json
import re
from collections import Counter
from dataclasses import dataclass
from typing import Any, Callable

from eval.trajectory.atif import ATIFTrajectoryLoader as ATIFTrajectoryLoader, ToolObservation, content_text, tool_observations

from trajectory_harness.model import (
    AnalysisCategory,
    step_duration_ms,
        step_operation,
    trajectory_metadata,
)
from trajectory_harness import (
    DetectionResult,
    Detector,
    DetectorSpec,
    VerificationResult,
    Verifier,
    VerifierSpec,
    Finding,
    Measurements,
    RepeatedToolCallDetector,
    RetryLoopDetector,
    Trajectory,
)

REVIEW1 = "review1"
REVIEW2 = "review2"
UNKNOWN_STAGE = "unknown"


def _verifier_spec(
    verifier_id: str,
    title: str,
    description: str,
    *,
    category: AnalysisCategory = "effect",
) -> VerifierSpec:
    return VerifierSpec(
        verifier_id=verifier_id,
        category=category,
        title=title,
        description=description,
        kind="domain",
        owner="case-code-review",
    )


def _detector_spec(
    detector_id: str,
    title: str,
    description: str,
) -> DetectorSpec:
    return DetectorSpec(
        detector_id=detector_id,
        category="cost",
        title=title,
        description=description,
        kind="domain",
        owner="case-code-review",
    )


@dataclass(frozen=True, slots=True)
class ContextDemand:
    """One information need inferred from a trajectory step by an eval operator."""

    kind: str
    identity: str
    signal: str


ContextDemandExtractor = Callable[[ToolObservation], list[ContextDemand]]
_CONTEXT_DEMAND_EXTRACTORS: dict[str, ContextDemandExtractor] = {}


def register_context_demand_extractor(
    tool_name: str,
) -> Callable[[ContextDemandExtractor], ContextDemandExtractor]:
    """Register one tool-specific projection without teaching Harness the tool."""

    def register(extractor: ContextDemandExtractor) -> ContextDemandExtractor:
        _CONTEXT_DEMAND_EXTRACTORS[tool_name] = extractor
        return extractor

    return register


@dataclass(frozen=True, slots=True)
class ToolFailureVerifier:
    spec: VerifierSpec = _verifier_spec(
        "tool_success",
        "Tool success",
        "Measure successful tool executions in one CCR trajectory.",
    )

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        calls = [call for call in tool_observations(trajectory) if call.failed or call.succeeded]
        if not calls:
            return _not_verified(
                self.spec.verifier_id, "Trajectory contains no tool executions with explicit status."
            )
        failed = [str(step.step.step_id) for step in calls if step.failed]
        return _ratio_verification(
            self.spec.verifier_id,
            len(calls) - len(failed),
            len(calls),
            failed,
            "tool calls succeeded",
        )


@dataclass(frozen=True, slots=True)
class SearchScopeVerifier:
    """Reject failed searches and empty scopes without penalizing valid absence."""

    spec: VerifierSpec = _verifier_spec(
        "search_scope_validity",
        "Search scope validity",
        "Measure whether code searches execute against a valid non-empty scope.",
    )

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        observations = _code_search_observations(trajectory)
        if not observations:
            return _not_verified(
                self.spec.verifier_id,
                "Trajectory contains no search_code calls.",
            )
        evaluated = [
            item for item in observations if item["outcome"] != "scope_unknown"
        ]
        if not evaluated:
            return _not_verified(
                self.spec.verifier_id,
                "Searches returned legacy or unmeasured empty scopes.",
            )
        failed = [
            item["step_id"]
            for item in evaluated
            if item["outcome"] in {"scope_miss", "tool_failure"}
        ]
        return _ratio_verification(
            self.spec.verifier_id,
            len(evaluated) - len(failed),
            len(evaluated),
            failed,
            "searches executed against a non-empty scope",
        )


@dataclass(frozen=True, slots=True)
class FileReadCoverageVerifier:
    """Score how much read_files output adds coverage not seen earlier."""

    spec: VerifierSpec = _verifier_spec(
        "file_read_coverage",
        "File read coverage",
        "Measure how much read_files output adds previously unseen line coverage.",
        category="cost",
    )

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        covered: dict[str, list[tuple[int, int]]] = {}
        total_lines = 0
        novel_lines = 0
        overlapping_steps: list[str] = []

        for step in tool_observations(trajectory):
            if step.call.function_name != "read_files" or step.failed:
                continue
            for path, start, end in _file_read_ranges(step):
                delivered = end - start + 1
                prior = covered.setdefault(path, [])
                overlap = _covered_lines(prior, start, end)
                if overlap:
                    overlapping_steps.append(str(step.step.step_id))
                total_lines += delivered
                novel_lines += delivered - overlap
                prior.append((start, end))
                covered[path] = _merge_ranges(prior)

        if total_lines == 0:
            return _not_verified(
                self.spec.verifier_id,
                "Trajectory contains no successful ranged read_files output.",
            )
        score = round(novel_lines / total_lines, 3)
        return VerificationResult(
            verifier_id=self.spec.verifier_id,
            status="verified",
            score=score,
            verdict="pass" if novel_lines == total_lines else "fail",
            explanation=(
                f"{novel_lines} of {total_lines} returned file lines added new coverage."
            ),
            step_ids=tuple(overlapping_steps),
        )


@dataclass(frozen=True, slots=True)
class PromptFileCoverageVerifier:
    """Measure read_files content already visible in initial File messages."""

    spec: VerifierSpec = _verifier_spec(
        "file_read_prompt_novelty",
        "File read prompt novelty",
        "Measure whether read_files output was already present in initial context.",
        category="cost",
    )

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        overlap = prompt_file_read_overlap(trajectory)
        if overlap["total_lines"] == 0:
            return _not_verified(
                self.spec.verifier_id,
                "Trajectory contains no successful ranged read_files output.",
            )
        novel = overlap["total_lines"] - overlap["covered_lines"]
        score = round(novel / overlap["total_lines"], 3)
        return VerificationResult(
            verifier_id=self.spec.verifier_id,
            status="verified",
            score=score,
            verdict="pass" if overlap["covered_lines"] == 0 else "fail",
            explanation=(
                f"{overlap['covered_lines']} of {overlap['total_lines']} returned file "
                "lines were already visible in initial File messages."
            ),
            step_ids=tuple(overlap["overlapping_steps"]),
        )


@dataclass(frozen=True, slots=True)
class AdjacentFileReadsDetector:
    """Describe adjacent reads without assuming they were knowable upfront."""

    spec: DetectorSpec = _detector_spec(
        "adjacent_file_reads",
        "Adjacent file reads",
        "Identify adjacent or overlapping file ranges and when they became available.",
    )

    def detect(
        self, trajectory: Trajectory, *, measurements: Measurements = ()
    ) -> DetectionResult:
        stats = adjacent_file_read_stats(trajectory)
        range_count = stats["read_range_count"]
        if range_count == 0:
            return _not_detected(
                self.spec.detector_id,
                "Trajectory contains no ranged read_files output.",
            )
        mergeable = stats["mergeable_range_count"]
        findings = ()
        if mergeable:
            findings = (
                Finding(
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
        return DetectionResult(
            detector_id=self.spec.detector_id,
            status="analyzed",
            explanation=(
                f"{range_count} observed file ranges contain {mergeable} adjacent or "
                "overlapping range(s); "
                f"{stats['cross_turn_mergeable_range_count']} were requested "
                "across different inference turns."
            ),
            findings=findings,
        )


@dataclass(frozen=True, slots=True)
class FileReadBatchingDetector:
    """Detect read_files calls that the model emitted in the same response."""

    spec: DetectorSpec = _detector_spec(
        "file_read_batching",
        "File read batching",
        "Identify same-turn read_files calls that could share one reads[] batch.",
    )

    def detect(
        self, trajectory: Trajectory, *, measurements: Measurements = ()
    ) -> DetectionResult:
        stats = file_read_stats(trajectory)
        if stats["calls"] == 0:
            return _not_detected(
                self.spec.detector_id, "Trajectory contains no read_files calls."
            )
        batching = same_turn_file_read_batching(trajectory)
        extra_calls = batching["extra_calls"]
        findings = ()
        if extra_calls:
            findings = (
                Finding(
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
        return DetectionResult(
            detector_id=self.spec.detector_id,
            status="analyzed",
            explanation=(
                f"{stats['requests']} ranges used {stats['calls']} read_files calls; "
                f"{extra_calls} same-turn extra call(s) were observed."
            ),
            findings=findings,
        )


@dataclass(frozen=True, slots=True)
class SearchThenReadDetector:
    """Observe later reads that cover an earlier search hit."""

    spec: DetectorSpec = _detector_spec(
        "search_then_read",
        "Search then read",
        "Identify source reads that follow and cover a search_code hit.",
    )

    def detect(
        self, trajectory: Trajectory, *, measurements: Measurements = ()
    ) -> DetectionResult:
        stats = search_then_read_stats(trajectory)
        if not stats["hit_search_request_count"]:
            return _not_detected(
                self.spec.detector_id,
                "Trajectory needs a search_code request that returned at least one hit.",
            )
        linked = stats["search_then_read_range_count"]
        findings = ()
        if linked:
            findings = (
                Finding(
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
        return DetectionResult(
            detector_id=self.spec.detector_id,
            status="analyzed",
            explanation=(
                f"{stats['follow_up_read_request_count']} of "
                f"{stats['hit_search_request_count']} hit-producing search request(s) "
                f"were followed by a covering read ({linked} read range(s))."
            ),
            findings=findings,
        )


@dataclass(frozen=True, slots=True)
class ReviewCompletionVerifier:
    """Score the authoritative Execution outcome, independent of result yield."""

    spec: VerifierSpec = _verifier_spec(
        "review_completion",
        "Review completion",
        "Check the authoritative execution outcome of one CCR review scope.",
    )

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        outcome = str(trajectory_metadata(trajectory).get("execution_outcome") or "")
        if not outcome:
            return _not_verified(
                self.spec.verifier_id,
                "Trajectory does not carry an authoritative Execution outcome.",
            )
        completed = outcome == "completed"
        reason = str(trajectory_metadata(trajectory).get("execution_reason") or "")
        return VerificationResult(
            verifier_id=self.spec.verifier_id,
            status="verified",
            score=1.0 if completed else 0.0,
            verdict="pass" if completed else "fail",
            explanation=(
                "Review execution completed."
                if completed
                else f"Review execution ended as {outcome}{': ' + reason if reason else ''}."
            ),
        )


@dataclass(frozen=True, slots=True)
class AssessmentCompletionVerifier:
    """A Review 2 execution completes by submitting an Assessment."""

    spec: VerifierSpec = _verifier_spec(
        "assessment_submission",
        "Assessment submission",
        "Measure whether Review 2 submitted and completed its Assessments.",
    )

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        calls = tool_observations(trajectory)
        if not calls and not any(
            step_operation(step) == "inference" for step in trajectory.steps
        ):
            return _not_verified(
                self.spec.verifier_id, "Trajectory contains no model execution."
            )
        submissions = [step for step in calls if step.call.function_name == "submit_assessment"]
        if not submissions:
            return VerificationResult(
                verifier_id=self.spec.verifier_id,
                status="verified",
                score=0.0,
                verdict="fail",
                explanation="Review 2 ended without submitting an Assessment.",
            )
        completed = [step for step in submissions if _assessment_completed(step)]
        failed = [str(step.step.step_id) for step in submissions if step not in completed]
        return _ratio_verification(
            self.spec.verifier_id,
            len(completed),
            len(submissions),
            failed,
            "Assessment submissions completed their Review 2 execution",
        )


@dataclass(frozen=True, slots=True)
class RoundEfficiencyVerifier:
    """Score inference rounds per Unit or completed Lane assessment."""

    spec: VerifierSpec = _verifier_spec(
        "round_efficiency",
        "Round efficiency",
        "Measure inference rounds per Unit or completed Lane assessment.",
        category="cost",
    )
    target_rounds_per_item: int = 12

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        rounds = sum(step_operation(step) == "inference" for step in trajectory.steps)
        if rounds == 0:
            return _not_verified(
                self.spec.verifier_id, "Trajectory contains no model execution."
            )
        items = _review_work_items(trajectory)
        budget = self.target_rounds_per_item * items
        score = min(1.0, round(budget / rounds, 3))
        return VerificationResult(
            verifier_id=self.spec.verifier_id,
            status="verified",
            score=score,
            verdict="pass" if rounds <= budget else "fail",
            explanation=(
                f"{rounds} inference rounds for {items} review item(s); "
                f"target is at most {budget} ({self.target_rounds_per_item} per item)."
            ),
        )


@dataclass(frozen=True, slots=True)
class DurationEfficiencyVerifier:
    """Score model/tool duration per Unit or completed Lane assessment."""

    spec: VerifierSpec = _verifier_spec(
        "duration_efficiency",
        "Duration efficiency",
        "Measure model and tool duration per Unit or completed Lane assessment.",
        category="cost",
    )
    review1_seconds_per_item: int = 180
    review2_seconds_per_item: int = 120

    def verify(
        self,
        trajectory: Trajectory,
        *,
        measurements: Measurements = (),
        reference: Trajectory | None = None,
    ) -> VerificationResult:
        del reference, measurements
        duration = round(sum(step_duration_ms(step) for step in trajectory.steps) / 1000)
        if duration == 0:
            return _not_verified(
                self.spec.verifier_id, "Trajectory contains no recorded duration."
            )
        items = _review_work_items(trajectory)
        per_item = (
            self.review2_seconds_per_item
            if review_stage(trajectory) == REVIEW2
            else self.review1_seconds_per_item
        )
        budget = per_item * items
        score = min(1.0, round(budget / duration, 3))
        return VerificationResult(
            verifier_id=self.spec.verifier_id,
            status="verified",
            score=score,
            verdict="pass" if duration <= budget else "fail",
            explanation=(
                f"{duration}s recorded duration for {items} review item(s); "
                f"target is at most {budget}s ({per_item}s per item)."
            ),
        )


def review_stage(trajectory: Trajectory) -> str:
    """Classify a CCR scope without relying on trajectory-id naming."""

    scope_kind = trajectory_metadata(trajectory).get("scope_kind")
    if scope_kind == "unit":
        return REVIEW1
    if scope_kind == "lane":
        return REVIEW2
    if any(
        (step.extra or {}).get("task_type") == "hypothesis_review_task"
        for step in trajectory.steps
        if step_operation(step) == "inference"
    ):
        return REVIEW2
    return UNKNOWN_STAGE


def verifiers_for_stage(stage: str) -> tuple[Verifier, ...]:
    """Return CCR's deterministic verifier suite for one review stage."""

    common = (
        ToolFailureVerifier(),
        SearchScopeVerifier(),
        FileReadCoverageVerifier(),
        PromptFileCoverageVerifier(),
        RoundEfficiencyVerifier(),
        DurationEfficiencyVerifier(),
        ReviewCompletionVerifier(),
    )
    if stage == REVIEW2:
        return (*common, AssessmentCompletionVerifier())
    return common


def detectors_for_stage(stage: str) -> tuple[Detector, ...]:
    """Return reusable trajectory-pattern detectors for one review stage."""

    del stage
    return (
        RepeatedToolCallDetector(),
        RetryLoopDetector(),
        AdjacentFileReadsDetector(),
        FileReadBatchingDetector(),
        SearchThenReadDetector(),
    )


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
    for step in tool_observations(trajectory):
        if step.call.function_name != "submit_assessment" or step.failed:
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
    for step in tool_observations(trajectory):
        if step.call.function_name != "read_files":
            continue
        reads.update(
            str(request.get("file_path") or "?")
            for request in _file_read_requests(step)
        )
    return {path: count for path, count in reads.items() if count > 1}


def hypothesis_yield(trajectory: Trajectory) -> int:
    """Count accepted Review 1 outputs without treating a clean zero as failure."""

    accepted = 0
    for step in tool_observations(trajectory):
        if step.call.function_name != "submit_hypothesis" or step.failed:
            continue
        if not _hypotheses_accepted(step):
            continue
        accepted += 1
    return accepted


@register_context_demand_extractor("read_files")
def _current_file_demands(step: ToolObservation) -> list[ContextDemand]:
    return [
        ContextDemand("file", str(request.get("file_path") or "?"), "source_request")
        for request in _file_read_requests(step)
    ]


@register_context_demand_extractor("read_base_files")
def _baseline_file_demands(step: ToolObservation) -> list[ContextDemand]:
    return [
        ContextDemand(
            "baseline_file", str(request.get("file_path") or "?"), "baseline_request"
        )
        for request in _file_read_requests(step)
    ]


@register_context_demand_extractor("read_diffs")
def _diff_demands(step: ToolObservation) -> list[ContextDemand]:
    paths = _tool_arguments(step).get("paths") or []
    return [ContextDemand("diff", str(path), "diff_request") for path in paths]


@register_context_demand_extractor("search_code")
def _search_result_demands(step: ToolObservation) -> list[ContextDemand]:
    paths = re.findall(r"(?m)^File: (.+)$", _tool_response(step))
    return [ContextDemand("file", path.strip(), "search_hit") for path in paths]


@register_context_demand_extractor("file_find")
def _file_discovery_demands(step: ToolObservation) -> list[ContextDemand]:
    paths = []
    for line in _tool_response(step).splitlines():
        value = line.strip()
        if value and not value.lower().startswith(
            ("error:", "no matching", "file was not found")
        ):
            paths.append(ContextDemand("file", value, "file_discovery"))
    return paths


def context_demands(trajectory: Trajectory) -> list[ContextDemand]:
    """Project raw tool steps into generic information demand signals."""

    demands = []
    for step in tool_observations(trajectory):
        extractor = _CONTEXT_DEMAND_EXTRACTORS.get(step.call.function_name)
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

    for item in trajectory_metadata(trajectory).get("initial_context") or []:
        if not isinstance(item, dict):
            continue
        kind = str(item.get("kind") or "unknown")
        identity = str(item.get("identity") or "")
        if not identity:
            continue
        key = (kind, identity)
        current = inventory.get(key)
        if current is None or view_rank.get(
            str(item.get("representation")), 0
        ) > view_rank.get(str(current.get("representation")), 0):
            inventory[key] = item

    admitted: Counter[str] = Counter()
    demand: dict[str, Counter[str]] = {}
    by_reason: dict[str, Counter[str]] = {}
    outline_outcomes: Counter[str] = Counter()
    outline_by_language: dict[str, Counter[str]] = {}
    admitted_outline_bytes = 0

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

    for item in trajectory_metadata(trajectory).get("initial_outline_attempts") or []:
        if not isinstance(item, dict):
            continue
        outcome = str(item.get("outcome") or "unknown")
        language = str(item.get("language") or "unknown")
        outline_outcomes[outcome] += 1
        outline_by_language.setdefault(language, Counter())[outcome] += 1
        if outcome == "admitted":
            admitted_outline_bytes += _non_negative_int(item.get("bytes"))

    return {
        "admitted": dict(admitted),
        "demand": {signal: dict(values) for signal, values in demand.items()},
        "by_reason": {reason: dict(values) for reason, values in by_reason.items()},
        "outlines": {
            "attempts": sum(outline_outcomes.values()),
            "outcomes": dict(outline_outcomes),
            "by_language": {
                language: dict(outcomes)
                for language, outcomes in outline_by_language.items()
            },
            "admitted_bytes": admitted_outline_bytes,
        },
    }


def file_read_stats(trajectory: Trajectory) -> dict[str, float | int]:
    """Describe how file reads are spread across model turns."""

    calls = [step for step in tool_observations(trajectory) if step.call.function_name == "read_files"]
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
    rounds = len({str(step.step.step_id) for step in calls})
    requests = sum(batches)
    return {
        "calls": len(calls),
        "requests": requests,
        "rounds": rounds,
        "average_batch": round(requests / len(calls), 2),
        "max_batch": max(batches),
        "calls_per_round": round(len(calls) / rounds, 2),
    }


def code_search_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Describe query batching separately from tool-call frequency."""

    calls = [step for step in tool_observations(trajectory) if step.call.function_name == "search_code"]
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
            "context_projections": 0,
            "context_projection_rate": 0.0,
            "returned_context_lines": 0,
            "context_truncated_results": 0,
            "context_unavailable_results": 0,
            "symbol_context_attempts": 0,
            "symbol_context_outcomes": {},
            "returned_symbol_context_lines": 0,
        }
    batches = [max(len(_code_search_requests(step)), 1) for step in calls]
    rounds = len({str(step.step.step_id) for step in calls})
    requests = sum(batches)
    context_projections = 0
    returned_context_lines = 0
    context_truncated_results = 0
    context_unavailable_results = 0
    symbol_context_attempts = 0
    symbol_context_outcomes: Counter[str] = Counter()
    returned_symbol_context_lines = 0
    for step in calls:
        step_requests = _code_search_requests(step) or [{}]
        step_results = _code_search_result_parts(step)
        for index, request in enumerate(step_requests):
            result = step_results[index] if index < len(step_results) else ""
            context_lines = _returned_search_context_lines(result)
            symbol_lines = _returned_search_symbol_lines(result)
            if context_lines or symbol_lines:
                context_projections += 1
            returned_context_lines += context_lines
            context_truncated_results += "Note: Context truncated" in result
            context_unavailable_results += "Context unavailable:" in result
            outcome = _search_symbol_context_outcome(result)
            if outcome:
                symbol_context_attempts += 1
                symbol_context_outcomes[outcome] += 1
                returned_symbol_context_lines += symbol_lines
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
        "context_projections": context_projections,
        "context_projection_rate": round(context_projections / requests, 3),
        "returned_context_lines": returned_context_lines,
        "context_truncated_results": context_truncated_results,
        "context_unavailable_results": context_unavailable_results,
        "symbol_context_attempts": symbol_context_attempts,
        "symbol_context_outcomes": dict(symbol_context_outcomes),
        "returned_symbol_context_lines": returned_symbol_context_lines,
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
    for step in tool_observations(trajectory):
        if step.call.function_name != "read_files":
            continue
        request_count = max(len(_file_read_requests(step)), 1)
        if step.failed:
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
                overlapping_steps.append(str(step.step.step_id))
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
                overlapping_steps.append(str(step.step.step_id))
            elif overlap:
                partially_covered += 1
                overlapping_steps.append(str(step.step.step_id))
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
        "overlapping_steps": list(dict.fromkeys(overlapping_steps)),
    }


def adjacent_file_read_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Describe conservative merge potential and when ranges became knowable.

    Cross-turn adjacency is navigation evidence, not proof that the earlier call
    could have requested a range discovered only after seeing its result.
    """

    by_path: dict[str, list[tuple[int, int, str]]] = {}
    tool_steps = tool_observations(trajectory)
    parent_by_step = {step.key: str(step.step.step_id) for step in tool_steps}
    for step in tool_steps:
        if step.call.function_name != "read_files" or step.failed:
            continue
        for path, start, end in _file_read_observed_ranges(step):
            by_path.setdefault(path, []).append((start, end, step.key))

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
                adjacent_step_ids.append(parent_by_step[step_id])
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
    for step in tool_observations(trajectory):
        if step.call.function_name != "read_files" or not str(step.step.step_id):
            continue
        by_parent.setdefault(str(step.step.step_id), []).append(str(step.step.step_id))
    groups = [step_ids for step_ids in by_parent.values() if len(step_ids) > 1]
    return {
        "extra_calls": sum(len(step_ids) - 1 for step_ids in groups),
        "step_ids": list(dict.fromkeys(step_id for group in groups for step_id in group)),
    }


def search_then_read_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Link a read range to earlier search hits it actually covers."""

    search_calls = 0
    read_range_count = 0
    linked_ranges = 0
    identifier_linked_ranges = 0
    linked_step_ids: list[str] = []
    hits_by_path: dict[str, list[tuple[int, str, str, str, str, bool]]] = {}
    hit_requests: set[str] = set()
    context_hit_requests: set[str] = set()
    plain_hit_requests: set[str] = set()
    follow_up_requests: set[str] = set()
    context_follow_up_requests: set[str] = set()
    plain_follow_up_requests: set[str] = set()
    symbol_hit_requests: set[str] = set()
    symbol_expanded_hit_requests: set[str] = set()
    symbol_follow_up_requests: set[str] = set()
    symbol_expanded_follow_up_requests: set[str] = set()
    symbol_expanded_within_span_follow_up_requests: set[str] = set()
    symbol_expanded_extending_follow_up_requests: set[str] = set()
    symbol_source_ranges_by_request: dict[str, list[tuple[str, int, int]]] = {}
    identifier = re.compile(r"^[A-Za-z_][A-Za-z0-9_.$]*$")

    for step in tool_observations(trajectory):
        if step.failed:
            continue
        if step.call.function_name == "search_code":
            search_calls += 1
            requests = _code_search_requests(step) or [{}]
            for index, result in enumerate(_code_search_result_parts(step)):
                request = requests[index] if index < len(requests) else {}
                query = str(request.get("query") if request else "")
                request_id = f"{step.key}:{index + 1}"
                symbol_outcome = _search_symbol_context_outcome(result)
                has_symbol_context = bool(symbol_outcome)
                symbol_expanded = symbol_outcome == "expanded"
                if symbol_expanded:
                    symbol_source_ranges_by_request[request_id] = (
                        _search_symbol_source_ranges(result)
                    )
                has_context = (
                    _returned_search_context_lines(result) > 0 or symbol_expanded
                )
                request_has_hits = False
                file_matches = list(re.finditer(r"(?m)^File:\s*(.+?)\s*$", result))
                for file_index, match in enumerate(file_matches):
                    end = (
                        file_matches[file_index + 1].start()
                        if file_index + 1 < len(file_matches)
                        else len(result)
                    )
                    block = result[match.end() : end]
                    count_match = re.search(r"(?m)^Match lines:\s*(\d+)\s*$", block)
                    match_count = int(count_match.group(1)) if count_match else 0
                    line_matches = list(re.finditer(r"(?m)^(\d+)\|", block))
                    # Automatic nearby-source projection appends numbered windows
                    # after the original hit list. Only those first Match lines entries
                    # identify the search hits this verifier links to reads.
                    for line_match in line_matches[:match_count]:
                        request_has_hits = True
                        hits_by_path.setdefault(match.group(1), []).append(
                            (
                                int(line_match.group(1)),
                                query,
                                str(step.step.step_id),
                                str(step.step.step_id),
                                request_id,
                                has_context,
                            )
                        )
                if request_has_hits:
                    hit_requests.add(request_id)
                    (context_hit_requests if has_context else plain_hit_requests).add(
                        request_id
                    )
                    if has_symbol_context:
                        symbol_hit_requests.add(request_id)
                    if symbol_expanded:
                        symbol_expanded_hit_requests.add(request_id)
            continue
        if step.call.function_name != "read_files":
            continue
        for path, start, end in _file_read_observed_ranges(step):
            read_range_count += 1
            hits = [
                hit
                for hit in hits_by_path.get(path, [])
                if start <= hit[0] <= end and hit[3] != str(step.step.step_id)
            ]
            if not hits:
                continue
            linked_ranges += 1
            linked_step_ids.extend(hit[2] for hit in hits)
            linked_step_ids.append(str(step.step.step_id))
            for hit in hits:
                follow_up_requests.add(hit[4])
                (
                    context_follow_up_requests if hit[5] else plain_follow_up_requests
                ).add(hit[4])
                if hit[4] in symbol_hit_requests:
                    symbol_follow_up_requests.add(hit[4])
                if hit[4] in symbol_expanded_hit_requests:
                    symbol_expanded_follow_up_requests.add(hit[4])
                    source_ranges = symbol_source_ranges_by_request.get(hit[4], [])
                    if any(
                        source_path == path
                        and source_start <= start
                        and end <= source_end
                        for source_path, source_start, source_end in source_ranges
                    ):
                        symbol_expanded_within_span_follow_up_requests.add(hit[4])
                    elif any(
                        source_path == path
                        for source_path, _, _ in source_ranges
                    ):
                        symbol_expanded_extending_follow_up_requests.add(hit[4])
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
        "hit_search_request_count": len(hit_requests),
        "follow_up_read_request_count": len(follow_up_requests),
        "follow_up_read_rate": _ratio(len(follow_up_requests), len(hit_requests)),
        "context_hit_search_request_count": len(context_hit_requests),
        "context_follow_up_read_request_count": len(context_follow_up_requests),
        "context_follow_up_read_rate": _ratio(
            len(context_follow_up_requests), len(context_hit_requests)
        ),
        "plain_hit_search_request_count": len(plain_hit_requests),
        "plain_follow_up_read_request_count": len(plain_follow_up_requests),
        "plain_follow_up_read_rate": _ratio(
            len(plain_follow_up_requests), len(plain_hit_requests)
        ),
        "symbol_hit_search_request_count": len(symbol_hit_requests),
        "symbol_follow_up_read_request_count": len(symbol_follow_up_requests),
        "symbol_follow_up_read_rate": _ratio(
            len(symbol_follow_up_requests), len(symbol_hit_requests)
        ),
        "symbol_expanded_hit_search_request_count": len(symbol_expanded_hit_requests),
        "symbol_expanded_follow_up_read_request_count": len(
            symbol_expanded_follow_up_requests
        ),
        "symbol_expanded_follow_up_read_rate": _ratio(
            len(symbol_expanded_follow_up_requests),
            len(symbol_expanded_hit_requests),
        ),
        "symbol_expanded_within_span_follow_up_read_request_count": len(
            symbol_expanded_within_span_follow_up_requests
        ),
        "symbol_expanded_within_span_follow_up_read_rate": _ratio(
            len(symbol_expanded_within_span_follow_up_requests),
            len(symbol_expanded_hit_requests),
        ),
        "symbol_expanded_extending_follow_up_read_request_count": len(
            symbol_expanded_extending_follow_up_requests
        ),
        "symbol_expanded_extending_follow_up_read_rate": _ratio(
            len(symbol_expanded_extending_follow_up_requests),
            len(symbol_expanded_hit_requests),
        ),
        "step_ids": list(dict.fromkeys(linked_step_ids)),
    }


def _non_negative_int(value: Any) -> int:
    if isinstance(value, bool):
        return 0
    if isinstance(value, (int, float)) and value >= 0 and int(value) == value:
        return int(value)
    return 0


def _returned_search_context_lines(result: str) -> int:
    """Count numbered source lines emitted inside search Context sections."""

    in_context = False
    count = 0
    for line in result.splitlines():
        if line == "Context:":
            in_context = True
            continue
        if line.startswith("File: "):
            in_context = False
            continue
        if in_context and re.match(r"^\d+\|", line):
            count += 1
    return count


def _search_symbol_context_outcome(result: str) -> str:
    for line in result.splitlines():
        if not line.startswith("Symbol context: "):
            continue
        try:
            value = json.loads(line.removeprefix("Symbol context: "))
        except json.JSONDecodeError:
            return ""
        return str(value.get("status") or "") if isinstance(value, dict) else ""
    return ""


def _search_symbol_source_ranges(result: str) -> list[tuple[str, int, int]]:
    ranges: list[tuple[str, int, int]] = []
    for line in result.splitlines():
        if not line.startswith("Symbol source: "):
            continue
        try:
            value = json.loads(line.removeprefix("Symbol source: "))
        except json.JSONDecodeError:
            continue
        if not isinstance(value, dict):
            continue
        path = str(value.get("path") or "")
        start = _non_negative_int(value.get("start_line"))
        end = _non_negative_int(value.get("end_line"))
        if path and start > 0 and end >= start:
            ranges.append((path, start, end))
    return ranges


def _returned_search_symbol_lines(result: str) -> int:
    """Count source lines emitted after a Symbol source metadata line."""

    in_source = False
    count = 0
    for line in result.splitlines():
        if line.startswith("Symbol source: "):
            in_source = True
            continue
        if in_source and re.match(r"^\d+\|", line):
            count += 1
        elif in_source:
            in_source = False
    return count


def _ratio(numerator: int, denominator: int) -> float | None:
    return round(numerator / denominator, 3) if denominator else None


def tool_frequencies(trajectory: Trajectory) -> dict[str, int]:
    return dict(Counter(step.call.function_name for step in tool_observations(trajectory)))


def empty_tool_argument_stats(trajectory: Trajectory) -> dict[str, Any]:
    """Count empty payload failures and attribute them to tool and model."""

    inference_models = {
        str(step.step_id): step.model_name or trajectory.agent.model_name or "unknown"
        for step in trajectory.steps
        if step_operation(step) == "inference"
    }
    empty = [
        step
        for step in tool_observations(trajectory)
        if step.failed and _tool_argument_value(step) == {}
    ]
    return {
        "count": len(empty),
        "by_tool": dict(Counter(step.call.function_name for step in empty)),
        "by_model": dict(
            Counter(
                inference_models.get(str(step.step.step_id), "unknown") for step in empty
            )
        ),
        "step_ids": [str(step.step.step_id) for step in empty],
    }


def _tool_argument_value(step: ToolObservation) -> Any:
    return step.call.arguments


def _tool_arguments(step: ToolObservation) -> dict[str, Any]:
    return step.call.arguments


def _tool_response(step: ToolObservation) -> str:
    return step.text


def _file_read_requests(step: ToolObservation) -> list[dict[str, Any]]:
    arguments = _tool_arguments(step)
    reads = arguments.get("reads")
    if isinstance(reads, list):
        return [item for item in reads if isinstance(item, dict)]
    # Historical ATIF remains analyzable even though the runtime no longer
    # accepts the former singular tool shape.
    if arguments.get("file_path"):
        return [arguments]
    return []


def _code_search_requests(step: ToolObservation) -> list[dict[str, Any]]:
    arguments = _tool_arguments(step)
    searches = arguments.get("searches")
    if isinstance(searches, list):
        return [item for item in searches if isinstance(item, dict)]
    # Historical ATIF remains analyzable even though the runtime only accepts
    # searches[]. This is a trace reader, not a public execution entrypoint.
    if "query" in arguments:
        return [arguments]
    return []


def _code_search_result_parts(step: ToolObservation) -> list[str]:
    response = _tool_response(step)
    markers = list(
        re.finditer(r"(?m)^===== CODE_SEARCH RESULT \d+/\d+ =====\n", response)
    )
    if not markers or markers[0].start() != 0:
        return [response]
    return [
        response[
            marker.end() : markers[index + 1].start()
            if index + 1 < len(markers)
            else len(response)
        ].strip()
        for index, marker in enumerate(markers)
    ]


def _code_search_observations(trajectory: Trajectory) -> list[dict[str, str]]:
    """Classify mechanically knowable search outcomes without guessing intent.

    A valid zero-hit search is not automatically useful, but it is also not a
    failure. Distinguishing useful negative evidence from a weak query remains
    a trajectory/judge concern; this layer only proves whether the scope ran.
    """

    observations = []
    for step in tool_observations(trajectory):
        if step.call.function_name != "search_code":
            continue
        requests = _code_search_requests(step) or [{}]
        results = _code_search_result_parts(step)
        for index, request in enumerate(requests):
            text = results[index].strip() if index < len(results) else ""
            outcome = "hit"
            if (
                step.failed
                or not text
                or text.startswith(("Error:", "search_code timed out"))
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
                    "step_id": str(step.step.step_id),
                    "request_index": str(index + 1),
                    "outcome": outcome,
                    "request_key": request_key,
                }
            )
    return observations


def _file_read_result_parts(step: ToolObservation) -> list[str]:
    response = _tool_response(step)
    markers = list(
        re.finditer(r"(?m)^===== FILE_READ RESULT \d+/\d+ =====\n", response)
    )
    if not markers or markers[0].start() != 0:
        return [response]
    return [
        response[
            marker.end() : markers[index + 1].start()
            if index + 1 < len(markers)
            else len(response)
        ].strip()
        for index, marker in enumerate(markers)
    ]


def _file_read_ranges(step: ToolObservation) -> list[tuple[str, int, int]]:
    requests = _file_read_requests(step)
    ranges: list[tuple[str, int, int]] = []
    for index, response in enumerate(_file_read_result_parts(step)):
        path_match = re.search(r"(?m)^File:\s*(.+?)\s+\(Total lines:", response)
        range_match = re.search(r"(?m)^LINE_RANGE:\s*(\d+)-(\d+)\s*$", response)
        if not range_match:
            continue
        requested_path = (
            requests[index].get("file_path") if index < len(requests) else None
        )
        path = path_match.group(1) if path_match else requested_path
        if not path:
            continue
        start, end = map(int, range_match.groups())
        if start > 0 and end >= start:
            ranges.append((str(path), start, end))
    return ranges


def _file_read_observed_ranges(step: ToolObservation) -> list[tuple[str, int, int]]:
    ranges = _file_read_ranges(step)
    ranges.extend(
        (path, start, end) for _, path, start, end in _already_available_ranges(step)
    )
    return ranges


def _already_available_ranges(step: ToolObservation) -> list[tuple[str, str, int, int]]:
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
        ranges.append(
            (source, match.group(2), int(match.group(3)), int(match.group(4)))
        )
    return ranges


def _context_file_ranges(trajectory: Trajectory) -> dict[str, list[tuple[int, int]]]:
    ranges: dict[str, list[tuple[int, int]]] = {}
    for step in trajectory.steps:
        if step.source not in {"user", "system"}:
            continue
        content = content_text(step.message)
        lines = content.splitlines()
        if not lines:
            continue
        header = re.match(
            r"^File:\s*(.+?)\s+\(Total lines:\s*(\d+)\)$", lines[0]
        )
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


def _assessment_completed(step: ToolObservation) -> bool:
    try:
        result = json.loads(_tool_response(step))
    except json.JSONDecodeError:
        return False
    return bool(result.get("accepted")) and not result.get("remaining")


def _hypotheses_accepted(step: ToolObservation) -> bool:
    response = _tool_response(step)
    return response.startswith("Hypothesis accepted for independent review.")


def _not_verified(verifier_id: str, explanation: str) -> VerificationResult:
    return VerificationResult(
        verifier_id=verifier_id,
        status="not_applicable",
        explanation=explanation,
    )


def _not_detected(detector_id: str, explanation: str) -> DetectionResult:
    return DetectionResult(
        detector_id=detector_id,
        status="not_applicable",
        explanation=explanation,
    )


def _ratio_verification(
    name: str,
    passed: int,
    total: int,
    failed_step_ids: list[str],
    description: str,
) -> VerificationResult:
    score = round(passed / total, 3)
    return VerificationResult(
        verifier_id=name,
        status="verified",
        score=score,
        verdict="pass" if passed == total else "fail",
        explanation=f"{passed} of {total} {description}.",
        step_ids=tuple(dict.fromkeys(failed_step_ids)),
    )

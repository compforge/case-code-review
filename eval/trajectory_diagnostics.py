#!/usr/bin/env python3
"""Incrementally diagnose the highest-signal rows in a canonical CCR run."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import sys
from collections import Counter
from dataclasses import dataclass, replace
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

from trajectory_harness import Trajectory, TrajectoryRunArtifact, load_run_artifact
from trajectory_judge import (
    JUDGE_SYSTEM,
    TAXONOMY,
    JudgeResult,
    chain_digest,
    judge_chain_with_usage,
    load_llm,
    objective_analysis_from_run,
)

DIAGNOSTIC_SCHEMA_VERSION = 2
PLANNER_VERSION = "ccr-trajectory-diagnostic-planner-v2"
PROMPT_VERSION = "ccr-trajectory-diagnostic-prompt-v2"
FACETS_FILE = "diagnostic-facets.jsonl"
MANIFEST_FILE = "diagnostic-manifest.json"
DEFAULT_UNCACHED_LIMIT = 20
MAX_DIAGNOSTIC_DIGEST_BYTES = 24_000
TERMINAL_EVIDENCE_STEPS = 4
ESTIMATED_BYTES_PER_TOKEN = 4

Diagnose = Callable[[str], JudgeResult]


@dataclass(frozen=True, slots=True)
class DiagnosticCandidate:
    trajectory: Trajectory
    analysis: dict[str, Any]
    priority: int
    signals: tuple[str, ...]
    duration_sec: float
    total_tokens: int


@dataclass(frozen=True, slots=True)
class DiagnosticPrompt:
    digest: str
    source_bytes: int
    digest_bytes: int
    truncated_bytes: int
    total_steps: int
    expanded_steps: int
    estimated_input_tokens: int


@dataclass(frozen=True, slots=True)
class DiagnosticJob:
    candidate: DiagnosticCandidate
    prompt: DiagnosticPrompt
    version_key: str
    cache_path: Path
    cached_diagnostic: dict[str, Any] | None = None


@dataclass(frozen=True, slots=True)
class DiagnosticPlan:
    artifact: TrajectoryRunArtifact
    model: str
    uncached_limit: int
    candidates: tuple[DiagnosticCandidate, ...]
    jobs: tuple[DiagnosticJob, ...]
    skipped_uncached: int

    @property
    def cache_hits(self) -> int:
        return sum(job.cached_diagnostic is not None for job in self.jobs)

    @property
    def uncached_jobs(self) -> int:
        return sum(job.cached_diagnostic is None for job in self.jobs)

    def summary(self) -> dict[str, Any]:
        signal_counts = Counter(
            signal for candidate in self.candidates for signal in candidate.signals
        )
        fresh_prompts = [job.prompt for job in self.jobs if job.cached_diagnostic is None]
        estimates = [prompt.estimated_input_tokens for prompt in fresh_prompts]
        return {
            "planner_version": PLANNER_VERSION,
            "dataset_id": self.artifact.dataset.dataset_id,
            "dataset_version": self.artifact.dataset.version,
            "run_id": self.artifact.run.run_id,
            "model": self.model,
            "trajectories": len(self.artifact.dataset.trajectories),
            "candidates": len(self.candidates),
            "selected": len(self.jobs),
            "cache_hits": self.cache_hits,
            "uncached_planned": self.uncached_jobs,
            "uncached_limit": self.uncached_limit,
            "skipped_uncached": self.skipped_uncached,
            "signals": dict(sorted(signal_counts.items())),
            "prompt": {
                "max_digest_bytes": MAX_DIAGNOSTIC_DIGEST_BYTES,
                "source_bytes": sum(prompt.source_bytes for prompt in fresh_prompts),
                "digest_bytes": sum(prompt.digest_bytes for prompt in fresh_prompts),
                "truncated_bytes": sum(
                    prompt.truncated_bytes for prompt in fresh_prompts
                ),
                "truncated_prompts": sum(
                    prompt.truncated_bytes > 0 for prompt in fresh_prompts
                ),
                "total_steps": sum(prompt.total_steps for prompt in fresh_prompts),
                "expanded_steps": sum(
                    prompt.expanded_steps for prompt in fresh_prompts
                ),
            },
            "input_estimate": {
                "method": "utf8_bytes_div_4",
                "calls": len(estimates),
                "input_tokens": sum(estimates),
                "input_tokens_range": {
                    "low": math.floor(sum(estimates) * 0.8),
                    "high": math.ceil(sum(estimates) * 1.35),
                },
                "min_input_tokens": min(estimates, default=0),
                "median_input_tokens": _median(estimates),
                "max_input_tokens": max(estimates, default=0),
            },
        }


def rank_diagnostic_candidates(
    artifact: TrajectoryRunArtifact,
) -> tuple[DiagnosticCandidate, ...]:
    """Rank observable trajectory problems without re-running eval components."""

    analyzed = [
        (trajectory, objective_analysis_from_run(trajectory, artifact.run))
        for trajectory in artifact.dataset.trajectories
    ]
    durations = [
        float(analysis["duration_sec"])
        for _, analysis in analyzed
        if float(analysis["duration_sec"]) > 0
    ]
    tokens = [
        _total_tokens(analysis)
        for _, analysis in analyzed
        if _total_tokens(analysis) > 0
    ]
    duration_p95 = _p95(durations) if len(durations) >= 20 else None
    tokens_p95 = _p95(tokens) if len(tokens) >= 20 else None

    candidates = []
    for trajectory, analysis in analyzed:
        priority = 0
        signals: list[str] = []
        outcome = trajectory.execution.outcome if trajectory.execution else "unknown"
        if outcome != "completed":
            priority = 500
            _append_signal(signals, f"execution:{outcome}")

        for failure in analysis["failures"]:
            priority = max(priority, 475 if failure.get("impact") == "execution" else 450)
            _append_signal(signals, f"failure:{failure.get('key') or 'unknown'}")
        for failure in analysis["tool_failures"]:
            priority = max(priority, 450)
            _append_signal(signals, f"tool_failure:{failure.get('tool') or 'unknown'}")
        for evaluation in analysis["evaluations"]:
            if evaluation.get("verdict") == "fail":
                priority = max(priority, 400)
                _append_signal(
                    signals,
                    f"evaluation:{evaluation.get('evaluator_id') or 'unknown'}",
                )
        for detection in analysis["detections"]:
            for finding in detection.get("findings") or ():
                severity = str(finding.get("severity") or "info")
                priority = max(
                    priority,
                    {"error": 350, "warning": 300, "info": 100}.get(
                        severity, 100
                    ),
                )
                _append_signal(
                    signals,
                    f"finding:{finding.get('code') or 'unknown'}",
                )

        duration_sec = float(analysis["duration_sec"])
        total_tokens = _total_tokens(analysis)
        if duration_p95 is not None and duration_sec >= duration_p95:
            priority = max(priority, 200)
            _append_signal(signals, "cost:duration_p95")
        if tokens_p95 is not None and total_tokens >= tokens_p95:
            priority = max(priority, 200)
            _append_signal(signals, "cost:tokens_p95")
        if not signals:
            continue
        candidates.append(
            DiagnosticCandidate(
                trajectory=trajectory,
                analysis=analysis,
                priority=priority,
                signals=tuple(signals),
                duration_sec=duration_sec,
                total_tokens=total_tokens,
            )
        )

    candidates.sort(
        key=lambda item: (
            -item.priority,
            -item.duration_sec,
            -item.total_tokens,
            item.trajectory.trajectory_id,
        )
    )
    return tuple(candidates)


def plan_diagnostics(
    artifact: TrajectoryRunArtifact,
    *,
    cache_dir: Path,
    model: str,
    uncached_limit: int = DEFAULT_UNCACHED_LIMIT,
) -> DiagnosticPlan:
    """Select every valid cache hit plus a bounded number of fresh diagnoses."""

    if uncached_limit < 0:
        raise ValueError("uncached_limit must be non-negative")
    candidates = rank_diagnostic_candidates(artifact)
    jobs = []
    uncached = 0
    skipped = 0
    for candidate in candidates:
        prompt = build_diagnostic_prompt(candidate)
        version_key = diagnostic_version_key(prompt, model)
        cache_path = _cache_path(
            cache_dir,
            artifact.dataset.dataset_id,
            candidate.trajectory.trajectory_id,
            version_key,
        )
        cached = _read_cached_diagnostic(cache_path, version_key)
        if cached is not None:
            jobs.append(
                DiagnosticJob(
                    candidate=candidate,
                    prompt=prompt,
                    version_key=version_key,
                    cache_path=cache_path,
                    cached_diagnostic=cached,
                )
            )
            continue
        if uncached >= uncached_limit:
            skipped += 1
            continue
        uncached += 1
        jobs.append(
            DiagnosticJob(
                candidate=candidate,
                prompt=prompt,
                version_key=version_key,
                cache_path=cache_path,
            )
        )
    return DiagnosticPlan(
        artifact=artifact,
        model=model,
        uncached_limit=uncached_limit,
        candidates=candidates,
        jobs=tuple(jobs),
        skipped_uncached=skipped,
    )


def build_diagnostic_prompt(candidate: DiagnosticCandidate) -> DiagnosticPrompt:
    """Project one bounded, evidence-addressable prompt from a candidate."""

    trajectory = candidate.trajectory
    steps = list(trajectory.steps)
    selected_indexes = _evidence_step_indexes(candidate)
    selected_steps = tuple(steps[index] for index in selected_indexes)
    outline = [
        {
            "step_id": step.step_id,
            "operation": step.operation,
            "name": step.name,
            "status": step.status,
            "duration_ms": round(step.duration_ms),
        }
        for step in steps
    ]
    evidence_trajectory = replace(
        trajectory,
        steps=selected_steps,
        metadata={
            "generation": dict(trajectory.generation),
            "total_steps": len(steps),
            "expanded_steps": len(selected_steps),
        },
    )
    source = "\n".join(
        (
            f"diagnostic signals: {json.dumps(candidate.signals, ensure_ascii=False)}",
            f"step outline: {json.dumps(outline, ensure_ascii=False)}",
            "expanded evidence:",
            chain_digest(
                evidence_trajectory,
                _compact_objective_analysis(candidate),
            ),
        )
    )
    digest, truncated_bytes = _bounded_utf8(source, MAX_DIAGNOSTIC_DIGEST_BYTES)
    prompt_bytes = len(f"{JUDGE_SYSTEM}\n\n{digest}".encode("utf-8"))
    return DiagnosticPrompt(
        digest=digest,
        source_bytes=len(source.encode("utf-8")),
        digest_bytes=len(digest.encode("utf-8")),
        truncated_bytes=truncated_bytes,
        total_steps=len(steps),
        expanded_steps=len(selected_steps),
        estimated_input_tokens=math.ceil(
            prompt_bytes / ESTIMATED_BYTES_PER_TOKEN
        ),
    )


def _evidence_step_indexes(candidate: DiagnosticCandidate) -> tuple[int, ...]:
    steps = list(candidate.trajectory.steps)
    indexes_by_id = {step.step_id: index for index, step in enumerate(steps)}
    referenced_ids = {
        str(step_id)
        for evaluation in candidate.analysis["evaluations"]
        if evaluation.get("verdict") == "fail"
        for step_id in evaluation.get("step_ids") or ()
    }
    referenced_ids.update(
        str(step_id)
        for detection in candidate.analysis["detections"]
        for finding in detection.get("findings") or ()
        for step_id in finding.get("step_ids") or ()
    )
    seed_indexes = {
        indexes_by_id[step_id]
        for step_id in referenced_ids
        if step_id in indexes_by_id
    }
    seed_indexes.update(
        index
        for index, step in enumerate(steps)
        if step.status == "error" or step.failure is not None
    )
    selected = {
        index
        for seed in seed_indexes
        for index in (seed - 1, seed, seed + 1)
        if 0 <= index < len(steps)
    }
    selected.update(
        index for index, step in enumerate(steps) if step.operation == "context"
    )
    selected.update(range(max(0, len(steps) - TERMINAL_EVIDENCE_STEPS), len(steps)))
    return tuple(sorted(selected))


def _compact_objective_analysis(
    candidate: DiagnosticCandidate,
) -> dict[str, Any]:
    analysis = candidate.analysis
    return {
        "signals": list(candidate.signals),
        "stage": analysis["stage"],
        "score": analysis["score"],
        "rounds": analysis["rounds"],
        "duration_sec": analysis["duration_sec"],
        "model_usage": analysis["model_usage"],
        "tool_freq": analysis["tool_freq"],
        "failures": analysis["failures"],
        "tool_failures": analysis["tool_failures"],
        "failed_evaluations": [
            evaluation
            for evaluation in analysis["evaluations"]
            if evaluation.get("verdict") == "fail"
        ],
        "findings": [
            finding
            for detection in analysis["detections"]
            for finding in detection.get("findings") or ()
        ],
        "empty_args": analysis["empty_args"],
        "code_searches": analysis["code_searches"],
        "file_reads": analysis["file_reads"],
        "prompt_overlap": analysis["prompt_overlap"],
        "initial_context": analysis["initial_context"],
        "adjacent_file_reads": analysis["adjacent_file_reads"],
        "read_batching": analysis["read_batching"],
        "search_then_read": analysis["search_then_read"],
        "repeated_reads": analysis["repeated_reads"],
        "hypothesis_yield": analysis["hypothesis_yield"],
        "assessment_count": analysis["assessment_count"],
    }


def _bounded_utf8(value: str, max_bytes: int) -> tuple[str, int]:
    encoded = value.encode("utf-8")
    if len(encoded) <= max_bytes:
        return value, 0
    marker = b"\n... middle evidence omitted to fit the bounded digest ...\n"
    available = max_bytes - len(marker)
    head_bytes = available * 2 // 3
    tail_bytes = available - head_bytes
    bounded = encoded[:head_bytes] + marker + encoded[-tail_bytes:]
    decoded = bounded.decode("utf-8", errors="ignore")
    return decoded, len(encoded) - len(decoded.encode("utf-8"))


def run_diagnostics(
    plan: DiagnosticPlan,
    *,
    output_dir: Path,
    diagnose: Diagnose,
    source_run_dir: Path | None = None,
) -> dict[str, Any]:
    """Generate missing facets, persist them incrementally, and write run coverage."""

    records = []
    errors = []
    generated = 0
    actual_usage = _empty_usage()
    for index, job in enumerate(plan.jobs, start=1):
        candidate = job.candidate
        diagnostic = job.cached_diagnostic
        cache_status = "cached"
        if diagnostic is None:
            print(
                f"diagnosing {index}/{len(plan.jobs)} "
                f"{candidate.trajectory.trajectory_id}",
                file=sys.stderr,
            )
            actual_usage["model_call_count"] += 1
            try:
                result = diagnose(job.prompt.digest)
                _add_usage(actual_usage, result.usage)
                diagnostic = _normalize_diagnostic(result.diagnostic)
                _write_json(
                    job.cache_path,
                    {
                        "schema_version": DIAGNOSTIC_SCHEMA_VERSION,
                        "version_key": job.version_key,
                        "prompt_version": PROMPT_VERSION,
                        "model": plan.model,
                        "trajectory_id": candidate.trajectory.trajectory_id,
                        "created_at": datetime.now(timezone.utc).isoformat(),
                        "diagnostic": diagnostic,
                    },
                )
                cache_status = "generated"
                generated += 1
            except Exception as error:  # one diagnostic must not discard other work
                errors.append(
                    {
                        "trajectory_id": candidate.trajectory.trajectory_id,
                        "error": f"{type(error).__name__}: {error}"[:300],
                    }
                )
                continue
        records.append(
            {
                "schema_version": DIAGNOSTIC_SCHEMA_VERSION,
                "trajectory_id": candidate.trajectory.trajectory_id,
                "recording_id": candidate.trajectory.recording_id,
                "target": plan.artifact.run.target_for(
                    candidate.trajectory.trajectory_id
                ),
                "generation": dict(candidate.trajectory.generation),
                "priority": candidate.priority,
                "signals": list(candidate.signals),
                "model": plan.model,
                "cache_key": job.version_key,
                "cache_status": cache_status,
                "prompt": {
                    "digest_bytes": job.prompt.digest_bytes,
                    "source_bytes": job.prompt.source_bytes,
                    "truncated_bytes": job.prompt.truncated_bytes,
                    "total_steps": job.prompt.total_steps,
                    "expanded_steps": job.prompt.expanded_steps,
                    "estimated_input_tokens": job.prompt.estimated_input_tokens,
                },
                "diagnostic": diagnostic,
            }
        )

    output_dir.mkdir(parents=True, exist_ok=True)
    facets_path = output_dir / FACETS_FILE
    manifest_path = output_dir / MANIFEST_FILE
    _write_jsonl(facets_path, records)
    candidate_count = len(plan.candidates)
    manifest = {
        "schema_version": DIAGNOSTIC_SCHEMA_VERSION,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "source": {
            "run_dir": str(source_run_dir) if source_run_dir is not None else "",
            "dataset_id": plan.artifact.dataset.dataset_id,
            "dataset_version": plan.artifact.dataset.version,
            "run_id": plan.artifact.run.run_id,
            "trajectories": len(plan.artifact.dataset.trajectories),
        },
        "judge": {
            "model": plan.model,
            "prompt_version": PROMPT_VERSION,
            "taxonomy": list(TAXONOMY),
        },
        "plan": plan.summary(),
        "coverage": {
            "facets": len(records),
            "cache_hits": plan.cache_hits,
            "generated": generated,
            "errors": len(errors),
            "skipped_uncached": plan.skipped_uncached,
            "diagnostic_coverage": (
                round(len(records) / candidate_count, 3)
                if candidate_count
                else None
            ),
        },
        "usage": _finalize_usage(actual_usage),
        "errors": errors,
        "artifacts": [FACETS_FILE, MANIFEST_FILE],
    }
    _write_json(manifest_path, manifest)
    return manifest


def diagnostic_version_key(prompt: DiagnosticPrompt, model: str) -> str:
    value = {
        "schema_version": DIAGNOSTIC_SCHEMA_VERSION,
        "prompt_version": PROMPT_VERSION,
        "taxonomy": TAXONOMY,
        "model": model,
        "digest": prompt.digest,
    }
    encoded = json.dumps(
        value, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode()
    return hashlib.sha256(encoded).hexdigest()


def default_cache_dir() -> Path:
    return Path.home() / ".casecodereview" / "eval-cache" / "trajectory-diagnostics"


def _empty_usage() -> dict[str, int]:
    return {
        "model_call_count": 0,
        "usage_reported_call_count": 0,
        "input_tokens": 0,
        "cached_input_tokens": 0,
        "output_tokens": 0,
        "total_tokens": 0,
    }


def _add_usage(total: dict[str, int], usage: dict[str, int]) -> None:
    if usage:
        total["usage_reported_call_count"] += 1
    for key in (
        "input_tokens",
        "cached_input_tokens",
        "output_tokens",
        "total_tokens",
    ):
        total[key] += int(usage.get(key) or 0)


def _finalize_usage(total: dict[str, int]) -> dict[str, Any]:
    calls = total["model_call_count"]
    return {
        **total,
        "usage_coverage_ratio": (
            round(total["usage_reported_call_count"] / calls, 6)
            if calls
            else None
        ),
        "uncached_input_tokens": max(
            0, total["input_tokens"] - total["cached_input_tokens"]
        ),
    }


def _total_tokens(analysis: dict[str, Any]) -> int:
    measurements = analysis["model_usage"].get("measurements") or {}
    return int(measurements.get("total_tokens") or 0)


def _p95(values: list[float] | list[int]) -> float:
    ordered = sorted(float(value) for value in values)
    index = max(0, (95 * len(ordered) + 99) // 100 - 1)
    return ordered[index]


def _median(values: list[int]) -> int:
    if not values:
        return 0
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[middle]
    return round((ordered[middle - 1] + ordered[middle]) / 2)


def _append_signal(signals: list[str], signal: str) -> None:
    if signal not in signals:
        signals.append(signal)


def _cache_path(
    cache_dir: Path,
    dataset_id: str,
    trajectory_id: str,
    version_key: str,
) -> Path:
    identity = hashlib.sha256(f"{dataset_id}:{trajectory_id}".encode()).hexdigest()
    return cache_dir / identity / f"{version_key}.json"


def _read_cached_diagnostic(
    path: Path, version_key: str
) -> dict[str, Any] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    diagnostic = value.get("diagnostic")
    if (
        value.get("schema_version") != DIAGNOSTIC_SCHEMA_VERSION
        or value.get("version_key") != version_key
        or not isinstance(diagnostic, dict)
    ):
        return None
    return _normalize_diagnostic(diagnostic)


def _normalize_diagnostic(value: dict[str, Any]) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError("diagnostic must be a JSON object")
    categories = []
    for category in value.get("categories") or ():
        if not isinstance(category, dict) or category.get("type") not in TAXONOMY:
            continue
        categories.append(
            {
                "type": str(category["type"]),
                "evidence": str(category.get("evidence") or ""),
                "suggestion": str(category.get("suggestion") or ""),
                "confidence": float(category.get("confidence") or 0),
            }
        )
    return {"categories": categories, "summary": str(value.get("summary") or "")}


def _write_json(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(
        json.dumps(value, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    temporary.replace(path)


def _write_jsonl(path: Path, values: list[dict[str, Any]]) -> None:
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(
        "".join(json.dumps(value, ensure_ascii=False) + "\n" for value in values),
        encoding="utf-8",
    )
    temporary.replace(path)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_dir", type=Path, help="canonical trajectory run directory")
    parser.add_argument("--out-dir", type=Path)
    parser.add_argument("--cache-dir", type=Path, default=default_cache_dir())
    parser.add_argument("--model", help="routing alias/model for the diagnostic judge")
    parser.add_argument(
        "--uncached-limit",
        type=int,
        default=DEFAULT_UNCACHED_LIMIT,
        help="maximum fresh LLM diagnoses; valid cache hits do not count",
    )
    parser.add_argument(
        "--plan-only",
        action="store_true",
        help="show candidate/cache coverage without calling the judge",
    )
    args = parser.parse_args()

    artifact = load_run_artifact(args.run_dir)
    llm = load_llm(args.model)
    plan = plan_diagnostics(
        artifact,
        cache_dir=args.cache_dir,
        model=llm[2],
        uncached_limit=args.uncached_limit,
    )
    if args.plan_only:
        print(json.dumps(plan.summary(), indent=2, ensure_ascii=False))
        return 0
    manifest = run_diagnostics(
        plan,
        output_dir=args.out_dir or args.run_dir,
        source_run_dir=args.run_dir,
        diagnose=lambda digest: judge_chain_with_usage(*llm, digest),
    )
    print(json.dumps(manifest, indent=2, ensure_ascii=False))
    return 1 if manifest["coverage"]["errors"] else 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Pure metric aggregation for CCR trajectory report rows."""

from __future__ import annotations

import math
from collections import Counter
from typing import Any

from ccr_trajectory import UNKNOWN_STAGE
from trajectory_judge import main_deductions


def _percentile(values: list[float], percentile: float) -> float | int | None:
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, math.ceil(percentile * len(ordered)) - 1)
    value = ordered[index]
    return int(value) if float(value).is_integer() else round(value, 3)


def distribution(values: list[float]) -> dict[str, float | int | None]:
    if not values:
        return {"total": 0, "average": None, "p50": None, "p95": None}
    total = sum(values)
    average = total / len(values)
    return {
        "total": int(total) if float(total).is_integer() else round(total, 3),
        "average": round(average, 3),
        "p50": _percentile(values, 0.50),
        "p95": _percentile(values, 0.95),
    }


def _average_per(values: list[float], count: int) -> float | None:
    return round(sum(values) / count, 3) if count else None


def _ratio(numerator: int, denominator: int) -> float | None:
    return round(numerator / denominator, 3) if denominator else None


def aggregate_stage(rows: list[dict[str, Any]], stage: str) -> dict[str, Any]:
    selected = [row for row in rows if row["stage"] == stage]
    outcomes = Counter(row["outcome"] for row in selected)
    scores = [float(row["score"]) for row in selected if row["score"] is not None]
    tools: Counter[str] = Counter()
    failure_events: Counter[tuple[str, str]] = Counter()
    failure_affected: dict[tuple[str, str], set[int]] = {}
    diagnostic_events: Counter[tuple[str, str]] = Counter()
    diagnostic_affected: dict[tuple[str, str], set[int]] = {}
    search_purposes: Counter[str] = Counter()
    search_calls = 0
    search_requests = 0
    context_requests = 0
    requested_context_lines = 0
    returned_context_lines = 0
    context_truncated_requests = 0
    context_unavailable_requests = 0
    hit_search_requests = 0
    follow_up_read_requests = 0
    context_hit_search_requests = 0
    context_follow_up_read_requests = 0
    plain_hit_search_requests = 0
    plain_follow_up_read_requests = 0
    initial_outline_attempts = 0
    outline_admitted_bytes = 0
    outline_outcomes: Counter[str] = Counter()
    outline_by_language: dict[str, Counter[str]] = {}

    for row_index, row in enumerate(selected):
        tools.update(row["tool_freq"])
        analysis = row.get("analysis") or {}
        code_searches = analysis.get("code_searches") or {}
        search_calls += int(code_searches.get("calls") or 0)
        search_requests += int(code_searches.get("requests") or 0)
        context_requests += int(code_searches.get("context_requests") or 0)
        requested_context_lines += int(
            code_searches.get("requested_context_lines") or 0
        )
        returned_context_lines += int(code_searches.get("returned_context_lines") or 0)
        context_truncated_requests += int(
            code_searches.get("context_truncated_requests") or 0
        )
        context_unavailable_requests += int(
            code_searches.get("context_unavailable_requests") or 0
        )

        search_follow_up = analysis.get("search_then_read") or {}
        hit_search_requests += int(
            search_follow_up.get("hit_search_request_count") or 0
        )
        follow_up_read_requests += int(
            search_follow_up.get("follow_up_read_request_count") or 0
        )
        context_hit_search_requests += int(
            search_follow_up.get("context_hit_search_request_count") or 0
        )
        context_follow_up_read_requests += int(
            search_follow_up.get("context_follow_up_read_request_count") or 0
        )
        plain_hit_search_requests += int(
            search_follow_up.get("plain_hit_search_request_count") or 0
        )
        plain_follow_up_read_requests += int(
            search_follow_up.get("plain_follow_up_read_request_count") or 0
        )

        outlines = (analysis.get("initial_context") or {}).get("outlines") or {}
        initial_outline_attempts += int(outlines.get("attempts") or 0)
        outline_admitted_bytes += int(outlines.get("admitted_bytes") or 0)
        outline_outcomes.update(
            {
                str(outcome): int(count)
                for outcome, count in (outlines.get("outcomes") or {}).items()
            }
        )
        for language, language_outcomes in (outlines.get("by_language") or {}).items():
            outline_by_language.setdefault(str(language), Counter()).update(
                {
                    str(outcome): int(event_count)
                    for outcome, event_count in (language_outcomes or {}).items()
                }
            )

        search_purposes.update(
            {
                str(purpose): int(requests)
                for purpose, requests in (
                    code_searches.get("purpose_counts") or {}
                ).items()
            }
        )
        for failure in analysis.get("failures") or []:
            key = (
                str(failure.get("impact") or "step"),
                str(failure.get("key") or "unknown.unknown.unknown"),
            )
            failure_events[key] += 1
            failure_affected.setdefault(key, set()).add(row_index)
        for evaluation in analysis.get("evaluations") or []:
            for finding in evaluation.get("findings") or []:
                key = (
                    str(finding.get("severity") or "info"),
                    str(finding.get("code") or "unknown"),
                )
                diagnostic_events[key] += 1
                diagnostic_affected.setdefault(key, set()).add(row_index)

    count = len(selected)
    known_outcomes = count - outcomes["unknown"]
    assessments = sum(
        int((row.get("analysis") or {}).get("assessment_count") or 0)
        for row in selected
    )
    durations = [float(row["duration_sec"]) for row in selected]
    prompt_tokens = [float(row["prompt_tokens"]) for row in selected]
    completion_tokens = [float(row["completion_tokens"]) for row in selected]
    cached_tokens = [float(row.get("cached_tokens", 0)) for row in selected]
    uncached_tokens = [
        float(
            row.get("uncached_tokens")
            if row.get("uncached_tokens") is not None
            else max(
                float(row.get("prompt_tokens") or 0)
                - float(row.get("cached_tokens") or 0),
                0,
            )
        )
        for row in selected
    ]
    total_tokens = [
        float(
            row.get("total_tokens")
            if row.get("total_tokens") is not None
            else float(row.get("prompt_tokens") or 0)
            + float(row.get("completion_tokens") or 0)
        )
        for row in selected
    ]
    model_calls = [float(row.get("model_calls", 0)) for row in selected]
    model_call_count = sum(int(row.get("model_calls") or 0) for row in selected)
    usage_reported_calls = sum(
        int(row.get("usage_reported_calls") or 0) for row in selected
    )
    failure_items = [
        {
            "impact": impact,
            "failure": failure,
            "count": event_count,
            "affected_chains": len(failure_affected[(impact, failure)]),
            "rate": round(len(failure_affected[(impact, failure)]) / count, 3),
        }
        for (impact, failure), event_count in sorted(
            failure_events.items(),
            key=lambda item: (-len(failure_affected[item[0]]), item[0]),
        )
    ]
    execution_failure_rates = {
        item["failure"]: item["rate"]
        for item in failure_items
        if item["impact"] == "execution"
    }
    diagnostic_items = [
        {
            "severity": severity,
            "finding": signal,
            "count": event_count,
            "affected_chains": len(diagnostic_affected[(severity, signal)]),
            "rate": round(len(diagnostic_affected[(severity, signal)]) / count, 3),
        }
        for (severity, signal), event_count in sorted(
            diagnostic_events.items(),
            key=lambda item: (-len(diagnostic_affected[item[0]]), item[0]),
        )
    ]
    labeled_search_requests = sum(
        requests
        for purpose, requests in search_purposes.items()
        if purpose != "(unspecified)"
    )

    return {
        "chains": count,
        "outcomes": dict(sorted(outcomes.items())),
        "outcome_coverage": round(known_outcomes / count, 3) if count else None,
        "completion_rate": round(outcomes["completed"] / known_outcomes, 3)
        if known_outcomes
        else None,
        "workflow_timeout_rate": execution_failure_rates.get(
            "workflow.timeout", 0.0 if count else None
        ),
        "llm_routing_timeout_rate": execution_failure_rates.get(
            "llm.routing.timeout", 0.0 if count else None
        ),
        "average_score": round(sum(scores) / len(scores), 3) if scores else None,
        "assessments": assessments,
        "rounds": distribution([float(row["rounds"]) for row in selected]),
        "duration_sec": distribution(durations),
        "prompt_tokens": distribution(prompt_tokens),
        "completion_tokens": distribution(completion_tokens),
        "cached_tokens": distribution(cached_tokens),
        "uncached_tokens": distribution(uncached_tokens),
        "total_tokens": distribution(total_tokens),
        "model_calls": distribution(model_calls),
        "usage_coverage": round(usage_reported_calls / model_call_count, 3)
        if model_call_count
        else None,
        "per_assessment": {
            "duration_sec": _average_per(durations, assessments),
            "prompt_tokens": _average_per(prompt_tokens, assessments),
            "completion_tokens": _average_per(completion_tokens, assessments),
            "cached_tokens": _average_per(cached_tokens, assessments),
            "total_tokens": _average_per(total_tokens, assessments),
        },
        "tool_freq": dict(sorted(tools.items(), key=lambda item: (-item[1], item[0]))),
        "code_searches": {
            "calls": search_calls,
            "requests": search_requests,
            "purpose_counts": dict(
                sorted(search_purposes.items(), key=lambda item: (-item[1], item[0]))
            ),
            "purpose_coverage": round(labeled_search_requests / search_requests, 3)
            if search_requests
            else None,
            "context_requests": context_requests,
            "context_request_rate": round(context_requests / search_requests, 3)
            if search_requests
            else None,
            "requested_context_lines": requested_context_lines,
            "returned_context_lines": returned_context_lines,
            "context_truncated_requests": context_truncated_requests,
            "context_unavailable_requests": context_unavailable_requests,
        },
        "search_follow_up": {
            "hit_search_requests": hit_search_requests,
            "follow_up_read_requests": follow_up_read_requests,
            "follow_up_read_rate": _ratio(follow_up_read_requests, hit_search_requests),
            "context_hit_search_requests": context_hit_search_requests,
            "context_follow_up_read_requests": context_follow_up_read_requests,
            "context_follow_up_read_rate": _ratio(
                context_follow_up_read_requests, context_hit_search_requests
            ),
            "plain_hit_search_requests": plain_hit_search_requests,
            "plain_follow_up_read_requests": plain_follow_up_read_requests,
            "plain_follow_up_read_rate": _ratio(
                plain_follow_up_read_requests, plain_hit_search_requests
            ),
        },
        "initial_outlines": {
            "attempts": initial_outline_attempts,
            "admission_rate": _ratio(
                outline_outcomes["admitted"], initial_outline_attempts
            ),
            "admitted_bytes": outline_admitted_bytes,
            "outcomes": dict(sorted(outline_outcomes.items())),
            "by_language": {
                language: dict(sorted(language_outcomes.items()))
                for language, language_outcomes in sorted(outline_by_language.items())
            },
        },
        "failures": {
            "events": sum(failure_events.values()),
            "items": failure_items,
        },
        "diagnostic_findings": {
            "events": sum(diagnostic_events.values()),
            "items": diagnostic_items,
        },
        "main_deductions": main_deductions([row["analysis"] for row in selected]),
    }


def aggregate_cohorts(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Keep model/tool/repository changes visible inside one report window."""

    grouped: dict[tuple[str, str, str, str], list[dict[str, Any]]] = {}
    for row in rows:
        key = (
            str(row.get("stage") or UNKNOWN_STAGE),
            str(row.get("tool_version") or "unknown"),
            str(row.get("model") or "unknown"),
            str(row.get("repository") or "unknown"),
        )
        grouped.setdefault(key, []).append(row)
    cohorts = []
    for (stage, tool_version, model, repository), members in sorted(grouped.items()):
        metrics = aggregate_stage(members, stage)
        cohorts.append(
            {
                "stage": stage,
                "tool_version": tool_version,
                "model": model,
                "repository": repository,
                "chains": metrics["chains"],
                "completion_rate": metrics["completion_rate"],
                "duration_sec": metrics["duration_sec"],
                "prompt_tokens": metrics["prompt_tokens"],
                "total_tokens": metrics["total_tokens"],
                "code_searches": metrics["code_searches"],
                "search_follow_up": metrics["search_follow_up"],
                "initial_outlines": metrics["initial_outlines"],
            }
        )
    return cohorts

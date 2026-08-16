"""Markdown presentation for the weekly CCR eval read model."""

from __future__ import annotations

from typing import Any

from ccr_trajectory import REVIEW1, REVIEW2


def _format_value(value: Any, kind: str = "number") -> str:
    if value is None:
        return "—"
    if kind == "percent":
        return f"{float(value) * 100:.1f}%"
    if kind == "score":
        return f"{float(value):.3f}"
    if isinstance(value, float):
        return f"{value:,.1f}"
    return f"{value:,}" if isinstance(value, int) else str(value)


def _format_delta(item: dict[str, Any]) -> str:
    delta = item["delta"]
    if delta is None:
        return "—"
    if item["kind"] == "percent":
        return f"{delta * 100:+.1f} pp"
    if item["kind"] == "score":
        return f"{delta:+.3f}"
    change = item["change_pct"]
    suffix = f" ({change:+.1%})" if change is not None else ""
    return f"{delta:+,.1f}{suffix}"


def _markdown_table(headers: list[str], rows: list[list[str]]) -> list[str]:
    escaped = [
        [str(value).replace("|", "\\|").replace("\n", " ") for value in row]
        for row in rows
    ]
    return [
        "| " + " | ".join(headers) + " |",
        "|" + "|".join("---" for _ in headers) + "|",
        *("| " + " | ".join(row) + " |" for row in escaped),
    ]


def render_markdown(
    current: dict[str, Any],
    previous: dict[str, Any],
    comparison: list[dict[str, Any]],
    unit_durations: list[dict[str, Any]],
) -> str:
    lines = [
        f"# CCR weekly eval — {current['week']}",
        "",
        f"Period: `{current['window']['start']}` → `{current['window']['end']}`  ",
        f"Comparison: `{previous['week']}`",
        "",
        "## Week-over-week",
        "",
    ]
    lines.extend(
        _markdown_table(
            ["Metric", current["week"], previous["week"], "Delta"],
            [
                [
                    item["metric"],
                    _format_value(item["current"], item["kind"]),
                    _format_value(item["previous"], item["kind"]),
                    _format_delta(item),
                ]
                for item in comparison
            ],
        )
    )

    sessions = current["sessions"]
    lines.extend(
        [
            "",
            "## Current-week cohort",
            "",
            f"Sessions: **{sessions['total']}** across **{sessions['repositories']}** repositories; "
            f"closed={sessions['closed']}, unclosed={sessions['unclosed']}, "
            f"finding events={sessions['finding_events']}.",
            "",
            "### Engine mix",
            "",
        ]
    )
    mix_rows = [
        ["tool version", name, str(count)]
        for name, count in sessions["by_tool_version"].items()
    ] + [["model", name, str(count)] for name, count in sessions["by_model"].items()]
    lines.extend(
        _markdown_table(["Dimension", "Value", "Sessions"], mix_rows)
        if mix_rows
        else ["No sessions in this week."]
    )

    lines.extend(["", "### Review stages", ""])
    stage_rows = []
    for stage, title in ((REVIEW1, "Review 1 · Unit"), (REVIEW2, "Review 2 · Lane")):
        metrics = current[stage]
        stage_rows.append(
            [
                title,
                str(metrics["chains"]),
                _format_value(metrics["outcome_coverage"], "percent"),
                _format_value(metrics["completion_rate"], "percent"),
                _format_value(metrics["workflow_timeout_rate"], "percent"),
                _format_value(metrics["llm_routing_timeout_rate"], "percent"),
                _format_value(metrics["average_score"], "score"),
                _format_value(metrics["assessments"] if stage == REVIEW2 else None),
                _format_value(metrics["rounds"]["p50"]),
                _format_value(metrics["duration_sec"]["average"]),
                _format_value(metrics["per_assessment"]["duration_sec"]),
                _format_value(metrics["duration_sec"]["p50"]),
                _format_value(metrics["duration_sec"]["p95"]),
            ]
        )
    lines.extend(
        _markdown_table(
            [
                "Stage",
                "Chains",
                "Outcome coverage",
                "Complete",
                "Workflow timeout",
                "llm.routing.timeout",
                "Score",
                "Assessments",
                "p50 rounds",
                "avg sec/chain",
                "avg sec/Assessment",
                "p50 sec",
                "p95 sec",
            ],
            stage_rows,
        )
    )

    lines.extend(["", "### Failures", ""])
    failure_rows = []
    for stage, title in ((REVIEW1, "Review 1"), (REVIEW2, "Review 2")):
        for item in current[stage]["failures"]["items"]:
            failure_rows.append(
                [
                    title,
                    str(item["impact"]),
                    str(item["failure"]),
                    str(item["count"]),
                    str(item["affected_chains"]),
                    _format_value(item["rate"], "percent"),
                ]
            )
    lines.extend(
        _markdown_table(
            ["Stage", "Impact", "Failure", "Events", "Affected chains", "Rate"],
            failure_rows,
        )
        if failure_rows
        else ["No structured failures in this week."]
    )

    lines.extend(["", "### Slowest Review 1 units", ""])
    lines.extend(
        _markdown_table(
            ["Unit", "Duration sec", "Outcome", "Rounds", "Prompt tokens", "Session"],
            [
                [
                    str(record["unit"]),
                    _format_value(record["duration_sec"]),
                    str(record["outcome"]),
                    _format_value(record["rounds"]),
                    _format_value(record["prompt_tokens"]),
                    str(record["session_id"]),
                ]
                for record in unit_durations[:20]
            ],
        )
        if unit_durations
        else ["No Review 1 units in this week."]
    )
    lines.extend(
        [
            "",
            "Complete per-Unit timing records are available in `unit-durations.jsonl`.",
        ]
    )

    lines.extend(["", "### Token usage", ""])
    lines.extend(
        _markdown_table(
            [
                "Stage",
                "Prompt/Unit or Lane",
                "Prompt/Assessment",
                "Completion/Unit or Lane",
                "Completion/Assessment",
                "Cached/Unit or Lane",
                "Cached/Assessment",
                "Total prompt",
                "Total completion",
            ],
            [
                [
                    title,
                    _format_value(current[stage]["prompt_tokens"]["average"]),
                    _format_value(current[stage]["per_assessment"]["prompt_tokens"]),
                    _format_value(current[stage]["completion_tokens"]["average"]),
                    _format_value(
                        current[stage]["per_assessment"]["completion_tokens"]
                    ),
                    _format_value(current[stage]["cached_tokens"]["average"]),
                    _format_value(current[stage]["per_assessment"]["cached_tokens"]),
                    _format_value(current[stage]["prompt_tokens"]["total"]),
                    _format_value(current[stage]["completion_tokens"]["total"]),
                ]
                for stage, title in ((REVIEW1, "Review 1"), (REVIEW2, "Review 2"))
            ],
        )
    )

    lines.extend(["", "### Tool usage", ""])
    tool_names = {
        *current[REVIEW1]["tool_freq"],
        *current[REVIEW2]["tool_freq"],
    }
    ranked_tools = sorted(
        tool_names,
        key=lambda name: (
            -current[REVIEW1]["tool_freq"].get(name, 0)
            - current[REVIEW2]["tool_freq"].get(name, 0),
            name,
        ),
    )
    lines.extend(
        _markdown_table(
            ["Tool", "Review 1 calls", "Review 2 calls"],
            [
                [
                    name,
                    str(current[REVIEW1]["tool_freq"].get(name, 0)),
                    str(current[REVIEW2]["tool_freq"].get(name, 0)),
                ]
                for name in ranked_tools
            ],
        )
        if ranked_tools
        else ["No tool calls in this week."]
    )

    lines.extend(["", "### Main deductions", ""])
    deductions = []
    for stage, title in ((REVIEW1, "Review 1"), (REVIEW2, "Review 2")):
        for item in current[stage]["main_deductions"]:
            deductions.append(
                f"- {title}: `{item['name']}` — {item['count']} chain(s), "
                f"average score {item['average_score']}"
            )
    lines.extend(deductions or ["No recurring deterministic deductions."])

    quality = current["quality"]
    review_week = quality["review_week"]
    labeled_week = quality["labeled_this_week"]
    lines.extend(
        [
            "",
            "## Human labels",
            "",
            f"Review-week examples={review_week['examples']}, "
            f"labeled findings={review_week['labeled_findings']}, "
            f"coverage={_format_value(review_week['label_coverage'], 'percent')}.",
            "",
        ]
    )
    label_names = sorted(set(review_week["by_label"]) | set(labeled_week["by_label"]))
    lines.extend(
        _markdown_table(
            ["Label", "Produced this week", "Labeled this week"],
            [
                [
                    label,
                    str(review_week["by_label"].get(label, 0)),
                    str(labeled_week["by_label"].get(label, 0)),
                ]
                for label in label_names
            ],
        )
        if label_names
        else ["No normalized labels available. Run `build_label_dataset.py` first."]
    )
    wrong_tags = labeled_week["wrong_tags"]
    wrong_tag_text = (
        ", ".join(f"`{tag}`×{count}" for tag, count in wrong_tags.items())
        if wrong_tags
        else "none"
    )
    lines.extend(["", f"Wrong tags added this week: {wrong_tag_text}."])

    data_quality = current["data_quality"]
    lines.extend(
        [
            "",
            "## Data quality",
            "",
            f"- Invalid session files in scan: "
            f"{data_quality['invalid_session_files_in_scan']}",
            f"- Trajectory export failures: {data_quality['trajectory_export_failures']}",
            f"- Missing dataset files: {data_quality['missing_dataset_files']}",
            f"- Invalid dataset lines: {data_quality['invalid_dataset_lines']}",
            f"- Labels added without a matched session: {labeled_week['without_session']}",
            "",
            "Session metrics are grouped by `session_start` in the report timezone. "
            "Review-week quality uses the normalized dataset's `engine.session_id`; "
            "label throughput uses the human label timestamp.",
        ]
    )
    return "\n".join(lines) + "\n"

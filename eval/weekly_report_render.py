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
                _format_value(metrics["code_searches"]["purpose_coverage"], "percent"),
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
                "Search purpose coverage",
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

    lines.extend(["", "### Diagnostic findings", ""])
    diagnostic_rows = []
    for stage, title in ((REVIEW1, "Review 1"), (REVIEW2, "Review 2")):
        for item in current[stage]["diagnostic_findings"]["items"]:
            diagnostic_rows.append(
                [
                    title,
                    str(item["severity"]),
                    str(item["finding"]),
                    str(item["count"]),
                    str(item["affected_chains"]),
                    _format_value(item["rate"], "percent"),
                ]
            )
    lines.extend(
        _markdown_table(
            ["Stage", "Severity", "Finding", "Events", "Affected chains", "Rate"],
            diagnostic_rows,
        )
        if diagnostic_rows
        else ["No diagnostic findings in this week."]
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
                "Input/Unit or Lane",
                "Output/Unit or Lane",
                "Total/Unit or Lane",
                "Total/Assessment",
                "Cached input/Unit or Lane",
                "Uncached input/Unit or Lane",
                "Model calls/Unit or Lane",
                "Usage coverage",
            ],
            [
                [
                    title,
                    _format_value(current[stage]["prompt_tokens"]["average"]),
                    _format_value(current[stage]["completion_tokens"]["average"]),
                    _format_value(current[stage]["total_tokens"]["average"]),
                    _format_value(current[stage]["per_assessment"]["total_tokens"]),
                    _format_value(current[stage]["cached_tokens"]["average"]),
                    _format_value(current[stage]["uncached_tokens"]["average"]),
                    _format_value(current[stage]["model_calls"]["average"]),
                    _format_value(current[stage]["usage_coverage"], "percent"),
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

    lines.extend(["", "### Search purposes", ""])
    purpose_rows = []
    for stage, title in ((REVIEW1, "Review 1"), (REVIEW2, "Review 2")):
        searches = current[stage]["code_searches"]
        for purpose, count in searches["purpose_counts"].items():
            purpose_rows.append(
                [
                    title,
                    purpose,
                    str(count),
                    _format_value(count / searches["requests"], "percent"),
                ]
            )
    lines.extend(
        _markdown_table(
            ["Stage", "Purpose", "Requests", "Share within stage"], purpose_rows
        )
        if purpose_rows
        else ["No search requests in this week."]
    )

    lines.extend(["", "### Search context effectiveness", ""])
    search_effect_rows = []
    for stage, title in ((REVIEW1, "Review 1"), (REVIEW2, "Review 2")):
        searches = current[stage]["code_searches"]
        follow_up = current[stage].get("search_follow_up") or {}
        search_effect_rows.append(
            [
                title,
                f"{searches.get('context_requests', 0)}/{searches['requests']}",
                _format_value(searches.get("context_request_rate"), "percent"),
                str(searches.get("returned_context_lines", 0)),
                _format_value(follow_up.get("context_follow_up_read_rate"), "percent"),
                _format_value(follow_up.get("plain_follow_up_read_rate"), "percent"),
            ]
        )
    lines.extend(
        _markdown_table(
            [
                "Stage",
                "Context requests",
                "Context usage",
                "Context lines returned",
                "Follow-up with context",
                "Follow-up without context",
            ],
            search_effect_rows,
        )
    )

    lines.extend(["", "### Initial FileOutline availability", ""])
    outline_rows = []
    for stage, title in ((REVIEW1, "Review 1"), (REVIEW2, "Review 2")):
        outlines = current[stage].get("initial_outlines") or {}
        for language, outcomes in (outlines.get("by_language") or {}).items():
            attempts = sum(int(count) for count in outcomes.values())
            admitted = int(outcomes.get("admitted") or 0)
            fallback = ", ".join(
                f"{outcome}={count}"
                for outcome, count in outcomes.items()
                if outcome != "admitted" and count
            )
            outline_rows.append(
                [
                    title,
                    language,
                    str(attempts),
                    str(admitted),
                    _format_value(admitted / attempts if attempts else None, "percent"),
                    fallback or "-",
                ]
            )
    lines.extend(
        _markdown_table(
            ["Stage", "Language", "Attempts", "Admitted", "Admission", "Fallbacks"],
            outline_rows,
        )
        if outline_rows
        else ["No Initial FileOutline attempts in this week."]
    )

    lines.extend(["", "### Execution cohorts", ""])
    cohort_rows = [
        [
            cohort["stage"],
            cohort["tool_version"],
            cohort["model"],
            cohort["repository"],
            str(cohort["chains"]),
            _format_value(cohort["completion_rate"], "percent"),
            _format_value(cohort["duration_sec"]["average"]),
            _format_value(cohort["search_follow_up"]["follow_up_read_rate"], "percent"),
        ]
        for cohort in current.get("cohorts") or []
    ]
    lines.extend(
        _markdown_table(
            [
                "Stage",
                "Tool version",
                "Model",
                "Repository",
                "Chains",
                "Completion",
                "Average sec",
                "Search follow-up",
            ],
            cohort_rows,
        )
        if cohort_rows
        else ["No execution cohorts in this week."]
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
    label_dataset = quality["label_dataset"]
    review_week = quality["review_week"]
    labeled_week = quality["labeled_this_week"]
    label_sync = current.get("label_sync") or {
        "status": "missing",
        "latest_dataset_label_at": None,
        "latest_harvested_label_at": None,
        "dataset_current": None,
        "pull_requests_discovered": None,
        "pull_requests_harvested": None,
        "pull_requests_failed": None,
        "harvest_coverage": None,
        "covers_report_window": None,
    }
    cost_effect = current["cost_effect"]
    lines.extend(
        [
            "",
            "## Cost and effect",
            "",
            *_markdown_table(
                ["Measurement", "Value"],
                [
                    ["Total model tokens", _format_value(cost_effect["total_tokens"])],
                    ["Input tokens", _format_value(cost_effect["input_tokens"])],
                    ["Output tokens", _format_value(cost_effect["output_tokens"])],
                    [
                        "Cached input tokens",
                        _format_value(cost_effect["cached_input_tokens"]),
                    ],
                    ["Model calls", _format_value(cost_effect["model_calls"])],
                    [
                        "Usage coverage",
                        _format_value(cost_effect["usage_coverage"], "percent"),
                    ],
                    [
                        "Labeled accepted Findings",
                        _format_value(cost_effect["labeled_accepted_findings"]),
                    ],
                    [
                        "Tokens/labeled accepted Finding",
                        _format_value(
                            cost_effect["tokens_per_labeled_accepted_finding"]
                        ),
                    ],
                    [
                        "Finding label coverage",
                        _format_value(cost_effect["label_coverage"], "percent"),
                    ],
                ],
            ),
            "",
            "The unit-cost denominator includes only human-labeled important/minor "
            "Findings. Interpret it together with label coverage; incomplete labels can "
            "overstate cost per accepted Finding.",
        ]
    )
    lines.extend(["", "## Human labels", ""])
    if label_dataset["status"] == "ready":
        lines.append(
            f"Dataset records={label_dataset['records']}; "
            f"review-week examples={review_week['examples']}, "
            f"labeled findings={review_week['labeled_findings']}, "
            f"coverage={_format_value(review_week['label_coverage'], 'percent')}."
        )
    else:
        lines.append(
            f"Label dataset status=`{label_dataset['status']}`; coverage and "
            "finding-quality rates are unavailable. Pass every normalized input with "
            "`--dataset`."
        )
    if label_sync["status"] == "missing":
        lines.append(
            "GitHub label sync manifest is missing; zero labels may mean harvest has not run."
        )
    else:
        lines.append(
            "GitHub label sync "
            f"status=`{label_sync['status']}`, "
            f"PRs={_format_value(label_sync['pull_requests_harvested'])}/"
            f"{_format_value(label_sync['pull_requests_discovered'])}, "
            f"coverage={_format_value(label_sync['harvest_coverage'], 'percent')}, "
            f"covers report window={label_sync['covers_report_window']}, "
            f"dataset current={label_sync['dataset_current']}, "
            f"latest harvested label="
            f"{_format_value(label_sync['latest_harvested_label_at'])}, "
            f"latest dataset label="
            f"{_format_value(label_sync['latest_dataset_label_at'])}."
        )
    lines.extend(
        [
            "",
            *_markdown_table(
                ["Finding quality", "Rate"],
                [
                    [
                        "Accepted (important + minor)",
                        _format_value(review_week["accepted_rate"], "percent"),
                    ],
                    ["Wrong", _format_value(review_week["wrong_rate"], "percent")],
                    ["Repeat", _format_value(review_week["repeat_rate"], "percent")],
                    [
                        "Debatable",
                        _format_value(review_week["debatable_rate"], "percent"),
                    ],
                    ["Recall", _format_value(review_week["recall_rate"], "percent")],
                ],
            ),
            "",
            f"Missed findings reported this week: "
            f"{_format_value(labeled_week['missed_findings_reported'])}. "
            "Recall remains unavailable "
            "until each reviewed change has exhaustive human ground truth.",
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
        else [
            "No labels matched this report window."
            if label_dataset["status"] == "ready"
            else "Label distribution unavailable."
        ]
    )
    wrong_tags = labeled_week["wrong_tags"]
    wrong_tag_text = (
        ", ".join(f"`{tag}`×{count}" for tag, count in wrong_tags.items())
        if wrong_tags
        else "none"
        if label_dataset["status"] == "ready"
        else "unavailable"
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
            f"- GitHub label sync status: {label_sync['status']}",
            f"- GitHub PR harvest failures: "
            f"{_format_value(label_sync['pull_requests_failed'])}",
            f"- Labels added without a matched session: "
            f"{_format_value(labeled_week['without_session'])}",
            "",
            "Session metrics are grouped by `session_start` in the report timezone. "
            "Review-week quality uses the normalized dataset's `engine.session_id`; "
            "label throughput uses the human label timestamp.",
        ]
    )
    return "\n".join(lines) + "\n"

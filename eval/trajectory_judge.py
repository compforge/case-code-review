#!/usr/bin/env python3
"""Trajectory judge — classify WHY a review chain was slow or weak, per unit.

Consumes ATIF trajectories (`ccr export --format atif`, one JSON per line),
projects them into `trajectory_harness.Trajectory`, and evaluates Review 1
Units separately from Review 2 Lanes against a fixed failure taxonomy:

  missing_tool          需要的能力没有对应工具，agent 在用别的工具硬凑
  bad_tool_description  工具选错 / 参数拼错 / 失败后换参重试——描述或参数 schema 没写清
  bad_prompt            指令不清导致游走：重复读已提供内容、超轮数预算、输出格式反复
  missing_context       该有的上下文既不在 prompt 里也拿不到（跨仓、需求背景等）
  model_limitation      模型自身瓶颈（长思考卡顿、幻觉 API）
  ok                    链路高效，无明显问题

Two passes:
  1. objective  — 纯本地统计（重复读、搜索 scope、工具失败、轮数），零成本、确定性；
  2. judge      — 每条链喂给 LLM（复用 ~/.casecodereview/config.json 的 provider），
                  按分类法给出 categories + evidence + suggestion 的 JSON 结论。

The per-chain labels are the raw material for prompt/tool-desc evolution
(GEPA-style): objective signals double as Actionable Side Information.

Usage:
  ccr export | uv run --project eval/reviewbench python eval/trajectory_judge.py
  uv run --project eval/reviewbench python eval/trajectory_judge.py traj.jsonl [--no-llm]
  uv run --project eval/reviewbench python eval/trajectory_judge.py session.jsonl
  ... [--labels out.jsonl] [--model <name>] [--max-chains N]

Dependencies are managed by eval/reviewbench/pyproject.toml.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import urllib.request
from collections import Counter
from pathlib import Path

from ccr_trajectory import (
    ATIFTrajectoryLoader,
    AssessmentCompletionEvaluator,
    DurationEfficiencyEvaluator,
    FileReadCoverageEvaluator,
    FileReadFragmentationEvaluator,
    REVIEW1,
    REVIEW2,
    ReviewCompletionEvaluator,
    PromptFileCoverageEvaluator,
    RoundEfficiencyEvaluator,
    SearchScopeEvaluator,
    ToolFailureEvaluator,
    UNKNOWN_STAGE,
    code_search_stats,
    empty_tool_argument_stats,
    file_read_fragmentation,
    file_read_stats,
    hypothesis_yield,
    initial_context_stats,
    prompt_file_read_overlap,
    repeated_file_reads,
    review_stage,
    tool_frequencies,
)
from trajectory_harness import RepeatedToolCallEvaluator, Trajectory, evaluate

TAXONOMY = ["missing_tool", "bad_tool_description", "bad_prompt",
            "missing_context", "model_limitation", "ok"]

# ── input ────────────────────────────────────────────────────────────────────

def load_trajectories(path: str | None) -> list[Trajectory]:
    """Read ATIF trajectories: from stdin, an ATIF json/jsonl file, or a raw
    session jsonl (detected by its session_start first line → `ccr export` it)."""
    if path is None:
        data = sys.stdin.read()
    else:
        data = Path(path).read_text(encoding="utf-8")
        first = data.splitlines()[0] if data.strip() else ""
        if '"session_start"' in first:  # raw session transcript, not ATIF
            data = subprocess.run(["ccr", "export", "--format", "atif", path],
                                  capture_output=True, text=True, check=True).stdout
    return ATIFTrajectoryLoader().loads(data, source=path or "stdin")


# ── objective pass (deterministic, free) ─────────────────────────────────────

_COMMON_EVALUATORS = (
    RepeatedToolCallEvaluator(),
    ToolFailureEvaluator(),
    SearchScopeEvaluator(),
    FileReadCoverageEvaluator(),
    PromptFileCoverageEvaluator(),
    FileReadFragmentationEvaluator(),
    RoundEfficiencyEvaluator(),
    DurationEfficiencyEvaluator(),
    ReviewCompletionEvaluator(),
)

_STAGE_EVALUATORS = {
    REVIEW1: _COMMON_EVALUATORS,
    REVIEW2: (*_COMMON_EVALUATORS, AssessmentCompletionEvaluator()),
    UNKNOWN_STAGE: _COMMON_EVALUATORS,
}


def objective_signals(trajectory: Trajectory) -> dict:
    """Local, deterministic waste/failure signals for one unit chain. These are
    both a cheap standalone report and the ASI handed to the LLM judge."""
    stage = review_stage(trajectory)
    report = evaluate(trajectory, _STAGE_EVALUATORS[stage])
    evaluations = {result.name: result for result in report.evaluations}
    tool_fails = [
        {"tool": step.name, "error": _tool_result(step)[:120]}
        for step in trajectory.steps
        if step.operation == "execute_tool" and step.status == "error"
    ]
    return {
        "stage": stage,
        "score": report.score,
        "evaluations": [result.to_dict() for result in report.evaluations],
        "rounds": sum(step.operation == "inference" for step in trajectory.steps),
        "duration_sec": round(sum(step.duration_ms for step in trajectory.steps) / 1000),
        "tool_freq": tool_frequencies(trajectory),
        "empty_args": empty_tool_argument_stats(trajectory),
        "code_searches": code_search_stats(trajectory),
        "file_reads": file_read_stats(trajectory),
        "prompt_overlap": prompt_file_read_overlap(trajectory),
        "initial_context": initial_context_stats(trajectory),
        "read_fragmentation": file_read_fragmentation(trajectory),
        "repeated_reads": repeated_file_reads(trajectory),
        "hypothesis_yield": hypothesis_yield(trajectory) if stage == REVIEW1 else 0,
        "tool_failures": tool_fails,
    }


def main_deductions(signals: list[dict], limit: int = 3) -> list[dict]:
    """Rank the stage's recurring score losses without hiding raw Evaluations."""

    grouped: dict[str, list[float]] = {}
    for signal in signals:
        for result in signal["evaluations"]:
            score = result.get("score")
            if result.get("label") != "fail" or score is None:
                continue
            grouped.setdefault(result["name"], []).append(float(score))
    ranked = [
        {
            "name": name,
            "count": len(scores),
            "average_score": round(sum(scores) / len(scores), 3),
            "lost_score": round(sum(1 - score for score in scores), 3),
        }
        for name, scores in grouped.items()
    ]
    ranked.sort(key=lambda item: (-item["lost_score"], -item["count"], item["name"]))
    return ranked[:limit]


# ── judge pass (LLM over the chain, taxonomy-constrained) ────────────────────

JUDGE_SYSTEM = """You are auditing ONE code-review agent trajectory (an LLM reviewing one \
Unit in Review 1 or one Lane in Review 2 through tool calls). Diagnose why the chain was slow or weak, using ONLY \
this taxonomy (multiple allowed; use "ok" alone when the chain is efficient):

- missing_tool: the agent needed a capability no tool provides and worked around it \
(e.g. simulating cross-file navigation with repeated text search).
- bad_tool_description: wrong tool picked, malformed arguments, retry-after-error on \
the same tool — the tool's description/schema failed the agent.
- bad_prompt: wandering caused by unclear instructions — re-fetching content already \
in the prompt, exceeding a sane round budget, repeated output-format corrections.
- missing_context: information the review needed but neither had nor could fetch \
(requirement background, cross-repo callers).
- model_limitation: the model itself stalls or hallucinates despite adequate \
tools/prompt/context.
- ok: efficient chain, no significant issue.

Base every claim on concrete steps. Answer ONLY with JSON:
{"categories":[{"type":"<taxonomy>","evidence":"<step refs + what happened>",\
"suggestion":"<concrete fix: the tool to add / the desc line to change / the prompt \
rule to add>","confidence":0.0}],"summary":"<one sentence>"}"""

_TRUNC = 500  # per message/result — the judge needs shape, not full payloads


def chain_digest(trajectory: Trajectory, signals: dict) -> str:
    """Compact one chain for the judge: every step, messages/results truncated,
    objective signals appended as ASI."""
    lines = [
        f"stage: {review_stage(trajectory)} scope: {trajectory.trajectory_id} "
        f"metadata={json.dumps(trajectory.metadata, ensure_ascii=False)}"
    ]
    for step in trajectory.steps:
        head = f"#{step.step_id} [{step.operation}]"
        if step.operation == "context":
            lines.append(f"{head} {_message_text(step.output_messages)[:_TRUNC]}")
            continue
        if step.operation == "inference":
            lines.append(
                f"{head} ({int(step.duration_ms / 1000)}s) "
                f"{_message_text(step.output_messages)[:_TRUNC]}"
            )
            continue
        if step.operation == "execute_tool":
            ok = " FAILED" if step.status == "error" else ""
            lines.append(
                f"{head} {step.name}{ok}({_tool_arguments(step)[:200]}) "
                f"-> {_tool_result(step)[:_TRUNC]}"
            )
    lines.append(f"objective signals: {json.dumps(signals, ensure_ascii=False)}")
    return "\n".join(lines)


def _parts(messages: tuple[dict, ...]):
    for message in messages:
        yield from message.get("parts") or []


def _message_text(messages: tuple[dict, ...]) -> str:
    return "\n".join(
        str(part.get("content") or "")
        for part in _parts(messages)
        if part.get("type") == "text"
    )


def _tool_arguments(step) -> str:
    for part in _parts(step.input_messages):
        if part.get("type") == "tool_call":
            return json.dumps(part.get("arguments"), ensure_ascii=False)
    return "{}"


def _tool_result(step) -> str:
    for part in _parts(step.output_messages):
        if part.get("type") == "tool_call_response":
            value = part.get("response")
            return value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    return ""


def load_llm(model_override: str | None):
    """(url, api_key, model) from ccr's own config — the judge rides the same
    provider the review used, no separate credential story."""
    cfg = json.loads((Path.home() / ".casecodereview" / "config.json").read_text())
    providers = cfg.get("custom_providers") or {}
    routing = (cfg.get("routing") or {}).get("models") or []
    if not providers or not routing:
        raise SystemExit("no custom_providers/routing in ~/.casecodereview/config.json")
    route = routing[0]
    if model_override:
        route = next((m for m in routing if model_override in (m.get("alias"), m.get("model"))), route)
    prov = providers[route["provider"]]
    return prov["url"], prov["api_key"], route["model"]


def judge_chain(url: str, key: str, model: str, digest: str) -> dict:
    body = {
        "model": model,
        "messages": [{"role": "system", "content": JUDGE_SYSTEM},
                     {"role": "user", "content": digest}],
        "temperature": 0,
        # Ark models default thinking to auto; a taxonomy classification doesn't
        # need it and it multiplies latency (see mem: review p50 12s vs 77s).
        "thinking": {"type": "disabled"},
    }
    req = urllib.request.Request(url, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json",
                                          "Authorization": f"Bearer {key}"})
    with urllib.request.urlopen(req, timeout=180) as resp:
        out = json.loads(resp.read())
    text = out["choices"][0]["message"]["content"]
    m = re.search(r"\{.*\}", text, re.S)  # tolerate prose around the JSON
    verdict = json.loads(m.group(0)) if m else {"categories": [], "summary": text[:200]}
    verdict["categories"] = [c for c in verdict.get("categories") or []
                             if c.get("type") in TAXONOMY]
    return verdict


# ── report ───────────────────────────────────────────────────────────────────

def main() -> int:
    ap = argparse.ArgumentParser(description="classify review-chain failures over ATIF trajectories")
    ap.add_argument("path", nargs="?", help="ATIF json/jsonl (or raw session jsonl); stdin if omitted")
    ap.add_argument("--no-llm", action="store_true", help="objective signals only")
    ap.add_argument("--labels", help="also append per-chain JSONL labels here (fuel for prompt evolution)")
    ap.add_argument("--model", help="routing alias/model for the judge (default: first routing entry)")
    ap.add_argument("--max-chains", type=int, default=0, help="judge at most N chains (0 = all)")
    ns = ap.parse_args()

    trajectories = load_trajectories(ns.path)
    llm = None if ns.no_llm else load_llm(ns.model)
    labels_f = open(ns.labels, "a", encoding="utf-8") if ns.labels else None

    judged = 0
    sessions = {}
    for trajectory in trajectories:
        session_id = str(trajectory.metadata.get("session_id") or "?")
        sessions.setdefault(session_id, []).append(trajectory)

    for session_id, session_trajectories in sessions.items():
        first = session_trajectories[0]
        print(f"# session {session_id[:8]} "
              f"repo={first.metadata.get('repo', '?')} "
              f"branch={first.metadata.get('branch', '?')}")
        for stage, title in (
            (REVIEW1, "Review 1 · Unit"),
            (REVIEW2, "Review 2 · Lane"),
            (UNKNOWN_STAGE, "Other"),
        ):
            staged = [item for item in session_trajectories if review_stage(item) == stage]
            if not staged:
                continue
            print(f"\n## {title} ({len(staged)})")
            stage_signals = []
            for trajectory in staged:
                sig = objective_signals(trajectory)
                stage_signals.append(sig)
                print(f"\n### {trajectory.trajectory_id}")
                searches = sig["code_searches"]
                print(f"   score={sig['score']} rounds={sig['rounds']} "
                      f"duration={sig['duration_sec']}s tools={sig['tool_freq']} "
                      f"search_outcomes=hit:{searches['hits']}/valid_empty:{searches['valid_empty']}"
                      f"/scope_miss:{searches['scope_miss']}/scope_unknown:{searches['scope_unknown']}"
                      f"/failure:{searches['tool_failure']}/repeated_empty:{searches['repeated_empty']}")
                if sig["empty_args"]["count"]:
                    empty_args = sig["empty_args"]
                    print(
                        f"   empty_args={empty_args['count']} "
                        f"by_tool={empty_args['by_tool']} "
                        f"by_model={empty_args['by_model']}"
                    )
                if sig["file_reads"]["calls"]:
                    reads = sig["file_reads"]
                    print(f"   read_files calls={reads['calls']} requests={reads['requests']} "
                          f"rounds={reads['rounds']} avg_batch={reads['average_batch']} "
                          f"max_batch={reads['max_batch']} calls/round={reads['calls_per_round']}")
                    overlap = sig["prompt_overlap"]
                    fragmented = sig["read_fragmentation"]
                    print(
                        f"   prompt_overlap full={overlap['fully_covered']} "
                        f"partial={overlap['partially_covered']} new={overlap['new_context']} "
                        f"runtime_repeat={overlap['runtime_covered']} "
                        f"blocked={overlap['blocked']} failed={overlap['failed']} "
                        f"lines={overlap['covered_lines']}/{overlap['total_lines']}"
                    )
                    print(
                        f"   read_fragmentation minimal={fragmented['minimal_ranges']} "
                        f"mergeable={fragmented['mergeable_reads']}"
                    )
                if sig["code_searches"]["calls"]:
                    searches = sig["code_searches"]
                    print(
                        f"   search_code calls={searches['calls']} "
                        f"requests={searches['requests']} rounds={searches['rounds']} "
                        f"avg_batch={searches['average_batch']} "
                        f"max_batch={searches['max_batch']} "
                        f"calls/round={searches['calls_per_round']}"
                    )
                for result in sig["evaluations"]:
                    if result["label"] == "fail":
                        print(f"   ⚠ {result['name']}: {result['explanation']}")
                if sig["repeated_reads"]:
                    print(f"   ⚠ repeated reads: {sig['repeated_reads']}")
                for failure in sig["tool_failures"]:
                    print(f"   ⚠ tool failure: {failure['tool']}: {failure['error']}")
                verdict = None
                if llm and (not ns.max_chains or judged < ns.max_chains):
                    try:
                        verdict = judge_chain(*llm, chain_digest(trajectory, sig))
                        judged += 1
                    except Exception as e:  # judge 失败不挡客观报告
                        print(f"   (judge failed: {e})")
                if verdict:
                    for category in verdict.get("categories", []):
                        print(f"   [{category.get('type')}] ({category.get('confidence')}) "
                              f"{category.get('evidence', '')[:160]}")
                        if category.get("suggestion"):
                            print(f"       fix: {category['suggestion'][:200]}")
                    print(f"   => {verdict.get('summary', '')}")
                if labels_f:
                    labels_f.write(json.dumps({
                        "session_id": session_id,
                        "stage": stage,
                        "trajectory_id": trajectory.trajectory_id,
                        "extra": trajectory.metadata,
                        "signals": sig,
                        "verdict": verdict,
                    }, ensure_ascii=False) + "\n")
            scores = [item["score"] for item in stage_signals if item["score"] is not None]
            average = round(sum(scores) / len(scores), 3) if scores else None
            print(f"\n   {title} summary: score={average} "
                  f"rounds={sum(item['rounds'] for item in stage_signals)} "
                  f"duration={sum(item['duration_sec'] for item in stage_signals)}s")
            empty_by_tool: Counter[str] = Counter()
            empty_by_model: Counter[str] = Counter()
            for item in stage_signals:
                empty_by_tool.update(item["empty_args"]["by_tool"])
                empty_by_model.update(item["empty_args"]["by_model"])
            if empty_by_tool:
                print(
                    f"   empty_args summary: total={sum(empty_by_tool.values())} "
                    f"by_tool={dict(empty_by_tool)} "
                    f"by_model={dict(empty_by_model)}"
                )
            deductions = main_deductions(stage_signals)
            if deductions:
                detail = ", ".join(
                    f"{item['name']}({item['count']} chain(s), avg={item['average_score']})"
                    for item in deductions
                )
                print(f"   main deductions: {detail}")
        print()
    if trajectories:
        print()
    if labels_f:
        labels_f.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""replay — 固定回归集重放：corpus × arms 矩阵跑 ccr review 并出对比报告。

「同工作负载重放」的固化：corpus 是一组冻结的评审范围（merge 双亲），
arm 是一组 feature gate 配置或显式指定的二进制。趋势对比只能在固定 corpus 上做（生产 session
的 diff 各不相同，不可比）；单项归因只能在同 corpus 的 arm 间做（gate 消融）。
典型用途：为 feature gate 变更提供固定工作负载下的对照证据。

  python3 -m eval.benchmark.replay eval/data/corpus/ccr-self.json \\
      --arm base --arm no-plan:plan=off [--only "#93"] [--runs 2]

每个 run 打唯一 CCR_EVAL_TAG，事后从 session 目录按 tag 捞回 transcript，
按 target 完成证据区分 finding 持续、未再报告与未完成复查，并聚合 debrief 成本。
默认按 run 轮换 arm 首位，避免先跑完一个 arm 再跑另一个造成时间漂移；
模型与并发度可显式固定，并与 Session generation provenance 一起落盘。
产物写 --out（默认 ~/.casecodereview/replay/<ts>/），不入库。

stdlib only — no pip installs.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

from eval.benchmark.session_compare import compare, markdown, read_session

from eval.session_recording import find_session
from eval.eval_snapshot import file_sha256, review_sha256

RUN_TIMEOUT = 30 * 60


def parse_arm(spec: str) -> tuple[str, list[str]]:
    """"name[:feat=v,feat=v]" → (name, ["--feature","feat=v",...])."""
    name, _, feats = spec.partition(":")
    args = []
    for kv in filter(None, (s.strip() for s in feats.split(","))):
        args += ["--feature", kv]
    return name, args


def build_schedule(
    entries: list[dict],
    arms: list[tuple[str, list[str]]],
    runs: int,
    schedule: str,
) -> list[tuple[dict, str, list[str], int]]:
    """Build a deterministic execution order for the corpus × arms × runs matrix."""

    ordered: list[tuple[dict, str, list[str], int]] = []
    for entry in entries:
        if schedule == "arm-major":
            for arm_name, feat_args in arms:
                for run_index in range(runs):
                    ordered.append((entry, arm_name, feat_args, run_index))
            continue
        for run_index in range(runs):
            offset = run_index % len(arms)
            rotated = arms[offset:] + arms[:offset]
            for arm_name, feat_args in rotated:
                ordered.append((entry, arm_name, feat_args, run_index))
    return ordered


def run_ccr(
    ccr: str,
    repo: str,
    entry: dict,
    feat_args: list[str],
    tag: str,
    model: str | None = None,
    concurrency: int | None = None,
    timeout_minutes: int | None = None,
    max_tokens_budget: int | None = None,
    max_completion_tokens: int | None = None,
) -> tuple[bool, str]:
    cmd = [ccr, "review", "--from", entry["from"], "--to", entry["to"], *feat_args]
    if model:
        cmd += ["--model", model]
    if concurrency:
        cmd += ["--concurrency", str(concurrency)]
    for flag, value in (("timeout", timeout_minutes), ("max-tokens-budget", max_tokens_budget),
                        ("max-completion-tokens", max_completion_tokens)):
        if value is not None:
            cmd += [f"--{flag}", str(value)]
    env = dict(os.environ, CCR_EVAL_TAG=tag)
    try:
        r = subprocess.run(cmd, cwd=repo, env=env, capture_output=True, timeout=RUN_TIMEOUT)
    except subprocess.TimeoutExpired:
        return False, "timeout"
    if r.returncode != 0:
        return False, r.stderr.decode("utf-8", errors="replace")[-400:]
    return True, ""


def collect(path: Path) -> dict:
    """Aggregate persisted evidence without discarding partial sessions."""
    return read_session(path).summary()


def compare_runs(results: dict, entries: list[dict], arms: list[tuple[str, list[str]]], runs: int) -> list[dict]:
    """Compare corresponding repeats, retaining failed runs with a transcript."""
    reports = []
    for entry in entries:
        for index in range(runs):
            base = results.get((entry["name"], arms[0][0], index), {})
            for arm, _ in arms[1:]:
                candidate = results.get((entry["name"], arm, index), {})
                if not base.get("session") or not candidate.get("session"):
                    reports.append({"entry": entry["name"], "arm": arm, "run": index,
                                    "error": "Comparison unavailable: session missing"})
                    continue
                report = compare(read_session(Path(base["session"])), read_session(Path(candidate["session"])))
                for label, result in ((arms[0][0], base), (arm, candidate)):
                    if result.get("error"):
                        report["warnings"].append(f"{label} command failed: {result['error']}")
                reports.append({"entry": entry["name"], "arm": arm, "run": index, **report})
    return reports


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("corpus", help="corpus json（entries: [{name, from, to}]）")
    ap.add_argument("--arm", action="append", required=True,
                    help='arm 配置 "name[:feat=v,feat=v]"，可重复；第一个 arm 是对比基线')
    ap.add_argument("--repo", default=".", help="被重放的仓库（默认 cwd）")
    ap.add_argument("--ccr", default="ccr", help="ccr 可执行文件（默认 PATH 里的 ccr）")
    ap.add_argument("--arm-engine", action="append", default=[], metavar="NAME=PATH",
                    help="per-arm executable override; binary content hashes are saved in plan.json")
    ap.add_argument("--plan-only", action="store_true", help="freeze schedule and budgets without model calls")
    ap.add_argument("--timeout", type=int, help="same per-Unit timeout in minutes for every arm")
    ap.add_argument("--max-tokens-budget", type=int, help="same soft token cap per review")
    ap.add_argument("--max-completion-tokens", type=int, help="same output cap per model response")
    ap.add_argument("--max-total-tokens", type=int, help="stop admitting runs after this recorded input+output budget")
    ap.add_argument("--stop-after-errors", type=int, default=0, help="stop admitting runs after consecutive command failures (0 disables)")
    ap.add_argument("--model", help="固定 review 模型；省略时使用 CCR 配置")
    ap.add_argument("--concurrency", type=int,
                    help="固定 CCR Unit 并发度；省略时使用 CCR 默认值")
    ap.add_argument("--only", help="只跑名字含此子串的 corpus 条目")
    ap.add_argument("--runs", type=int, default=1, help="每格重复次数（观测方差）")
    ap.add_argument("--schedule", choices=("interleaved", "arm-major"),
                    default="interleaved",
                    help="执行顺序；interleaved 按 run 轮换 arm 首位（默认）")
    ap.add_argument("--out", help="产物目录（默认 ~/.casecodereview/replay/<ts>）")
    args = ap.parse_args()

    if args.runs < 1:
        ap.error("--runs must be at least 1")
    if args.concurrency is not None and args.concurrency < 1:
        ap.error("--concurrency must be at least 1")

    corpus = json.loads(Path(args.corpus).read_text(encoding="utf-8"))
    entries = [e for e in corpus["entries"] if not args.only or args.only in e["name"]]
    arms = [parse_arm(s) for s in args.arm]
    out_dir = Path(args.out) if args.out else Path.home() / ".casecodereview" / "replay" / str(int(time.time()))
    out_dir.mkdir(parents=True, exist_ok=True)
    if (out_dir / "runs.jsonl").exists() and not args.plan_only:
        ap.error("output already contains runs.jsonl; choose a new --out to preserve recorded runs")
    names = [name for name, _ in arms]
    if not entries or len(set(names)) != len(names) or any(not name for name in names):
        ap.error("select nonempty cases and distinct nonempty arm names")
    if len({e["name"] for e in entries}) != len(entries):
        ap.error("corpus case names must be unique")
    engines = dict.fromkeys(names, args.ccr)
    for override in args.arm_engine:
        name, separator, engine = override.partition("=")
        if not separator or name not in engines or not engine:
            ap.error("--arm-engine requires a selected NAME=PATH")
        executable = shutil.which(engine)
        if not executable:
            ap.error(f"engine unavailable for {name}: {engine}")
        engines[name] = str(Path(executable).resolve())
    for field in ("timeout", "max_tokens_budget", "max_completion_tokens", "max_total_tokens"):
        value = getattr(args, field)
        if value is not None and value < 1:
            ap.error(f"--{field.replace('_', '-')} must be positive")
    if args.stop_after_errors < 0:
        ap.error("--stop-after-errors must be nonnegative")
    schedule = build_schedule(entries, arms, args.runs, args.schedule)
    hashes = {name: file_sha256(Path(path)) for name, engine in engines.items()
              if (path := shutil.which(engine))}
    plan = {"corpus_sha256": file_sha256(Path(args.corpus)), "engines": engines,
            "engine_sha256": hashes, "model_override": args.model, "concurrency": args.concurrency,
            "timeout_minutes": args.timeout, "max_tokens_budget": args.max_tokens_budget,
            "max_completion_tokens": args.max_completion_tokens, "max_total_tokens": args.max_total_tokens,
            "stop_after_errors": args.stop_after_errors,
            "schedule": [{"case": entry["name"], "repo": str(Path(entry.get("repo") or args.repo).resolve()),
                          "from": entry["from"], "to": entry["to"], "arm": arm, "repeat": repeat,
                          "feature_args": features} for entry, arm, features, repeat in schedule]}
    config_path = Path.home() / ".casecodereview" / "config.json"
    config_hash = file_sha256(config_path) if config_path.is_file() else None
    plan["config_sha256"] = config_hash
    plan_path = out_dir / "plan.json"
    if plan_path.exists():
        if json.loads(plan_path.read_text()) != plan:
            ap.error("existing plan differs from requested inputs/configuration; choose a new --out")
    else:
        plan_path.write_text(json.dumps(plan, ensure_ascii=False, indent=2) + "\n")
    if args.plan_only:
        print(f"planned_reviews={len(schedule)} → {out_dir / 'plan.json'}")
        return 0

    results: dict[tuple[str, str, int], dict] = {}
    with (out_dir / "runs.jsonl").open("w", encoding="utf-8") as runlog:
        total_tokens, consecutive_errors = 0, 0
        cost_unknown = False
        for sequence, (e, arm_name, feat_args, i) in enumerate(schedule):
            repo = str(Path(e.get("repo") or args.repo).resolve())
            engine = engines[arm_name]
            budget_exhausted = ((args.max_total_tokens is not None and (total_tokens >= args.max_total_tokens or cost_unknown))
                                or (args.stop_after_errors and consecutive_errors >= args.stop_after_errors))
            if budget_exhausted:
                error = ("not executed: cost unknown; remaining experiment budget cannot be established"
                         if cost_unknown and args.max_total_tokens is not None
                         else "not executed: experiment token/error budget exhausted")
                results[(e["name"], arm_name, i)] = {"error": error}
                runlog.write(json.dumps({"entry": e["name"], "arm": arm_name, "run": i,
                                         "sequence": sequence, "error": error}) + "\n")
                runlog.flush()
                continue
            if config_hash and file_sha256(config_path) != config_hash:
                raise ValueError("CCR configuration changed after planning")
            if arm_name in hashes and file_sha256(Path(shutil.which(engine))) != hashes[arm_name]:
                raise ValueError(f"engine changed after planning: {arm_name}")
            tag = f"replay:{e['name']}:{arm_name}:{i}:{int(time.time())}"
            print(f"▶ {e['name']} × {arm_name} (run {i})", flush=True)
            ok, err = run_ccr(
                engine,
                repo,
                e,
                feat_args,
                tag,
                model=args.model,
                concurrency=args.concurrency,
                timeout_minutes=args.timeout,
                max_tokens_budget=args.max_tokens_budget,
                max_completion_tokens=args.max_completion_tokens,
            )
            consecutive_errors = 0 if ok else consecutive_errors + 1
            sess = find_session(repo, tag)
            if sess is None:
                cost_unknown = True
                error = err or "session not found"
                print(f"  ✗ {error}", flush=True)
                results[(e["name"], arm_name, i)] = {"error": error}
                runlog.write(json.dumps({
                    "entry": e["name"],
                    "arm": arm_name,
                    "run": i,
                    "sequence": sequence,
                    "schedule": args.schedule,
                    "model_override": args.model,
                    "concurrency": args.concurrency,
                    "feature_args": feat_args,
                    "error": error,
                }, ensure_ascii=False) + "\n")
                runlog.flush()
                continue
            r = collect(sess)
            r["session"] = str(sess)
            r["session_sha256"] = review_sha256(sess)
            r["engine_sha256"] = hashes.get(arm_name)
            total_tokens += r.get("prompt_tokens", 0) + r.get("completion_tokens", 0)
            cost_unknown |= any(r.get(key, 0) for key in ("unknown_usage", "pending_calls", "estimated_usage"))
            if not ok:
                r["error"] = err or "review command failed"
            results[(e["name"], arm_name, i)] = r
            runlog.write(json.dumps({
                "entry": e["name"],
                "arm": arm_name,
                "run": i,
                "sequence": sequence,
                "schedule": args.schedule,
                "model_override": args.model,
                "concurrency": args.concurrency,
                "feature_args": feat_args,
                **r,
            }, ensure_ascii=False) + "\n")
            runlog.flush()
            print(f"  {'✓' if ok else '✗'} units={r['units']} findings={len(r['findings'])} "
                  f"prompt_tok={r['prompt_tokens']} dur={r['duration_s']}s", flush=True)

    # ── report ──
    base_arm = arms[0][0]
    comparisons = compare_runs(results, entries, arms, args.runs)
    (out_dir / "comparisons.json").write_text(json.dumps(comparisons, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    lines = [
        f"# replay report — corpus={args.corpus} arms={[a for a, _ in arms]} runs={args.runs}\n",
        f"- schedule: {args.schedule}",
        f"- model override: {args.model or '(configured default)'}",
        f"- concurrency: {args.concurrency or '(engine default)'}",
        f"- engine: {args.ccr}",
    ]
    for e in entries:
        lines.append(f"\n## {e['name']}\n")
        lines.append("| arm | run | units | outcomes | findings | prompt tok | rounds | dur(s) |")
        lines.append("|---|---|---|---|---|---|---|---|")
        for arm_name, _ in arms:
            for i in range(args.runs):
                r = results.get((e["name"], arm_name, i), {})
                if "error" in r and "session" not in r:
                    lines.append(f"| {arm_name} | {i} | — | ERROR: {r['error'][:60]} | | | | |")
                    continue
                oc = ", ".join(f"{k}×{v}" for k, v in sorted(r["outcomes"].items()))
                if r.get("error"):
                    oc += " (command failed; partial evidence)"
                lines.append(f"| {arm_name} | {i} | {r['units']} | {oc} | {len(r['findings'])} "
                             f"| {r['prompt_tokens']} | {r['rounds']} | {r['duration_s']} |")
        for comparison in comparisons:
            if comparison["entry"] != e["name"]:
                continue
            lines.append(f"\n### {base_arm} vs {comparison['arm']} · run {comparison['run']}\n")
            lines.append(comparison["error"] if "error" in comparison else markdown(comparison))

    report = "\n".join(lines) + "\n"
    (out_dir / "REPORT.md").write_text(report, encoding="utf-8")
    print("\n" + report)
    print(f"artifacts → {out_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

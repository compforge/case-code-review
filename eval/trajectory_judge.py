#!/usr/bin/env python3
"""Trajectory judge — classify WHY a review chain was slow or weak, per unit.

Consumes ATIF trajectories (`ccr export --format atif`, one JSON per line) and
produces, for every unit chain, a diagnosis against a fixed failure taxonomy:

  missing_tool          需要的能力没有对应工具，agent 在用别的工具硬凑
  bad_tool_description  工具选错 / 参数拼错 / 失败后换参重试——描述或参数 schema 没写清
  bad_prompt            指令不清导致游走：重复读已提供内容、超轮数预算、输出格式反复
  missing_context       该有的上下文既不在 prompt 里也拿不到（跨仓、需求背景等）
  model_limitation      模型自身瓶颈（长思考卡顿、幻觉 API）
  ok                    链路高效，无明显问题

Two passes:
  1. objective  — 纯本地统计（重复读、空搜索、工具失败、轮数），零成本、确定性；
  2. judge      — 每条链喂给 LLM（复用 ~/.casecodereview/config.json 的 provider），
                  按分类法给出 categories + evidence + suggestion 的 JSON 结论。

The per-chain labels are the raw material for prompt/tool-desc evolution
(GEPA-style): objective signals double as Actionable Side Information.

Usage:
  ccr export | python3 scripts/trajectory_judge.py                 # stdin
  python3 scripts/trajectory_judge.py traj.jsonl [--no-llm]
  python3 scripts/trajectory_judge.py session.jsonl                # auto-runs ccr export
  ... [--labels out.jsonl] [--model <name>] [--max-chains N]

stdlib only — no pip installs.
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

TAXONOMY = ["missing_tool", "bad_tool_description", "bad_prompt",
            "missing_context", "model_limitation", "ok"]

# ── input ────────────────────────────────────────────────────────────────────

def load_trajectories(path: str | None) -> list[dict]:
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
    out = []
    for line in data.splitlines():
        line = line.strip()
        if line:
            out.append(json.loads(line))
    return out


# ── objective pass (deterministic, free) ─────────────────────────────────────

def objective_signals(chain: dict) -> dict:
    """Local, deterministic waste/failure signals for one unit chain. These are
    both a cheap standalone report and the ASI handed to the LLM judge."""
    steps = chain.get("steps") or []
    llm_steps = [s for s in steps if s.get("source") == "agent"]
    reads = Counter()          # file_path → times fetched via file_read
    empty_searches = 0
    tool_fails = []
    tool_freq = Counter()
    duration_ms = 0.0
    for s in steps:
        m = s.get("metrics") or {}
        duration_ms += float((m.get("extra") or {}).get("duration_ms") or 0)
        for tc in s.get("tool_calls") or []:
            name = tc.get("function_name", "?")
            tool_freq[name] += 1
            if name == "file_read":
                reads[(tc.get("arguments") or {}).get("file_path", "?")] += 1
        for r in (s.get("observation") or {}).get("results") or []:
            ex = r.get("extra") or {}
            if ex.get("ok") is False:
                tool_fails.append({"tool": ex.get("tool_name"),
                                   "error": (r.get("content") or "")[:120]})
            elif ex.get("tool_name") == "code_search" and len(r.get("content") or "") < 32:
                empty_searches += 1
    return {
        "rounds": len(llm_steps),
        "duration_sec": round(duration_ms / 1000),
        "tool_freq": dict(tool_freq),
        "repeated_reads": {p: n for p, n in reads.items() if n > 1},
        "empty_searches": empty_searches,
        "tool_failures": tool_fails,
    }


# ── judge pass (LLM over the chain, taxonomy-constrained) ────────────────────

JUDGE_SYSTEM = """You are auditing ONE code-review agent trajectory (an LLM reviewing one \
code unit through tool calls). Diagnose why the chain was slow or weak, using ONLY \
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


def chain_digest(chain: dict, signals: dict) -> str:
    """Compact one chain for the judge: every step, messages/results truncated,
    objective signals appended as ASI."""
    lines = [f"unit: {chain.get('trajectory_id')} extra={json.dumps(chain.get('extra') or {}, ensure_ascii=False)}"]
    for s in chain.get("steps") or []:
        src = s.get("source")
        msg = s.get("message")
        msg = msg if isinstance(msg, str) else json.dumps(msg, ensure_ascii=False)
        head = f"#{s.get('step_id')} [{src}]"
        if src in ("system", "user"):
            lines.append(f"{head} {msg[:_TRUNC]}")
            continue
        dur = ((s.get("metrics") or {}).get("extra") or {}).get("duration_ms", 0)
        lines.append(f"{head} ({int(float(dur) / 1000)}s) {msg[:_TRUNC]}")
        for tc in s.get("tool_calls") or []:
            lines.append(f"    -> {tc.get('function_name')}({json.dumps(tc.get('arguments'), ensure_ascii=False)[:200]})")
        for r in (s.get("observation") or {}).get("results") or []:
            ex = r.get("extra") or {}
            ok = "" if ex.get("ok", True) else " FAILED"
            lines.append(f"    <- {ex.get('tool_name')}{ok}: {(r.get('content') or '')[:_TRUNC]}")
    lines.append(f"objective signals: {json.dumps(signals, ensure_ascii=False)}")
    return "\n".join(lines)


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
    for traj in trajectories:
        print(f"# session {traj.get('session_id', '?')[:8]} "
              f"repo={(traj.get('extra') or {}).get('repo', '?')} "
              f"branch={(traj.get('extra') or {}).get('branch', '?')}")
        for chain in traj.get("subagent_trajectories") or []:
            sig = objective_signals(chain)
            print(f"\n## {chain.get('trajectory_id')}")
            print(f"   rounds={sig['rounds']} duration={sig['duration_sec']}s "
                  f"tools={sig['tool_freq']} empty_searches={sig['empty_searches']}")
            if sig["repeated_reads"]:
                print(f"   ⚠ repeated reads: {sig['repeated_reads']}")
            for tf in sig["tool_failures"]:
                print(f"   ⚠ tool failure: {tf['tool']}: {tf['error']}")
            verdict = None
            if llm and (not ns.max_chains or judged < ns.max_chains):
                try:
                    verdict = judge_chain(*llm, chain_digest(chain, sig))
                    judged += 1
                except Exception as e:  # judge 失败不挡客观报告
                    print(f"   (judge failed: {e})")
            if verdict:
                for c in verdict.get("categories", []):
                    print(f"   [{c.get('type')}] ({c.get('confidence')}) {c.get('evidence', '')[:160]}")
                    if c.get("suggestion"):
                        print(f"       fix: {c['suggestion'][:200]}")
                print(f"   => {verdict.get('summary', '')}")
            if labels_f:
                labels_f.write(json.dumps({
                    "session_id": traj.get("session_id"),
                    "trajectory_id": chain.get("trajectory_id"),
                    "extra": chain.get("extra"),
                    "signals": sig,
                    "verdict": verdict,
                }, ensure_ascii=False) + "\n")
        print()
    if labels_f:
        labels_f.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())

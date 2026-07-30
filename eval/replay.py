#!/usr/bin/env python3
"""replay.py — 固定回归集重放：corpus × arms 矩阵跑 ccr review 并出对比报告。

「同工作负载重放」的固化：corpus 是一组冻结的评审范围（merge 双亲），
arm 是一组 feature gate 配置。趋势对比只能在固定 corpus 上做（生产 session
的 diff 各不相同，不可比）；单项归因只能在同 corpus 的 arm 间做（gate 消融）。
典型用途：给 typed_briefing 这类默认关的形状变更发"翻转许可证"。

  python3 eval/replay.py eval/data/corpus/ccr-self.json \\
      --arm base --arm typed:typed_briefing=on [--only "#93"] [--runs 2]

每个 run 打唯一 CCR_EVAL_TAG，事后从 session 目录按 tag 捞回 transcript，
聚合 finding（指纹精确匹配 + path/symbol 宽松匹配两档——模型措辞会漂，
指纹 undercount，宽松档 overcount，真值在两者之间）与 debrief 成本。
产物写 --out（默认 ~/.casecodereview/replay/<ts>/），不入库。

stdlib only — no pip installs.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from collections import Counter
from pathlib import Path

RUN_TIMEOUT = 30 * 60


def encode_repo_path(p: str) -> str:
    """Port of internal/session/persist.go encodeRepoPath (posix subset)."""
    p = p.lstrip("/\\").replace("/", "-").replace("\\", "-")
    return p or "empty"


def session_dir(repo: str) -> Path:
    return Path.home() / ".casecodereview" / "sessions" / encode_repo_path(str(Path(repo).resolve()))


def parse_arm(spec: str) -> tuple[str, list[str]]:
    """"name[:feat=v,feat=v]" → (name, ["--feature","feat=v",...])."""
    name, _, feats = spec.partition(":")
    args = []
    for kv in filter(None, (s.strip() for s in feats.split(","))):
        args += ["--feature", kv]
    return name, args


def run_ccr(ccr: str, repo: str, entry: dict, feat_args: list[str], tag: str) -> tuple[bool, str]:
    cmd = [ccr, "review", "--from", entry["from"], "--to", entry["to"], *feat_args]
    env = dict(os.environ, CCR_EVAL_TAG=tag)
    try:
        r = subprocess.run(cmd, cwd=repo, env=env, capture_output=True, timeout=RUN_TIMEOUT)
    except subprocess.TimeoutExpired:
        return False, "timeout"
    if r.returncode != 0:
        return False, r.stderr.decode("utf-8", errors="replace")[-400:]
    return True, ""


def find_session(repo: str, tag: str) -> Path | None:
    """Locate the transcript this run wrote, by its unique eval_tag."""
    d = session_dir(repo)
    if not d.is_dir():
        return None
    for f in sorted(d.glob("*.jsonl"), key=lambda p: p.stat().st_mtime, reverse=True)[:30]:
        try:
            first = f.open(encoding="utf-8", errors="replace").readline()
            if json.loads(first).get("eval_tag") == tag:
                return f
        except (OSError, json.JSONDecodeError):
            continue
    return None


def collect(path: Path) -> dict:
    """Aggregate one transcript: findings + debrief cost + outcomes."""
    out = {"findings": [], "outcomes": Counter(), "prompt_tokens": 0, "completion_tokens": 0,
           "cache_read": 0, "rounds": 0, "duration_s": 0.0, "llm_failures": 0, "units": 0}
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            o = json.loads(line)
        except json.JSONDecodeError:
            continue
        t = o.get("type")
        if t == "finding":
            out["findings"].append({
                "fingerprint": o.get("fingerprint"),
                "symbol_id": o.get("symbol_id") or "",
                "path": o.get("path") or "?",
                "lines": f"{o.get('start_line')}-{o.get('end_line')}",
                "content": (o.get("content") or "")[:100],
            })
        elif t == "debrief":
            out["units"] += 1
            out["outcomes"][o.get("outcome") or "?"] += 1
            tok = o.get("tokens") or {}
            out["prompt_tokens"] += tok.get("prompt_tokens", 0)
            out["completion_tokens"] += tok.get("completion_tokens", 0)
            out["cache_read"] += tok.get("cache_read_tokens", 0)
            out["rounds"] += (o.get("rounds") or {}).get("main_task", 0)
        elif t == "session_end":
            out["duration_s"] = o.get("duration_seconds") or 0.0
            out["llm_failures"] = o.get("llm_failures") or 0
    return out


def match_findings(base: list[dict], arm: list[dict]) -> dict:
    # discard None fingerprints (pre-schema-v2 transcripts) — two unknowns must
    # not count as the same finding
    bf = {f["fingerprint"] for f in base if f["fingerprint"]}
    af = {f["fingerprint"] for f in arm if f["fingerprint"]}
    bl = {(f["path"], f["symbol_id"]) for f in base}
    al = {(f["path"], f["symbol_id"]) for f in arm}
    return {
        "exact_common": len(bf & af),
        "loose_common": len(bl & al),
        "only_base": [f for f in base if f["fingerprint"] not in af and (f["path"], f["symbol_id"]) not in al],
        "only_arm": [f for f in arm if f["fingerprint"] not in bf and (f["path"], f["symbol_id"]) not in bl],
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("corpus", help="corpus json（entries: [{name, from, to}]）")
    ap.add_argument("--arm", action="append", required=True,
                    help='arm 配置 "name[:feat=v,feat=v]"，可重复；第一个 arm 是对比基线')
    ap.add_argument("--repo", default=".", help="被重放的仓库（默认 cwd）")
    ap.add_argument("--ccr", default="ccr", help="ccr 可执行文件（默认 PATH 里的 ccr）")
    ap.add_argument("--only", help="只跑名字含此子串的 corpus 条目")
    ap.add_argument("--runs", type=int, default=1, help="每格重复次数（观测方差）")
    ap.add_argument("--out", help="产物目录（默认 ~/.casecodereview/replay/<ts>）")
    args = ap.parse_args()

    corpus = json.loads(Path(args.corpus).read_text(encoding="utf-8"))
    entries = [e for e in corpus["entries"] if not args.only or args.only in e["name"]]
    arms = [parse_arm(s) for s in args.arm]
    out_dir = Path(args.out) if args.out else Path.home() / ".casecodereview" / "replay" / str(int(time.time()))
    out_dir.mkdir(parents=True, exist_ok=True)

    results: dict[tuple[str, str, int], dict] = {}
    with (out_dir / "runs.jsonl").open("w", encoding="utf-8") as runlog:
        for e in entries:
            for arm_name, feat_args in arms:
                for i in range(args.runs):
                    tag = f"replay:{e['name']}:{arm_name}:{i}:{int(time.time())}"
                    print(f"▶ {e['name']} × {arm_name} (run {i})", flush=True)
                    ok, err = run_ccr(args.ccr, args.repo, e, feat_args, tag)
                    sess = find_session(args.repo, tag) if ok else None
                    if not ok or sess is None:
                        print(f"  ✗ {err or 'session not found'}", flush=True)
                        results[(e["name"], arm_name, i)] = {"error": err or "session not found"}
                        continue
                    r = collect(sess)
                    r["session"] = str(sess)
                    results[(e["name"], arm_name, i)] = r
                    runlog.write(json.dumps({"entry": e["name"], "arm": arm_name, "run": i,
                                             **{k: (dict(v) if isinstance(v, Counter) else v) for k, v in r.items()}},
                                            ensure_ascii=False) + "\n")
                    runlog.flush()
                    print(f"  ✓ units={r['units']} findings={len(r['findings'])} "
                          f"prompt_tok={r['prompt_tokens']} dur={r['duration_s']:.0f}s", flush=True)

    # ── report ──
    base_arm = arms[0][0]
    lines = [f"# replay report — corpus={args.corpus} arms={[a for a, _ in arms]} runs={args.runs}\n"]
    for e in entries:
        lines.append(f"\n## {e['name']}\n")
        lines.append("| arm | run | units | outcomes | findings | prompt tok | rounds | dur(s) |")
        lines.append("|---|---|---|---|---|---|---|---|")
        for arm_name, _ in arms:
            for i in range(args.runs):
                r = results.get((e["name"], arm_name, i), {})
                if "error" in r:
                    lines.append(f"| {arm_name} | {i} | — | ERROR: {r['error'][:60]} | | | | |")
                    continue
                oc = ", ".join(f"{k}×{v}" for k, v in sorted(r["outcomes"].items()))
                lines.append(f"| {arm_name} | {i} | {r['units']} | {oc} | {len(r['findings'])} "
                             f"| {r['prompt_tokens']} | {r['rounds']} | {r['duration_s']:.0f} |")
        # findings diff vs base (run 0 vs run 0)
        base = results.get((e["name"], base_arm, 0), {}).get("findings")
        if base is None:
            continue
        for arm_name, _ in arms[1:]:
            arm_f = results.get((e["name"], arm_name, 0), {}).get("findings")
            if arm_f is None:
                continue
            m = match_findings(base, arm_f)
            lines.append(f"\n**{base_arm} vs {arm_name}**: 指纹同 {m['exact_common']}，"
                         f"path+symbol 同 {m['loose_common']}；"
                         f"仅 {base_arm} {len(m['only_base'])}，仅 {arm_name} {len(m['only_arm'])}")
            for f in m["only_base"]:
                lines.append(f"- 仅 {base_arm}: `{f['path']}:{f['lines']}` {f['content'][:80]}")
            for f in m["only_arm"]:
                lines.append(f"- 仅 {arm_name}: `{f['path']}:{f['lines']}` {f['content'][:80]}")

    report = "\n".join(lines) + "\n"
    (out_dir / "REPORT.md").write_text(report, encoding="utf-8")
    print("\n" + report)
    print(f"artifacts → {out_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

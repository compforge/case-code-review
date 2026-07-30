#!/usr/bin/env python3
"""posterior.py — 后验扫描：finding 之后，作者做了什么。

半自动准确性档（eval/README「后验扫描」的脚本化）：对 session transcript 里
每条最终交付的 finding（schema v2 的 `finding` 记录），从评审锚点向前走 git
历史，看 finding 指过的行区间后来有没有被改。

  line_touched   后续 commit 改了 finding 指过的行 → important 实锤【候选】
  file_touched   行级追踪不可用（重命名/删除/-L 失败），但文件被改过 → 弱信号
  untouched      锚点以来无人碰过 → 无后验证据（不等于 finding 错）
  error          git 查询失败（锚点缺失、文件在锚点不存在等）

判定纪律不变：line_touched 只是候选——改动可能是无关重构，人要拿着
commit 对照 finding 文本确认"是在修它"。漏报侧（ccr 沉默处后来被修）不在
本脚本范围：它需要"评审看过但没报"的区域定义，另行处理。

锚点解析：diffCommit > diffTo > git_head（manifest 字段）。commit/range 重放
模式下评审看的是历史状态，锚点必须是被评审的那个 sha，不是跑评审时的 checkout。

Usage:
  python3 eval/posterior.py <session.jsonl | sessions-dir>... [--repo DIR]
      [--target REF] [--labels out.jsonl]

stdlib only — no pip installs.
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from collections import Counter
from pathlib import Path

GIT_TIMEOUT = 30


def load_sessions(paths: list[str]) -> list[dict]:
    """Read session jsonl files (or directories of them) into
    {manifest, findings, path} bundles; sessions without finding records are
    kept (they still count as '0 delivered' context for the caller)."""
    files: list[Path] = []
    for p in paths:
        pp = Path(p)
        if pp.is_dir():
            files.extend(sorted(pp.glob("*.jsonl")))
        else:
            files.append(pp)
    out = []
    for f in files:
        manifest, findings = {}, []
        try:
            # errors="replace": a stray non-UTF-8 byte in a record must not kill
            # the whole batch — UnicodeDecodeError is a ValueError and would
            # escape the (OSError, JSONDecodeError) except below.
            for line in f.read_text(encoding="utf-8", errors="replace").splitlines():
                line = line.strip()
                if not line:
                    continue
                rec = json.loads(line)
                if rec.get("type") == "session_start":
                    manifest = rec
                elif rec.get("type") == "finding":
                    findings.append(rec)
        except (OSError, json.JSONDecodeError) as e:
            print(f"[skip] {f}: {e}", file=sys.stderr)
            continue
        out.append({"path": str(f), "manifest": manifest, "findings": findings})
    return out


def anchor_of(manifest: dict) -> str:
    """The sha the review looked at — replay modes pin it via diffCommit/diffTo;
    workspace mode falls back to the checkout HEAD recorded as git_head."""
    return manifest.get("diffCommit") or manifest.get("diffTo") or manifest.get("git_head") or ""


def git(repo: str, *args: str) -> tuple[str, bool]:
    # No text=True: git output may carry non-UTF-8 bytes (legacy-encoded commit
    # messages), and the implicit strict decode raises UnicodeDecodeError — a
    # ValueError that would escape this except and kill the whole batch.
    try:
        r = subprocess.run(["git", "-C", repo, *args],
                           capture_output=True, timeout=GIT_TIMEOUT)
    except (subprocess.TimeoutExpired, OSError):
        return "", False
    return r.stdout.decode("utf-8", errors="replace"), r.returncode == 0


def commits_touching_lines(repo: str, anchor: str, target: str,
                           path: str, start: int, end: int) -> tuple[list[str], bool]:
    """Commits in anchor..target whose diff touched path:start-end, via git's
    line-log (-L follows the range through edits). Returns (commits, ok);
    ok=False means -L itself failed (rename, file unknown at some point) and
    the caller should degrade to file-level evidence."""
    start = max(start, 1)
    end = max(end, start)
    # %x00 is git's own NUL escape — expanded in output, so argv stays clean
    # (an argv with a literal NUL is rejected by exec).
    out, ok = git(repo, "log", "--format=%h%x00%s",
                  f"-L{start},{end}:{path}", f"{anchor}..{target}")
    if not ok:
        return [], False
    commits = [line.replace("\x00", " ") for line in out.splitlines() if "\x00" in line]
    return commits, True


def commits_touching_file(repo: str, anchor: str, target: str, path: str) -> list[str]:
    out, ok = git(repo, "log", "--format=%h%x00%s", f"{anchor}..{target}", "--", path)
    if not ok:
        return []
    return [line.replace("\x00", " ") for line in out.splitlines() if "\x00" in line]


def classify(repo: str, anchor: str, target: str, finding: dict) -> dict:
    path = finding.get("path") or ""
    start = int(finding.get("start_line") or 0)
    end = int(finding.get("end_line") or 0)
    label = {
        "fingerprint": finding.get("fingerprint"),
        "symbol_id": finding.get("symbol_id"),
        "path": path,
        "lines": f"{start}-{end}",
        "anchor": anchor,
        "commits": [],
    }
    if not anchor:
        label["class"] = "error"
        label["note"] = "no anchor in manifest (pre-v2 session?)"
        return label

    # A finding with no line data (file-level concern) must not be tested
    # against line 1: a hit there would be a fake line_touched, a miss a fake
    # untouched. Degrade straight to file-level evidence, and say so.
    if start >= 1:
        commits, ok = commits_touching_lines(repo, anchor, target, path, start, end)
        if ok:
            label["class"] = "line_touched" if commits else "untouched"
            label["commits"] = commits
            return label

    commits = commits_touching_file(repo, anchor, target, path)
    label["class"] = "file_touched" if commits else "untouched"
    label["commits"] = commits
    if commits:
        label["note"] = ("no line data; file-level evidence only" if start < 1
                         else "line-log unavailable (rename/delete?); file-level evidence only")
    return label


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("paths", nargs="+", help="session jsonl files or directories")
    ap.add_argument("--repo", help="repo to walk (default: the session's cwd)")
    ap.add_argument("--target", default="HEAD", help="walk anchor..TARGET (default HEAD)")
    ap.add_argument("--labels", help="write per-finding labels jsonl here")
    args = ap.parse_args()

    sessions = load_sessions(args.paths)
    labels: list[dict] = []
    tally: Counter = Counter()

    for s in sessions:
        findings = s["findings"]
        if not findings:
            continue
        manifest = s["manifest"]
        repo = args.repo or manifest.get("cwd") or "."
        anchor = anchor_of(manifest)
        print(f"\n== {s['path']}  (anchor {anchor[:12] or '???'} → {args.target})")
        for f in findings:
            lab = classify(repo, anchor, args.target, f)
            lab["session"] = manifest.get("sessionId")
            labels.append(lab)
            tally[lab["class"]] += 1
            mark = {"line_touched": "★", "file_touched": "~", "untouched": " ", "error": "!"}[lab["class"]]
            print(f" {mark} {lab['class']:<12} {lab['fingerprint']}  {lab['path']}:{lab['lines']}"
                  + (f"  [{lab.get('symbol_id')}]" if lab.get("symbol_id") else ""))
            for c in lab["commits"][:4]:
                print(f"      ↳ {c}")

    print(f"\n{sum(tally.values())} finding(s): " +
          ", ".join(f"{k}={v}" for k, v in sorted(tally.items())))
    if tally["line_touched"]:
        print("★ line_touched 是 important 实锤【候选】——人工对照 commit 与 finding 文本确认后才算数（见 eval/README）。")

    if args.labels:
        with open(args.labels, "w", encoding="utf-8") as fh:
            for lab in labels:
                fh.write(json.dumps(lab, ensure_ascii=False) + "\n")
        print(f"labels → {args.labels}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

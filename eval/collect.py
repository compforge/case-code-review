#!/usr/bin/env python3
"""Collect ccr review trajectories for a repo into an eval workspace.

Discovers session transcripts under ~/.casecodereview/sessions/ for the given
repo (including its .worktrees/* variants), exports each to an ATIF trajectory
via `ccr export --format atif`, and writes:

  <out>/<branch>-<sid8>.atif.jsonl   one ATIF trajectory per session
  <out>/comments.json                every code_comment payload, keyed by branch
  <out>/SUMMARY.md                   per-session / per-unit-chain inventory

The summary is the entry point of an eval round: it shows, per unit chain, the
tool mix and token spend — the raw material for the efficiency axis. Feed the
.atif.jsonl files to trajectory_judge.py for per-chain diagnosis. See README.md
for the full methodology.

stdlib only — no pip installs.

Usage:
  python3 eval/collect.py --repo /path/to/repo --out eval-out [--since 2026-07-02]
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from datetime import datetime
from pathlib import Path

SESSIONS_ROOT = Path.home() / ".casecodereview" / "sessions"


def encode_repo_path(repo: str) -> str:
    # Mirrors ccr's session-dir encoding: strip the leading separator, then
    # flatten separators to '-'. Worktrees under <repo>/.worktrees/<tag> encode
    # to '<enc>-.worktrees-<tag>', which is why discovery matches by prefix.
    return os.path.abspath(repo).lstrip(os.sep).replace(os.sep, "-")


def discover(repo: str, since: datetime | None) -> list[Path]:
    enc = encode_repo_path(repo)
    files: list[Path] = []
    if not SESSIONS_ROOT.is_dir():
        return files
    for d in SESSIONS_ROOT.iterdir():
        if d.name != enc and not d.name.startswith(enc + "-.worktrees-"):
            continue
        for f in d.glob("*.jsonl"):
            if since and datetime.fromtimestamp(f.stat().st_mtime) < since:
                continue
            files.append(f)
    return sorted(files, key=lambda f: f.stat().st_mtime)


def session_branch(path: Path) -> str:
    try:
        head = json.loads(path.open().readline())
        return head.get("gitBranch") or "unknown"
    except (json.JSONDecodeError, OSError):
        return "unknown"


def export_atif(session: Path, out: Path) -> dict | None:
    r = subprocess.run(["ccr", "export", "--format", "atif", str(session)],
                       capture_output=True, text=True)
    if r.returncode != 0:
        print(f"  ✗ export failed for {session.name}: {r.stderr.strip()[:200]}",
              file=sys.stderr)
        return None
    out.write_text(r.stdout, encoding="utf-8")
    line = r.stdout.splitlines()[0] if r.stdout.strip() else "{}"
    return json.loads(line)


def summarize(branch: str, traj: dict, md: list[str], comments: dict) -> None:
    subs = traj.get("subagent_trajectories") or []
    fm = traj.get("final_metrics") or {}
    md.append(f"\n## {branch}  units={len(subs)}"
              f"  prompt_tok={fm.get('total_prompt_tokens', 0):,}"
              f"  completion_tok={fm.get('total_completion_tokens', 0):,}")
    for s in subs:
        ex = s.get("extra", {})
        m = s.get("final_metrics") or {}
        tools: dict[str, int] = {}
        for st in s.get("steps", []):
            for tc in (st.get("tool_calls") or []):
                n = tc.get("function_name", "?")
                tools[n] = tools.get(n, 0) + 1
                if n == "code_comment":
                    a = tc.get("arguments", {})
                    comments.setdefault(branch, []).append({
                        "unit": ex.get("file_path"),
                        "path": a.get("file_path") or a.get("path"),
                        "lines": f"{a.get('start_line')}-{a.get('end_line')}",
                        "content": a.get("comment") or a.get("content") or "",
                    })
        tstr = " ".join(f"{k}×{v}" for k, v in sorted(tools.items(), key=lambda x: -x[1]))
        md.append(f"- `{ex.get('file_path', s.get('trajectory_id', '?'))}`"
                  f" steps={len(s.get('steps', []))} tools={sum(tools.values())}"
                  f" ({tstr}) ptok={m.get('total_prompt_tokens', 0):,}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--repo", default=".", help="repo path (default: cwd)")
    ap.add_argument("--out", required=True, help="output directory")
    ap.add_argument("--since", help="only sessions modified on/after this date (YYYY-MM-DD)")
    args = ap.parse_args()

    since = datetime.fromisoformat(args.since) if args.since else None
    sessions = discover(args.repo, since)
    if not sessions:
        print(f"no sessions found for {args.repo} under {SESSIONS_ROOT}", file=sys.stderr)
        return 1

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    md: list[str] = [f"# ccr eval collection — {os.path.abspath(args.repo)}"]
    comments: dict[str, list] = {}
    n_units = 0

    for sess in sessions:
        branch = session_branch(sess).replace("/", "-")
        atif = out_dir / f"{branch}-{sess.stem[:8]}.atif.jsonl"
        traj = export_atif(sess, atif)
        if traj is None:
            continue
        units = len(traj.get("subagent_trajectories") or [])
        n_units += units
        print(f"  ✓ {branch} ({sess.stem[:8]}) units={units} → {atif.name}")
        summarize(branch, traj, md, comments)

    n_comments = sum(len(v) for v in comments.values())
    md.append(f"\n## TOTAL sessions={len(sessions)} units={n_units} code_comments={n_comments}")
    (out_dir / "SUMMARY.md").write_text("\n".join(md) + "\n", encoding="utf-8")
    json.dump(comments, (out_dir / "comments.json").open("w"),
              ensure_ascii=False, indent=1)
    print(f"\nsessions={len(sessions)} units={n_units} code_comments={n_comments}")
    print(f"next: python3 eval/trajectory_judge.py {out_dir}/<branch>.atif.jsonl --no-llm")
    return 0


if __name__ == "__main__":
    sys.exit(main())

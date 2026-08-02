#!/usr/bin/env python3
"""Collect ccr review trajectories for a repo into an eval workspace.

Discovers session transcripts under ~/.casecodereview/sessions/ for the given
repo (including its .worktrees/* variants), exports each to an ATIF trajectory
via `ccr export --format atif`, and writes:

  <out>/<branch>-<sid8>.atif.jsonl   one ATIF trajectory per session
  <out>/comments.json                every code_comment payload, keyed by branch
  <out>/SUMMARY.md                   per-session / per-scope inventory

The summary is the entry point of an eval round: it shows, per Review 1 Unit or Review 2 Lane, the
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
            # Export only closed snapshots. An active review appends scope
            # records gradually, so collecting it can turn temporary absence
            # of a terminal submission into a false trajectory failure.
            if not session_complete(f):
                continue
            files.append(f)
    return sorted(files, key=lambda f: f.stat().st_mtime)


def session_complete(path: Path) -> bool:
    try:
        last = ""
        with path.open(encoding="utf-8") as stream:
            for line in stream:
                if line.strip():
                    last = line
        return bool(last) and json.loads(last).get("type") == "session_end"
    except (json.JSONDecodeError, OSError):
        return False


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
    units = [scope for scope in subs if (scope.get("extra") or {}).get("scope_kind") == "unit"]
    lanes = [scope for scope in subs if (scope.get("extra") or {}).get("scope_kind") == "lane"]
    other = [scope for scope in subs if scope not in units and scope not in lanes]
    md.append(f"\n## {branch}  scopes={len(subs)}  review1_units={len(units)}"
              f"  review2_lanes={len(lanes)}"
              f"  prompt_tok={fm.get('total_prompt_tokens', 0):,}"
              f"  completion_tok={fm.get('total_completion_tokens', 0):,}")
    for title, scopes in (("Review 1 · Unit", units), ("Review 2 · Lane", lanes), ("Other", other)):
        if not scopes:
            continue
        md.append(f"\n### {title} ({len(scopes)})")
        for scope in scopes:
            ex = scope.get("extra", {})
            m = scope.get("final_metrics") or {}
            tools: dict[str, int] = {}
            for step in scope.get("steps", []):
                for call in (step.get("tool_calls") or []):
                    name = call.get("function_name", "?")
                    tools[name] = tools.get(name, 0) + 1
                    if name == "code_comment":
                        args = call.get("arguments", {})
                        comments.setdefault(branch, []).append({
                            "unit": ex.get("file_path"),
                            "path": args.get("file_path") or args.get("path"),
                            "lines": f"{args.get('start_line')}-{args.get('end_line')}",
                            "content": args.get("comment") or args.get("content") or "",
                        })
            tool_summary = " ".join(
                f"{name}×{count}"
                for name, count in sorted(tools.items(), key=lambda item: -item[1])
            )
            label = (
                ex.get("file_path")
                if ex.get("scope_kind") == "unit"
                else scope.get("trajectory_id")
            ) or "?"
            md.append(f"- `{label}`"
                      f" steps={len(scope.get('steps', []))} tools={sum(tools.values())}"
                      f" ({tool_summary}) ptok={m.get('total_prompt_tokens', 0):,}")


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
    n_scopes = 0

    for sess in sessions:
        branch = session_branch(sess).replace("/", "-")
        atif = out_dir / f"{branch}-{sess.stem[:8]}.atif.jsonl"
        traj = export_atif(sess, atif)
        if traj is None:
            continue
        scopes = len(traj.get("subagent_trajectories") or [])
        n_scopes += scopes
        print(f"  ✓ {branch} ({sess.stem[:8]}) scopes={scopes} → {atif.name}")
        summarize(branch, traj, md, comments)

    n_comments = sum(len(v) for v in comments.values())
    md.append(f"\n## TOTAL sessions={len(sessions)} scopes={n_scopes} code_comments={n_comments}")
    (out_dir / "SUMMARY.md").write_text("\n".join(md) + "\n", encoding="utf-8")
    json.dump(comments, (out_dir / "comments.json").open("w"),
              ensure_ascii=False, indent=1)
    print(f"\nsessions={len(sessions)} scopes={n_scopes} code_comments={n_comments}")
    print("next: uv run --project eval/reviewbench python "
          f"eval/trajectory_judge.py {out_dir}/<branch>.atif.jsonl --no-llm")
    return 0


if __name__ == "__main__":
    sys.exit(main())

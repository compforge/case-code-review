"""corpus_build — 从本地 clone 枚举 merged PR，冻结成 corpus json。

一个 corpus 条目 = 一个 merge commit 的双亲（`--from M^1 --to M^2`；分支删除后
merge-base 会退化，必须用 merge 双亲重建评审范围）。输出与 eval/replay.py 的 corpus
格式兼容（entries[].name/from/to），额外带 repo 与规模字段供 reviewbench 使用。

docs-only PR 默认不入册（review 无产出面）；用 --include-docs 保留。

Usage:
    python3 -m reviewbench.corpus_build <repo-path> [--branch main] [--limit 30]
        [--include-docs] [--out corpus.json]
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

_PR_RE = re.compile(r"Merge pull request #(\d+)(?: from \S+)?")


def _git(repo: str, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", repo, *args], capture_output=True, text=True, check=True, timeout=120
    ).stdout


def build(repo: str, branch: str, limit: int, include_docs: bool) -> dict:
    out = _git(repo, "log", "--merges", "--first-parent", branch,
               f"--max-count={limit * 3}", "--format=%H|%P|%s")
    entries = []
    for line in out.splitlines():
        sha, parents, subject = line.split("|", 2)
        ps = parents.split()
        if len(ps) != 2:
            continue
        m = _PR_RE.search(subject)
        if not m:
            continue
        p1, p2 = ps
        files = [f for f in _git(repo, "diff", "--name-only", p1, p2).splitlines() if f]
        if not files:
            continue
        if not include_docs and all(f.endswith((".md", ".txt")) for f in files):
            continue
        stat = _git(repo, "diff", "--shortstat", p1, p2).strip()
        entries.append({
            "name": f"#{m.group(1)}",
            "from": p1,
            "to": p2,
            "title": subject,
            "files": len(files),
            "shortstat": stat,
        })
        if len(entries) >= limit:
            break
    repo_abs = str(Path(repo).resolve())
    return {
        "description": f"merged PRs of {Path(repo_abs).name} ({branch}, merge 双亲重建评审范围)",
        "repo": repo_abs,
        "entries": entries,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("repo")
    ap.add_argument("--branch", default="main")
    ap.add_argument("--limit", type=int, default=30)
    ap.add_argument("--include-docs", action="store_true")
    ap.add_argument("--out", type=Path)
    args = ap.parse_args()

    try:
        corpus = build(args.repo, args.branch, args.limit, args.include_docs)
    except subprocess.SubprocessError as e:
        # SubprocessError 同时覆盖 CalledProcessError 与 TimeoutExpired（timeout=120 抛后者）
        stderr = getattr(e, "stderr", None) or ""
        if isinstance(stderr, bytes):
            stderr = stderr.decode("utf-8", errors="replace")
        raise SystemExit(f"git failed（repo/branch 对吗？超时？）: {stderr.strip()[:200] or e}") from e
    except FileNotFoundError as e:
        raise SystemExit(f"not a directory or git missing: {e}") from e
    text = json.dumps(corpus, ensure_ascii=False, indent=2) + "\n"
    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(text, encoding="utf-8")
        print(f"{len(corpus['entries'])} entries → {args.out}", file=sys.stderr)
    else:
        print(text, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main())

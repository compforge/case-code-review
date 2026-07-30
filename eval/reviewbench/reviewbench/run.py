"""run — reviewbench 的一键入口：experiment yaml → worksheet → 报表。

    uv run python -m reviewbench.run experiments/gates-review-team.yaml [--fresh] [--only "#94"]

experiment yaml 形状（reviewbench 自有，比 eval_harness 通用 yaml 少一层——corpus
直接指 corpus json）：

    name: gates-review-team
    description: review_team 三臂消融
    corpus: ../data/corpus/ccr-self.json # corpus_build.py 产物（含 repo 字段）
    repo: /path/to/repo                  # 可省——corpus json 的 repo 字段优先
    engine: ccr                          # 基线 engine（Env 可覆盖 config.engine）
    envs:
      - {name: base, overrides: {config.features: "review_team=off"}}
      - {name: team, overrides: {config.features: "review_team=on"}}
      - {name: team-bulletin, overrides: {config.features: "review_team=on,post_bulletin=on"}}

产物：runs/<name>/<run-id>/{worksheet.jsonl, results.csv, report/}——worksheet 是
断点续跑的 checkpoint（reconciler 缺啥补啥，超时/失败重跑同目录即续）。
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from pathlib import Path

import yaml
from eval_harness.engine import run_experiment
from eval_harness.worksheet.worksheet import CellState
from eval_harness.model.evalset import EvalSet
from eval_harness.model.experiment import Env, Experiment, Target
from spec_case.model import Case

from reviewbench.adapter import (
    DurationS,
    EngineFailed,
    FindingCount,
    JudgePrecision,
    RepoProvisioner,
    ReviewSolver,
)


def load_experiment(path: Path, only: str | None, repo_override: str | None = None,
                    metric_names: list[str] | None = None) -> Experiment:
    cfg = yaml.safe_load(path.read_text(encoding="utf-8"))
    corpus_path = (path.parent / cfg["corpus"]).resolve()
    corpus = json.loads(corpus_path.read_text(encoding="utf-8"))
    # 优先级：CLI --repo > yaml repo > corpus json 的 repo（后者含机器路径，入库 corpus 不带）
    repo = repo_override or cfg.get("repo") or corpus.get("repo")
    if not repo or not Path(repo).is_dir():
        raise SystemExit(f"repo not found: {repo!r} (corpus json 的 repo 字段或 yaml 的 repo)")

    cases = [
        Case(
            id=e["name"].lstrip("#").replace(" ", "-").lower() or f"e{i}",
            input={"query": json.dumps({"repo": repo, "from": e["from"], "to": e["to"]})},
            desc=e.get("title", e["name"]),
        )
        for i, e in enumerate(corpus["entries"])
        if not only or only in e["name"]
    ]
    if not cases:
        raise SystemExit(f"no corpus entries matched (only={only!r})")

    return Experiment(
        name=cfg["name"],
        description=cfg.get("description", ""),
        target=Target(config={"engine": cfg.get("engine", "ccr"), "features": ""}),
        evalsets=[EvalSet(corpus=Path(corpus_path).stem, cases=cases)],
        envs=[Env(**e) for e in cfg.get("envs", [])] or [Env(name="base")],
        metrics=metric_names or [],
    )


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("experiment", type=Path)
    ap.add_argument("--fresh", action="store_true", help="忽略已有 checkpoint 重跑")
    ap.add_argument("--only", help="只跑名字含此子串的 corpus 条目（如 '#94'）")
    ap.add_argument("--repo", help="被评审 repo 的本地路径（覆盖 yaml/corpus）")
    ap.add_argument("--runs-dir", type=Path, default=Path("runs"))
    args = ap.parse_args()

    metrics = [JudgePrecision(), FindingCount(), DurationS(), EngineFailed()]
    exp = load_experiment(args.experiment, args.only, args.repo,
                          metric_names=[m.NAME for m in metrics])
    ws = asyncio.run(run_experiment(
        exp, RepoProvisioner(), ReviewSolver(), metrics,
        runs_dir=args.runs_dir, fresh=args.fresh, config_path=args.experiment,
    ))
    bad = sum(1 for r in ws.rows.values() if r.solve.state != CellState.OK)
    print(f"rows={len(ws.rows)} solve-not-ok={bad} → {args.runs_dir}/{exp.name}/", file=sys.stderr)
    return 0 if bad == 0 else 1


if __name__ == "__main__":
    sys.exit(main())

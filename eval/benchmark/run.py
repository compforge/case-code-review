"""Run frozen review cases through case-harness and retain case → Session links."""
from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path
import subprocess
import shutil

import yaml
from eval_harness.engine import run_experiment
from eval_harness.model.evalset import EvalSet
from eval_harness.model.experiment import Arm, Experiment, Service
from eval_harness.worksheet.worksheet import CellState, Worksheet
from spec_case.model import Case

from eval.benchmark.adapter import DurationS, EngineFailed, FindingCount, JudgePrecision, RepoProvisioner, ReviewSolver
from eval.eval_snapshot import canonical_sha256, file_sha256


class ReviewExperiment(Experiment):
    # The generic runner hashes case identities, but review inputs and model
    # configuration may change without a case rename. Bind resume to their bytes.
    def experiment_hash(self) -> str:
        return canonical_sha256({
            "experiment": super().experiment_hash(),
            "service": self.service.model_dump(),
            "cases": [case.input for _, case in self.cases()],
        })[:16]


def load_experiment(path: Path, only: str | None, repo_override: str | None = None,
                    metric_names: list[str] | None = None) -> Experiment:
    cfg = yaml.safe_load(path.read_text(encoding="utf-8"))
    corpus_path = (path.parent / cfg["corpus"]).resolve()
    corpus = json.loads(corpus_path.read_text(encoding="utf-8"))
    entries = [entry for entry in corpus["entries"] if not only or only in entry["name"]]
    if not entries:
        raise ValueError(f"no corpus entries matched: {only!r}")
    if repo_override and len({e.get("repository") for e in entries}) > 1:
        raise ValueError("--repo requires a single-repository selection; use repositories in YAML")
    dataset_id = file_sha256(corpus_path)
    cases = []
    for entry in entries:
        repo = (repo_override or cfg.get("repositories", {}).get(entry.get("repository"))
                or cfg.get("repo") or corpus.get("repo"))
        if not repo:
            raise ValueError(f"no local repository for {entry['name']}")
        local = Path(repo).expanduser()
        if not local.is_absolute():
            local = path.parent / local
        local = local.resolve()
        if not local.is_dir():
            raise ValueError(f"repo not found: {local}")
        for revision in (entry["from"], entry["to"]):
            subprocess.run(["git", "-C", str(local), "cat-file", "-e", f"{revision}^{{commit}}"],
                           check=True, capture_output=True, timeout=30)
        query = {
            **entry, "repo": str(local), "dataset": corpus.get("dataset", corpus_path.stem),
            "dataset_id": dataset_id,
        }
        cases.append(Case(id=entry.get("case_id", entry["name"]),
                          input={"query": json.dumps(query, ensure_ascii=False)},
                          desc=entry.get("title", entry["name"])))
    if len({case.id for case in cases}) != len(cases):
        raise ValueError("duplicate case IDs in corpus")
    experiment = ReviewExperiment(
        name=cfg["name"], description=cfg.get("description", ""),
        service=Service(config={"engine": cfg.get("engine", "ccr"), "features": "",
                              "model": cfg.get("model"), "concurrency": cfg.get("concurrency")}),
        evalsets=[EvalSet(caseset=corpus_path.stem, cases=cases)],
        arms=[Arm(id=e["name"], overrides=e.get("overrides", {})) for e in cfg.get("envs", [])] or [Arm(id="base")],
        metrics=metric_names or [],
    )
    for arm in experiment.arms:
        engine = str(arm.resolve(experiment.service).config["engine"])
        executable = shutil.which(engine)
        if not executable:
            raise ValueError(f"review executable not found: {engine}")
        # Same path can be rebuilt/upgraded; its bytes participate in resume identity.
        arm.overrides["config.engine"] = str(Path(executable).resolve())
        arm.overrides["config.engine_sha256"] = file_sha256(Path(executable))
    return experiment


def export_runs(ws: Worksheet, destination: Path) -> None:
    """Offline effect analysis consumes frozen cases and the exact solve metadata."""
    rows = []
    for row in ws.rows.values():
        query = json.loads(row.query)
        rows.append({
            "run_id": ws.run_id, "arm": row.arm_id, "case_id": row.case_id,
            "dataset": query["dataset"], "dataset_id": query["dataset_id"],
            "case": query, "solve_state": row.solve.state.value,
            "error": row.solve.error or row.solve.observations.get("reason"),
            "observations": row.solve.observations, **row.meta,
        })
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("experiment", type=Path)
    parser.add_argument("--fresh", action="store_true", help="Discard this run's checkpoint")
    parser.add_argument("--run-id", help="Independent repeat; same ID resumes its checkpoint")
    parser.add_argument("--only", help="Select case names containing this substring")
    parser.add_argument("--repo", help="Override local path for a single repository")
    parser.add_argument("--runs-dir", type=Path, default=Path("eval/data/runs/benchmark"))
    parser.add_argument("--parallel", type=int, default=1, help="Maximum concurrent review processes")
    parser.add_argument("--judge-precision", action="store_true", help="Also run the existing diff-only finding judge")
    args = parser.parse_args()
    if args.parallel < 1:
        parser.error("--parallel must be positive")
    metrics = [FindingCount(), DurationS(), EngineFailed()]
    if args.judge_precision:
        metrics.append(JudgePrecision())
    exp = load_experiment(args.experiment, args.only, args.repo, [m.NAME for m in metrics])
    ws = asyncio.run(run_experiment(
        exp, RepoProvisioner(), ReviewSolver(max_parallel=args.parallel), metrics,
        runs_dir=args.runs_dir, fresh=args.fresh, config_path=args.experiment, run_id=args.run_id,
    ))
    destination = args.runs_dir / exp.name / ws.run_id / "runs.jsonl"
    export_runs(ws, destination)
    bad = sum(row.solve.state != CellState.OK or bool(row.solve.observations.get("failed"))
              for row in ws.rows.values())
    print(f"cases={len(ws.rows)} failed={bad} → {destination}")
    return 1 if bad else 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Freeze a small state-recovery corpus and verify its behavior before review.

The oracle stays outside the generated review repositories. References label
specific claims, not whether a whole change is clean.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess

from eval.eval_snapshot import canonical_sha256


BASE = '''def reconcile(state, material, *, due=False, acknowledged=True):
    """Repeated due checks suppress unchanged material after acknowledgement.

    Unacknowledged material must remain eligible for a subsequent attempt.
    """
    version = tuple(material)
    if state.get("reviewed") == version:
        return "skipped"
    state["calls"] = state.get("calls", 0) + 1
    if acknowledged:
        state["reviewed"] = version
    return "reviewed"
'''

BOUNDED = BASE.replace('version = tuple(material)', 'version = tuple(material if due else material[:1])')
BILLING = BASE.replace('Repeated due checks suppress unchanged material after acknowledgement.',
                       'The same material may be billed at most once across repeated ticks.').replace(
                           'state["calls"] = state.get("calls", 0) + 1',
                           'state["calls"] = state.get("calls", 0) + 1\n    state.setdefault("charges", []).append(tuple(material))')
ARRIVAL = '''def reconcile(state, material, after_review=None):
    """Material arriving during a review remains eligible for a later review."""
    version = tuple(material)
    if state.get("reviewed") == version:
        return "skipped"
    state["calls"] = state.get("calls", 0) + 1
    if after_review:
        after_review()
    state["reviewed"] = tuple(material)
    return "reviewed"
'''

CASES = {
    "r01": (BASE, BOUNDED, "negative",
            "A bounded digest makes every subsequent unchanged due check invoke review forever; suppression can never recover."),
    "r02": (BASE, BOUNDED.replace('if acknowledged:', 'if acknowledged and not due:'), "positive",
            "Due passes no longer persist acknowledged material, so unchanged due checks repeatedly invoke review."),
    "r03": (BASE, BASE.replace('    if acknowledged:\n        state["reviewed"] = version',
                                '    state["reviewed"] = version'), "positive",
            "An unacknowledged attempt is marked reviewed; the next attempt skips material that still needs acknowledgement."),
    "r04": (BILLING, BILLING.replace('version = tuple(material)', 'version = tuple(material if due else material[:1])'), "positive",
            "A bounded pass followed by a due pass bills identical material twice, even though a later due pass skips it."),
    "r05": (ARRIVAL, ARRIVAL.replace('state["reviewed"] = tuple(material)', 'state["reviewed"] = version'), "negative",
            "Persisting the captured version permanently hides material that arrives during the callback; later review can never process it."),
}


def simulate(source: str, scenario: str) -> dict:
    namespace = {}
    exec(compile(source, "frozen-worker.py", "exec"), namespace)
    reconcile = namespace["reconcile"]
    state, material = {}, ["a", "b"]
    if scenario in ("r01", "r02", "r04"):
        outputs = [reconcile(state, material)]
        outputs.extend(reconcile(state, material, due=True) for _ in range(3))
    elif scenario == "r03":
        outputs = [reconcile(state, material, acknowledged=False), reconcile(state, material)]
    else:
        outputs = [reconcile(state, material, lambda: material.append("c")), reconcile(state, material)]
    return {"outputs": outputs, "state": state}


def verify_oracles() -> dict:
    traces = {case: {"before": simulate(before, case), "after": simulate(after, case)}
              for case, (before, after, _, _) in CASES.items()}
    assert traces["r01"]["after"]["outputs"] == ["reviewed", "reviewed", "skipped", "skipped"]
    assert traces["r02"]["before"]["outputs"][-1] == "skipped"
    assert traces["r02"]["after"]["outputs"] == ["reviewed"] * 4
    assert traces["r03"]["before"]["outputs"][-1] == "reviewed"
    assert traces["r03"]["after"]["outputs"][-1] == "skipped"
    assert len(traces["r04"]["before"]["state"]["charges"]) == 1
    assert len(traces["r04"]["after"]["state"]["charges"]) == 2
    assert traces["r04"]["after"]["outputs"][-1] == "skipped"
    assert traces["r05"]["before"]["outputs"][-1] == "skipped"
    assert traces["r05"]["after"]["outputs"][-1] == "reviewed"
    return traces


def build(destination: Path) -> dict:
    """Generate isolated synthetic Git inputs; never reset an existing repository."""
    traces = verify_oracles()
    destination.mkdir(parents=True, exist_ok=False)
    entries = []
    for case, (before, after, label, claim) in CASES.items():
        repo = destination / case
        repo.mkdir()

        def git(*args):
            return subprocess.run(["git", "-c", "user.name=Benchmark", "-c", "user.email=benchmark@example.invalid",
                                   "-c", "core.hooksPath=/dev/null", "-C", str(repo), *args],
                                  env={**os.environ, "GIT_AUTHOR_DATE": "2000-01-01T00:00:00+00:00",
                                       "GIT_COMMITTER_DATE": "2000-01-01T00:00:00+00:00"},
                                  capture_output=True, text=True, check=True, timeout=30).stdout.strip()

        git("init", "--quiet")
        (repo / "pyproject.toml").write_text('[project]\nname = "state-worker"\nversion = "0.1.0"\n')
        worker = repo / "worker.py"
        worker.write_text(before)
        git("add", ".")
        git("commit", "--quiet", "--no-gpg-sign", "-m", "Initial worker")
        base = git("rev-parse", "HEAD")
        worker.write_text(after)
        git("add", "worker.py")
        git("commit", "--quiet", "--no-gpg-sign", "-m", "Update worker")
        reference = {"label": label, "path": "worker.py", "side": "new", "text": claim,
                     "producer": "executable synthetic oracle", "start_line": 1, "end_line": len(after.splitlines())}
        reference["reference_id"] = canonical_sha256(reference)
        entries.append({"name": case, "case_id": case, "repository": case, "repo": str(repo.resolve()),
                        "from": base, "to": git("rev-parse", "HEAD"), "references": [reference]})
    corpus = {"dataset": "recovery-conditions-v1", "entries": entries}
    corpus["dataset_id"] = canonical_sha256(corpus)
    (destination / "corpus.json").write_text(json.dumps(corpus, indent=2))
    (destination / "oracle-traces.json").write_text(json.dumps(traces, indent=2))
    return corpus


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    corpus = build(args.destination)
    print(f"Frozen {len(corpus['entries'])} cases and executable oracles: {args.destination}")


if __name__ == "__main__":
    main()

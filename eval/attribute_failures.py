#!/usr/bin/env python3
"""Attribute known review issues to the CCR stage where delivery stopped.

The attribution is deterministic. It joins expected issue locations and stable
identities with one Session JSONL, then follows the persisted pipeline facts:

    Unit scope -> Hypothesis -> Assessment -> Trial decision -> Finding

Formation and runtime failures are inferred from the absence or terminal state
of the relevant Unit/Lane scopes. The tool deliberately does not use an LLM to
guess whether two differently worded claims are semantically equivalent.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import Counter, defaultdict
from pathlib import Path
from typing import Iterable


DELIVERED = "delivered"
EXECUTION = "execution"
FORMATION = "formation"
UNIT_REVIEW = "unit_review"
HYPOTHESIS_REVIEW = "hypothesis_review"
TRIAL = "trial"
FINDING = "finding"

_STAGE_DEPTH = {
    EXECUTION: 0,
    FORMATION: 1,
    UNIT_REVIEW: 2,
    HYPOTHESIS_REVIEW: 3,
    TRIAL: 4,
    FINDING: 5,
    DELIVERED: 6,
}
_EXPECTED_LABELS = {"important", "minor", "missed"}


def read_jsonl(path: Path) -> list[dict]:
    return [
        json.loads(line)
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]


def select_expected_issues(records: Iterable[dict]) -> list[dict]:
    """Keep explicit expected cases and accepted/missed label-dataset rows."""

    selected = []
    for record in records:
        if record.get("expected_delivery") is False:
            continue
        label = record.get("label")
        if label and label not in _EXPECTED_LABELS:
            continue
        selected.append(record)
    return selected


def _norm_path(value: object) -> str:
    return str(value or "").removeprefix("./")


def _int(value: object) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def _paths(record: dict) -> set[str]:
    values = record.get("paths") or []
    if isinstance(values, str):
        values = [values]
    out = {_norm_path(value) for value in values if value}
    if record.get("filePath"):
        out.add(_norm_path(record["filePath"]))
    return out


def _artifact(records: Iterable[dict], kind: str) -> list[dict]:
    return [
        record.get("data") or {}
        for record in records
        if record.get("type") == "artifact"
        and record.get("artifact_kind") == kind
    ]


def _issue_hypothesis_ids(issue: dict) -> set[str]:
    engine_hypothesis = ((issue.get("engine") or {}).get("hypothesis") or {})
    return {
        str(value)
        for value in (issue.get("hypothesis_id"), engine_hypothesis.get("id"))
        if value
    }


def _issue_hypothesis_fingerprints(issue: dict) -> set[str]:
    engine_hypothesis = ((issue.get("engine") or {}).get("hypothesis") or {})
    return {
        str(value)
        for value in (
            issue.get("hypothesis_fingerprint"),
            engine_hypothesis.get("fingerprint"),
        )
        if value
    }


def _location_matches(issue: dict, candidate: dict) -> bool:
    if _norm_path(candidate.get("path")) != _norm_path(issue.get("path")):
        return False
    line = _int(issue.get("line"))
    if line <= 0:
        return True
    start = _int(candidate.get("start_line"))
    end = _int(candidate.get("end_line")) or start
    return start > 0 and start <= line <= end


def _hypothesis_match_basis(issue: dict, hypothesis: dict) -> str:
    ids = _issue_hypothesis_ids(issue)
    fingerprints = _issue_hypothesis_fingerprints(issue)
    if ids and str(hypothesis.get("id") or "") in ids:
        return "hypothesis_id"
    if fingerprints and str(hypothesis.get("fingerprint") or "") in fingerprints:
        return "hypothesis_fingerprint"
    if _location_matches(issue, hypothesis):
        return "path_line"
    return ""


def _matching_hypotheses(issue: dict, hypotheses: list[dict]) -> list[dict]:
    matches = [
        hypothesis
        for hypothesis in hypotheses
        if _hypothesis_match_basis(issue, hypothesis)
    ]
    stable = [
        hypothesis
        for hypothesis in matches
        if _hypothesis_match_basis(issue, hypothesis) != "path_line"
    ]
    return stable or matches


def _matching_units(issue: dict, records: list[dict]) -> list[str]:
    path = _norm_path(issue.get("path"))
    symbol_id = str(issue.get("symbol_id") or "")
    units: set[str] = set()
    for record in records:
        if record.get("kind") != "unit":
            continue
        scope_id = str(record.get("scope_id") or "")
        if (symbol_id and scope_id == symbol_id) or path in _paths(record):
            units.add(scope_id)
    return sorted(unit for unit in units if unit)


def _scope_outcomes(records: list[dict], scope_ids: Iterable[str]) -> dict[str, list[str]]:
    wanted = set(scope_ids)
    outcomes: dict[str, list[str]] = defaultdict(list)
    for record in records:
        scope_id = str(record.get("scope_id") or "")
        if scope_id not in wanted:
            continue
        if record.get("type") in {"execution_end", "debrief"}:
            outcomes[scope_id].append(str(record.get("outcome") or "unknown"))
    return dict(outcomes)


def _scope_completed(outcomes: dict[str, list[str]]) -> bool:
    return any("completed" in values for values in outcomes.values())


def _assessment_passes(assessment: dict) -> bool:
    return (
        assessment.get("support") == "supported"
        and assessment.get("attribution") == "caused"
        and assessment.get("value") == "actionable"
        and assessment.get("novelty") == "new"
    )


def _result(issue: dict, stage: str, reason: str, evidence: dict) -> dict:
    return {
        "issue_id": str(issue.get("id") or issue.get("issue_id") or ""),
        "path": _norm_path(issue.get("path")),
        "line": _int(issue.get("line")),
        "stage": stage,
        "reason": reason,
        "evidence": evidence,
    }


def attribute_issue(issue: dict, records: list[dict]) -> dict:
    """Return the deepest stage reached by one expected issue."""

    issue_id = issue.get("id") or issue.get("issue_id")
    if not issue_id:
        raise ValueError("expected issue requires id or issue_id")
    if not issue.get("path"):
        raise ValueError(f"expected issue {issue_id!r} requires path")
    if not (
        _int(issue.get("line")) > 0
        or _issue_hypothesis_ids(issue)
        or _issue_hypothesis_fingerprints(issue)
    ):
        raise ValueError(
            f"expected issue {issue_id!r} requires line or hypothesis identity"
        )

    session_complete = any(record.get("type") == "session_end" for record in records)
    hypotheses = _artifact(records, "review_hypothesis")
    assessments = _artifact(records, "review_assessment")
    trials = _artifact(records, "trial_decision")
    findings = [record for record in records if record.get("type") == "finding"]
    matched_hypotheses = _matching_hypotheses(issue, hypotheses)
    hypothesis_ids = {
        str(hypothesis.get("id"))
        for hypothesis in matched_hypotheses
        if hypothesis.get("id")
    }
    hypothesis_matches = {
        str(hypothesis.get("id")): _hypothesis_match_basis(issue, hypothesis)
        for hypothesis in matched_hypotheses
        if hypothesis.get("id")
    }

    finding_fingerprints = {
        str(value)
        for value in (issue.get("fingerprint"), issue.get("finding_fingerprint"))
        if value
    }
    delivered = [
        finding
        for finding in findings
        if (
            hypothesis_ids
            and str(finding.get("hypothesis_id") or "") in hypothesis_ids
        )
        or (
            finding_fingerprints
            and str(finding.get("fingerprint") or "") in finding_fingerprints
        )
        or _location_matches(issue, finding)
    ]
    if delivered:
        return _result(
            issue,
            DELIVERED,
            "a matching Finding was delivered",
            {
                "hypothesis_matches": hypothesis_matches,
                "finding_fingerprints": sorted(
                    str(finding.get("fingerprint") or "") for finding in delivered
                ),
            },
        )

    units = _matching_units(issue, records)
    unit_outcomes = _scope_outcomes(records, units)
    if not units:
        stage = FORMATION if session_complete else EXECUTION
        reason = (
            "no formed Unit covers the expected issue path"
            if session_complete
            else "the session ended before a Unit covering the expected issue was recorded"
        )
        return _result(issue, stage, reason, {"session_complete": session_complete})

    if not matched_hypotheses:
        if not _scope_completed(unit_outcomes):
            return _result(
                issue,
                EXECUTION,
                "the relevant Unit did not complete Review 1",
                {"unit_ids": units, "unit_outcomes": unit_outcomes},
            )
        return _result(
            issue,
            UNIT_REVIEW,
            "the relevant Unit completed without a matching Hypothesis",
            {"unit_ids": units, "unit_outcomes": unit_outcomes},
        )

    assignments = _artifact(records, "review_lane_assignment")
    lane_ids = {
        str(assignment.get("lane_id"))
        for assignment in assignments
        if str(assignment.get("hypothesis_id") or "") in hypothesis_ids
        and assignment.get("lane_id")
    }
    lane_scope_ids = {
        str(record.get("scope_id"))
        for record in records
        if record.get("kind") == "lane"
        and (
            str(record.get("scope_id") or "") in lane_ids
            or str(record.get("scope_id") or "").removeprefix("hypothesis_review:")
            in lane_ids
        )
    }
    lane_outcomes = _scope_outcomes(records, lane_scope_ids)
    matched_assessments = [
        assessment
        for assessment in assessments
        if str(assessment.get("hypothesis_id") or "") in hypothesis_ids
    ]
    if not matched_assessments:
        lane_incomplete = bool(lane_ids) and (
            not lane_scope_ids or not _scope_completed(lane_outcomes)
        )
        if not session_complete or lane_incomplete:
            return _result(
                issue,
                EXECUTION,
                "Review 2 did not complete for the matching Hypothesis",
                {
                    "hypothesis_matches": hypothesis_matches,
                    "lane_ids": sorted(lane_ids),
                    "lane_outcomes": lane_outcomes,
                },
            )
        return _result(
            issue,
            HYPOTHESIS_REVIEW,
            "the matching Hypothesis received no Assessment",
            {
                "hypothesis_matches": hypothesis_matches,
                "lane_ids": sorted(lane_ids),
                "lane_outcomes": lane_outcomes,
            },
        )

    latest_by_hypothesis: dict[str, dict] = {}
    for assessment in matched_assessments:
        hypothesis_id = str(assessment.get("hypothesis_id") or "")
        previous = latest_by_hypothesis.get(hypothesis_id)
        if previous is None or _int(assessment.get("submission_index")) >= _int(
            previous.get("submission_index")
        ):
            latest_by_hypothesis[hypothesis_id] = assessment
    passing_ids = {
        hypothesis_id
        for hypothesis_id, assessment in latest_by_hypothesis.items()
        if _assessment_passes(assessment)
    }
    if not passing_ids:
        return _result(
            issue,
            HYPOTHESIS_REVIEW,
            "Review 2 rejected every matching Hypothesis",
            {
                "hypothesis_matches": hypothesis_matches,
                "assessments": latest_by_hypothesis,
            },
        )

    matched_trials = [
        trial
        for trial in trials
        if str(trial.get("hypothesis_id") or "") in passing_ids
    ]
    passing_trials = [
        trial
        for trial in matched_trials
        if trial.get("passed_trial") and trial.get("delivered")
    ]
    if not passing_trials:
        return _result(
            issue,
            TRIAL,
            "Trial did not approve a Review 2-supported Hypothesis",
            {
                "hypothesis_ids": sorted(passing_ids),
                "trial_decisions": matched_trials,
            },
        )

    return _result(
        issue,
        FINDING,
        "Trial approved the issue but no matching Finding was persisted",
        {
            "hypothesis_ids": sorted(passing_ids),
            "trial_decisions": passing_trials,
        },
    )


def attribute_issues(issues: list[dict], records: list[dict]) -> list[dict]:
    results = [attribute_issue(issue, records) for issue in issues]
    return sorted(
        results,
        key=lambda result: (
            _STAGE_DEPTH[result["stage"]],
            result["path"],
            result["line"],
            result["issue_id"],
        ),
    )


def write_jsonl(path: Path, records: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_suffix(path.suffix + ".tmp")
    temp.write_text(
        "".join(json.dumps(record, ensure_ascii=False) + "\n" for record in records),
        encoding="utf-8",
    )
    temp.replace(path)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("session", type=Path, help="one CCR Session JSONL")
    parser.add_argument("expected", type=Path, help="expected issues JSONL")
    parser.add_argument("--out", type=Path, help="write attribution JSONL here")
    args = parser.parse_args()

    records = read_jsonl(args.session)
    issues = select_expected_issues(read_jsonl(args.expected))
    results = attribute_issues(issues, records)
    if args.out:
        write_jsonl(args.out, results)
    else:
        for result in results:
            print(json.dumps(result, ensure_ascii=False))

    summary = {
        "issues": len(results),
        "by_stage": dict(sorted(Counter(result["stage"] for result in results).items())),
    }
    if args.out:
        summary["out"] = str(args.out)
    print(json.dumps(summary, ensure_ascii=False), file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

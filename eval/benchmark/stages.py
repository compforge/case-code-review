"""Project stage outputs from the same recorded production review."""
from __future__ import annotations

from dataclasses import asdict
from pathlib import Path

from eval.benchmark.session_compare import read_session
from eval.session_recording import execution_facts


def project(path: Path) -> dict:
    session = read_session(path)
    claims = []
    unassessed = []
    for hid, issue in session.hypotheses.items():
        decision = session.decisions.get(hid, {})
        # Trial names the accepted submission. Taking the last arbitrary artifact
        # could join a different reviewer or a superseded Assessment.
        assessment = session.assessments.get((
            hid, decision.get("assessment_submission_index", 0), decision.get("lane_id", "")))
        assessed = bool(assessment and assessment.get("reviewer_alias") != "system")
        if not assessed:
            unassessed.append(hid)
        accepted = bool(assessed and assessment.get("support") == "supported"
                        and assessment.get("attribution") == "caused")
        claims.append({
            **asdict(issue), "claim_id": hid, "review1": True, "review2": accepted,
            "review3": any(f.hypothesis_id == hid for f in session.findings),
            "assessment": assessment, "trial": decision,
        })
    # Preserve findings lacking an R1 association (legacy/partial recordings).
    for index, finding in enumerate(session.findings):
        if finding.hypothesis_id not in session.hypotheses:
            claims.append({**asdict(finding), "claim_id": f"finding:{index}",
                           "review1": False, "review2": False, "review3": True,
                           "assessment": None, "trial": {}})
    summary = session.summary()
    gaps = list(session.gaps)
    try:
        facts = execution_facts(session.recording.records)
    except (ValueError, TypeError, KeyError) as error:
        facts = {}
        gaps.append(str(error))
    unit_ids = {target.unit for target in session.targets}
    units_complete = bool(unit_ids) and all(session.unit_state(unit) == "completed" for unit in unit_ids)
    execution_complete = all(fact.get("outcome") == "completed" for fact in facts.values())
    complete = bool(session.end and not gaps and units_complete and execution_complete
                    and not unassessed and not summary["pending_calls"])
    return {
        "session_id": session.start.get("sessionId") or path.stem,
        "session": str(path.resolve()), "complete": complete, "gaps": gaps,
        "unassessed": unassessed, "claims": claims, "health": summary,
    }

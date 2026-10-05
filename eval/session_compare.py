#!/usr/bin/env python3
"""Compare persisted review evidence, without running models or declaring fixes."""
from __future__ import annotations

import argparse
from collections import Counter, defaultdict, deque
from dataclasses import asdict, dataclass, field
import json
from pathlib import Path
from session_recording import Recording, execution_facts


@dataclass(frozen=True)
class Issue:
    path: str
    side: str = "new"
    old_path: str = ""
    start_line: int = 0
    end_line: int = 0
    category: str = ""
    content: str = ""
    existing_code: str = ""
    fingerprint: str = ""
    hypothesis_id: str = ""
    origin_unit: str = ""

    @classmethod
    def read(cls, data: dict, hypothesis: dict | None = None) -> Issue:
        merged = {**(hypothesis or {}), **data}
        values = {key: merged[key] for key in cls.__dataclass_fields__ if key in merged}
        values.setdefault("hypothesis_id", merged.get("id", ""))
        values["side"] = values.get("side") or "new"
        for key, value in values.items():
            expected = int if key in ("start_line", "end_line") else str
            if not isinstance(value, expected):
                raise ValueError(f"invalid issue {key}")
        if not values.get("path") or values["side"] not in ("old", "new"):
            raise ValueError("missing issue path or invalid side")
        return cls(**values)

    @property
    def source_path(self) -> str:
        return (self.old_path or self.path) if self.side == "old" else self.path

    def key(self, method: str) -> tuple | None:
        value = self.fingerprint if method == "fingerprint" else self.existing_code or self.content
        if not value:
            return None
        # This is an explainable heuristic, not semantic equivalence. Preserve
        # token boundaries and never use a shared symbol alone as issue identity.
        kind = "snippet" if self.existing_code else "content"
        return (self.source_path, self.side or "new", self.category,
                method if method == "fingerprint" else kind, " ".join(value.split()))


@dataclass(frozen=True)
class Target:
    id: str
    unit: str
    path: str
    old_path: str
    before: tuple[tuple[int, int], ...]
    after: tuple[tuple[int, int], ...]

    @classmethod
    def read(cls, data: dict, unit: str) -> Target:
        def spans(key: str) -> tuple[tuple[int, int], ...]:
            return tuple((int(s["Start"]), int(s["End"])) for s in data.get(key, []) or [])
        return cls(data["id"], unit, data["path"], data.get("old_path", ""),
                   spans("before_edits"), spans("after_edits"))

    def ranges(self) -> list[tuple[str, str, int, int]]:
        return ([(self.old_path or self.path, "old", a, b) for a, b in self.before]
                + [(self.path, "new", a, b) for a, b in self.after])


@dataclass
class SessionEvidence:
    path: str
    start: dict = field(default_factory=dict)
    input: dict = field(default_factory=dict)
    targets: list[Target] = field(default_factory=list)
    findings: list[Issue] = field(default_factory=list)
    hypotheses: dict[str, Issue] = field(default_factory=dict)
    assessments: dict[tuple[str, int, str], dict] = field(default_factory=dict)
    decisions: dict[str, dict] = field(default_factory=dict)
    executions: dict[str, dict] = field(default_factory=dict)
    debriefs: dict[str, dict] = field(default_factory=dict)
    recording: Recording = field(default_factory=lambda: Recording([], []))
    end: dict | None = None
    gaps: list[str] = field(default_factory=list)

    def stage(self, hypothesis_id: str) -> dict:
        decision = self.decisions.get(hypothesis_id, {})
        if decision.get("delivered"):
            if any(f.hypothesis_id == hypothesis_id for f in self.findings):
                return {"state": "delivered"}
            return {"state": "incomplete", "reason": "Trial delivery has no persisted Finding"}
        if decision.get("passed_trial"):
            return {"state": "duplicate", "reason": "Trial passed; duplicate delivery suppressed"}
        assessment = self.assessments.get((hypothesis_id, decision.get("assessment_submission_index", 0),
                                           decision.get("lane_id", "")))
        if assessment is not None and assessment.get("reviewer_alias") != "system":
            axes = {k: assessment.get(k) for k in ("support", "attribution", "value", "novelty")}
            return {"state": "filtered", "assessment": axes, "reason": assessment.get("reason", ""),
                    "submission_index": decision["assessment_submission_index"],
                    "evidence": assessment.get("evidence", []),
                    "evidence_receipts": assessment.get("evidence_receipts", [])}
        execution = self.executions.get(hypothesis_id, {})
        return {"state": "incomplete", "execution_outcome": execution.get("outcome", "unknown"),
                "reason": "No Assessment linked by a Trial decision"}

    def unit_state(self, unit: str) -> str:
        if self.gaps or self.input.get("version") != 1:
            return "unknown"
        outcome = self.debriefs.get(unit, {}).get("outcome")
        if outcome != "completed":
            return "incomplete"
        if any(self.stage(hid)["state"] == "incomplete"
               for hid, issue in self.hypotheses.items() if issue.origin_unit == unit):
            return "incomplete"
        # A terminal session seals the proposal/Trial stream. Before then an
        # absent record cannot be treated as evidence of absence.
        return "completed" if self.end is not None else "incomplete"

    def summary(self) -> dict:
        costs = self.recording.costs()
        outcomes = Counter()
        units = 0
        for event in self.debriefs.values():
            if event.get("kind") == "unit":
                units += 1
                outcomes[event.get("outcome", "unknown")] += 1
        return {
            "findings": [asdict(f) for f in self.findings], "units": units, "outcomes": dict(outcomes),
            "prompt_tokens": costs["prompt_tokens"], "completion_tokens": costs["completion_tokens"],
            "cache_read": costs["cache_read_tokens"], "cache_write": costs["cache_write_tokens"],
            "rounds": costs["rounds"], "duration_s": (self.end or {}).get("duration_seconds"),
            "llm_failures": (self.end or {}).get("llm_failures"),
            "closed": self.end is not None, "gaps": self.gaps,
            "unknown_usage": costs["unknown_usage"], "estimated_usage": costs["estimated_usage"],
            "pending_calls": costs["pending_calls"], "error_calls": costs["error_calls"],
            "generation": {key: self.start[key] for key in
                           ("tool_version", "git_head", "model", "features", "params") if key in self.start},
        }


def read_session(path: Path) -> SessionEvidence:
    session = SessionEvidence(str(path))
    raw_findings: list[dict] = []
    raw_hypotheses: dict[str, dict] = {}
    seen: set[str] = set()
    session.recording = Recording.read(path)
    records = session.recording.records
    session.gaps.extend(session.recording.gaps)
    for number, event in enumerate(records, 1):
        try:
            if not isinstance(event, dict):
                raise ValueError("record must be an object")
            uuid = event.get("uuid")
            if uuid and uuid in seen:
                continue
            if uuid:
                seen.add(uuid)
            kind = event.get("type")
            if kind == "session_start":
                session.start = event
            elif kind == "session_end":
                session.end = event
            elif kind == "finding":
                raw_findings.append(event)
            elif kind == "debrief":
                scope = event.get("scope_id") or f"legacy:{number}"
                if scope in session.debriefs:
                    session.gaps.append(f"duplicate debrief: {scope}")
                else:
                    session.debriefs[scope] = event
            elif kind == "artifact":
                data = event["data"]
                artifact = event.get("artifact_kind")
                if artifact == "review_input":
                    session.input = data
                elif artifact == "review_unit":
                    session.targets.extend(Target.read(t, data["unit_id"]) for t in data.get("targets", []) or [])
                elif artifact == "review_hypothesis":
                    raw_hypotheses[data["id"]] = data
                elif artifact == "review_assessment":
                    session.assessments[(data["hypothesis_id"], data["submission_index"], data["lane_id"])] = data
                elif artifact == "trial_decision":
                    session.decisions[data["hypothesis_id"]] = data
                elif artifact == "hypothesis_review_execution":
                    session.executions[data["hypothesis_id"]] = data
        except (ValueError, TypeError, KeyError, AttributeError) as error:
            session.gaps.append(f"line {number}: {type(error).__name__}")
    try:
        facts = execution_facts(records)
        for hypothesis_id, association in session.executions.items():
            session.executions[hypothesis_id] = facts.get(association.get("execution_id"), {})
    except (ValueError, TypeError, KeyError, AttributeError) as error:
        session.gaps.append(str(error))
    for hid, data in raw_hypotheses.items():
        try:
            session.hypotheses[hid] = Issue.read(data)
        except (TypeError, ValueError):
            session.gaps.append("invalid review_hypothesis")
    for data in raw_findings:
        try:
            session.findings.append(Issue.read(data, raw_hypotheses.get(data.get("hypothesis_id"))))
        except (TypeError, ValueError):
            session.gaps.append("invalid finding")
    if not session.start:
        session.gaps.append("missing session_start")
    return session


def same_input(before: SessionEvidence, after: SessionEvidence) -> bool:
    return (before.input.get("version") == after.input.get("version") == 1
            and bool(before.input.get("repository"))
            and before.input.get("repository") == after.input.get("repository")
            and bool(before.input.get("change_digest"))
            and before.input.get("change_digest") == after.input.get("change_digest"))


def covers(required: tuple[int, int], intervals: list[tuple[int, int]]) -> bool:
    cursor, end = required
    for start, stop in sorted(intervals):
        if start > cursor:
            break
        cursor = max(cursor, stop + 1)
        if cursor > end:
            return True
    return False


def target_coverage(target: Target, before: SessionEvidence, after: SessionEvidence) -> str:
    if not same_input(before, after) or before.gaps or after.gaps or not target.ranges():
        return "unknown"
    selected: dict[tuple[str, str], list[tuple[int, int]]] = defaultdict(list)
    completed: dict[tuple[str, str], list[tuple[int, int]]] = defaultdict(list)
    unknown = False
    for candidate in after.targets:
        state = after.unit_state(candidate.unit)
        for path, side, start, end in candidate.ranges():
            selected[(path, side)].append((start, end))
            if state == "completed":
                completed[(path, side)].append((start, end))
            if state == "unknown" and any(p == path and s == side and start <= b and end >= a
                                         for p, s, a, b in target.ranges()):
                unknown = True
    if all(covers((a, b), completed[(p, s)]) for p, s, a, b in target.ranges()):
        return "completed"
    if unknown:
        return "unknown"
    if all(covers((a, b), selected[(p, s)]) for p, s, a, b in target.ranges()):
        return "incomplete"
    return "outside_scope"


def issue_coverage(issue: Issue, before: SessionEvidence, after: SessionEvidence) -> str:
    candidates = [t for t in before.targets if t.unit == issue.origin_unit and
                  issue.source_path == ((t.old_path or t.path) if issue.side == "old" else t.path)]
    located = [t for t in candidates if any(p == issue.source_path and s == (issue.side or "new")
                and a <= issue.start_line <= b for p, s, a, b in t.ranges())]
    candidates = located or candidates
    if len(candidates) != 1:
        return "unknown"
    return target_coverage(candidates[0], before, after)


def pair_issues(before: list[Issue], after: list[Issue]) -> tuple[list[dict], list[int], list[int]]:
    remaining_before, remaining_after = set(range(len(before))), set(range(len(after)))
    pairs = []
    for method in ("fingerprint", "evidence"):
        index: dict[tuple, deque[int]] = defaultdict(deque)
        for j in sorted(remaining_after):
            key = after[j].key(method)
            if key is not None:
                index[key].append(j)
        for i in sorted(remaining_before):
            key = before[i].key(method)
            if key is not None and index[key]:
                j = index[key].popleft()
                pairs.append({"before": i, "after": j, "matched_by": method})
                remaining_before.remove(i)
                remaining_after.remove(j)
    return pairs, sorted(remaining_before), sorted(remaining_after)


def compare(before: SessionEvidence, after: SessionEvidence) -> dict:
    unrelated = (before.input.get("repository") and after.input.get("repository")
                 and before.input["repository"] != after.input["repository"])
    pairs, missing, added = ([], list(range(len(before.findings))), list(range(len(after.findings)))) if unrelated else pair_issues(before.findings, after.findings)
    # Hypotheses already represented by delivered findings cannot also explain
    # another missing occurrence of the same issue.
    delivered = {f.hypothesis_id for f in after.findings}
    hypotheses = [] if unrelated else [h for hid, h in after.hypotheses.items() if hid not in delivered]
    stage_pairs, _, _ = pair_issues([before.findings[i] for i in missing], hypotheses)
    stages = {missing[p["before"]]: {**after.stage(hypotheses[p["after"]].hypothesis_id),
              "hypothesis_id": hypotheses[p["after"]].hypothesis_id, "matched_by": p["matched_by"]}
              for p in stage_pairs}
    absent = []
    for i in missing:
        coverage = issue_coverage(before.findings[i], before, after)
        stage = stages.get(i, {"state": "no_matching_hypothesis" if coverage == "completed" else "unknown"})
        # A missing Review 2 result is incomplete even if another, regrouped
        # target happened to finish reviewing the same source range.
        status = "not_reported" if coverage == "completed" and stage["state"] != "incomplete" else "not_reviewed"
        absent.append({"finding": asdict(before.findings[i]), "status": status,
                       "coverage": coverage, "stage": stage})
    warnings = list(before.gaps + after.gaps)
    if not same_input(before, after):
        warnings.append("Captured changes/repository differ or are unknown; negative coverage conclusions are unknown")
    for key in ("tool_version", "model", "features", "params", "reviewMode"):
        if before.start.get(key) != after.start.get(key):
            warnings.append(f"generation differs: {key}")
    if before.end is None or after.end is None:
        warnings.append("An unclosed session has partial evidence and cost only")
    left, right = before.summary(), after.summary()
    if left["unknown_usage"] or right["unknown_usage"]:
        warnings.append("Some calls have no usage; recorded token totals are partial, not complete cost")
    costs = {k: {"before": left[k], "after": right[k],
                 "delta": right[k] - left[k] if left[k] is not None and right[k] is not None else None}
             for k in ("prompt_tokens", "completion_tokens", "cache_read", "cache_write", "rounds", "duration_s",
                       "unknown_usage", "estimated_usage", "pending_calls", "error_calls")}
    return {
        "before": before.path, "after": after.path, "same_captured_changes": same_input(before, after),
        "persisting": [{"before": asdict(before.findings[p["before"]]),
                        "after": asdict(after.findings[p["after"]]), "matched_by": p["matched_by"]} for p in pairs],
        "new": [{"finding": asdict(after.findings[j]), "baseline_coverage": issue_coverage(after.findings[j], after, before)}
                for j in added],
        "absent": absent,
        "target_coverage": [dict(id=t.id, path=t.path, status=target_coverage(t, before, after)) for t in before.targets],
        "costs": costs, "warnings": warnings,
    }


def markdown(report: dict) -> str:
    absent = Counter(item["status"] for item in report["absent"])
    lines = [f"新增报告 {len(report['new'])} · 持续 {len(report['persisting'])} · "
             f"本次未再报告 {absent['not_reported']} · 未完成可比复查 {absent['not_reviewed']}", "",
             "新增不等于新引入；未再报告不等于已修复。匹配按路径、侧、类别及指纹/代码片段（缺失时用正文）启发式对齐，保留重复次数。",
             "", "原目标在本次的覆盖：" + str(dict(Counter(t['status'] for t in report['target_coverage']))), "",
             "| 成本 | 基线 | 本次 | 差值 |", "|---|---:|---:|---:|"]
    for name, values in report["costs"].items():
        lines.append(f"| {name} | {values['before']} | {values['after']} | {values['delta']} |")
    def location(issue: dict) -> str:
        path = (issue["old_path"] or issue["path"]) if issue["side"] == "old" else issue["path"]
        return f"{path}:{issue['start_line']} ({issue['side']})"
    lines.append("")
    for item in report["new"]:
        lines.append(f"- 新增报告 `{location(item['finding'])}`；基线覆盖 `{item['baseline_coverage']}`")
    for item in report["absent"]:
        stage = json.dumps(item["stage"], ensure_ascii=False).replace("\n", " ")
        lines.append(f"- `{location(item['finding'])}`：{item['status']}；覆盖 `{item['coverage']}`；阶段 {stage}")
    for warning in report["warnings"]:
        lines.append(f"- 证据限制：{warning}")
    return "\n".join(lines) + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("before", type=Path)
    parser.add_argument("after", type=Path)
    parser.add_argument("--out", type=Path, required=True, help="directory for comparison.json and REPORT.md")
    args = parser.parse_args()
    result = compare(read_session(args.before), read_session(args.after))
    args.out.mkdir(parents=True, exist_ok=True)
    (args.out / "comparison.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    (args.out / "REPORT.md").write_text(markdown(result), encoding="utf-8")
    print(f"artifacts → {args.out}")


if __name__ == "__main__":
    main()

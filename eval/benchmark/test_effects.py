import asyncio
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from eval_harness.engine import run_experiment
from eval_harness.model.experiment import Service
from eval_harness.worksheet.worksheet import Row
from harness_common.llm import LLMClient, LLMConfig, ChatResult

from eval.benchmark.adapter import DurationS, EngineFailed, FindingCount, RepoProvisioner, ReviewSolver
from eval.benchmark.converters.aacr import convert
from eval.benchmark.evaluate import evaluate, run_analysis
from eval.benchmark.matching import Matcher, pair_id
from eval.benchmark.run import load_experiment, export_runs
from eval.benchmark.stages import project
from eval.benchmark.test_session_compare import artifact, pipeline, unit
from eval.eval_snapshot import file_sha256


def reference(label="positive", path="a.go"):
    return {"reference_id": "r", "label": label, "path": path, "side": "new",
            "text": "Dereferencing nil causes a crash", "start_line": 10, "end_line": 10}


class EffectsTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def recording(self, *, closed=True, assessed=True, support="supported", low_value=True):
        path = self.root / "session.jsonl"
        events = [{"type": "session_start", "sessionId": "session-1", "tool_version": "test"},
                  artifact("review_input", version=1, repository="repo", change_digest="change"),
                  *unit(), *pipeline(support=support, assessed=assessed)]
        for event in events:
            if event.get("artifact_kind") == "review_assessment" and low_value:
                event["data"]["value"] = "low_value"
        if closed:
            events.append({"type": "session_end"})
        path.write_text("".join(json.dumps(event) + "\n" for event in events))
        return path

    def run_row(self, session, refs=None):
        return {"dataset": "test", "dataset_id": "snapshot", "case_id": "case-1",
                "run_id": "run-1", "arm": "base", "solve_state": "ok", "error": None,
                "session": str(session), "session_sha256": file_sha256(session),
                "case": {"references": refs if refs is not None else [reference()]}}

    def matcher(self, session, matched=True):
        claim = project(session)["claims"][0]
        decisions = self.root / "decisions.jsonl"
        decisions.write_text(json.dumps({"pair_id": pair_id(reference(), claim),
                                         "matched": matched, "reason": "human verification"}) + "\n")
        return Matcher(self.root / "matches.jsonl", decisions=decisions)

    def test_positive_and_negative_are_comment_labels_on_same_pr(self):
        row = {"githubPrUrl": "https://github.com/example/project/pull/7", "source_commit": "a",
               "target_commit": "b", "project_main_language": "Go", "comments": [
                   {"path": "a.go", "note": "claim", "side": "right", "from_line": 10, "to_line": 10}]}
        corpus = convert([row], [row], "Go")
        self.assertEqual(len(corpus["entries"]), 1)
        self.assertEqual([ref["label"] for ref in corpus["entries"][0]["references"]], ["positive", "negative"])
        self.assertEqual(convert([row], [], "Python")["entries"], [])

    async def test_low_value_supported_claim_reaches_review2_but_not_review3(self):
        session = self.recording()
        result = await evaluate(self.run_row(session), self.matcher(session))
        self.assertTrue(result["complete"])
        self.assertEqual(result["references"][0]["stages"], {"review1": True, "review2": True, "review3": False})
        self.assertEqual(result["selection_reasons"], ["review3_filtered"])
        self.assertEqual(result["hypothesis_ids"], ["h"])
        self.assertEqual(result["unit_ids"], ["u"])

    async def test_partial_preserves_match_and_marks_absence_unknown(self):
        session = self.recording(closed=False, assessed=False)
        result = await evaluate(self.run_row(session), self.matcher(session))
        self.assertFalse(result["complete"])
        self.assertEqual(result["references"][0]["stages"], {"review1": True, "review2": None, "review3": None})
        self.assertNotIn("reference_not_discovered", result["selection_reasons"])

    async def test_missing_or_changed_recording_never_becomes_a_miss(self):
        session = self.recording()
        run = self.run_row(session)
        session.write_text(session.read_text() + "\n")
        result = await evaluate(run, Matcher(self.root / "matches.jsonl"))
        self.assertFalse(result["complete"])
        self.assertEqual(set(result["references"][0]["stages"].values()), {None})
        session.unlink()
        result = await evaluate(run, Matcher(self.root / "matches.jsonl"))
        self.assertEqual(result["gaps"], ["session missing"])

    async def test_same_location_without_judge_is_unassessed_not_automatic_match(self):
        session = self.recording()
        result = await evaluate(self.run_row(session), Matcher(self.root / "matches.jsonl"))
        self.assertIsNone(result["references"][0]["stages"]["review2"])
        self.assertIn("match_unassessed", result["selection_reasons"])

    async def test_no_candidate_is_miss_only_for_complete_run(self):
        session = self.recording()
        result = await evaluate(self.run_row(session, [reference(path="other.go")]), Matcher(self.root / "matches.jsonl"))
        self.assertIn("reference_not_discovered", result["selection_reasons"])
        run = self.run_row(session, [reference(path="other.go")])
        run["error"] = "timeout"
        result = await evaluate(run, Matcher(self.root / "matches.jsonl"))
        self.assertNotIn("reference_not_discovered", result["selection_reasons"])

    async def test_negative_claim_repetition_is_separate_from_reference_coverage(self):
        session = self.recording()
        ref = reference(label="negative")
        decisions = self.root / "negative-matches.jsonl"
        decisions.write_text(json.dumps({"pair_id": pair_id(ref, project(session)["claims"][0]),
                                         "matched": True, "reason": "same incorrect claim"}) + "\n")
        result = await evaluate(self.run_row(session, [ref]),
                                Matcher(self.root / "matches.jsonl", decisions=decisions))
        self.assertEqual(result["selection_reasons"], ["negative_claim_supported"])

    def test_resume_identity_covers_model_and_reference_changes(self):
        corpus = self.root / "corpus.json"
        body = {"repo": str(self.root), "entries": [{"name": "same", "from": "a", "to": "b"}]}
        corpus.write_text(json.dumps(body))
        config = self.root / "experiment.yaml"
        config.write_text(f"name: test\ncorpus: corpus.json\nengine: {sys.executable}\nmodel: first\n")
        with patch("eval.benchmark.run.subprocess.run"):
            before = load_experiment(config, None).experiment_hash()
            config.write_text(config.read_text().replace("first", "second"))
            model_changed = load_experiment(config, None).experiment_hash()
            body["entries"][0]["references"] = [reference()]
            corpus.write_text(json.dumps(body))
            labels_changed = load_experiment(config, None).experiment_hash()
        self.assertEqual(len({before, model_changed, labels_changed}), 3)

    def test_only_trial_selected_submission_counts_and_system_fallback_does_not(self):
        session = self.recording(support="contradicted")
        events = [json.loads(line) for line in session.read_text().splitlines()]
        events.insert(-1, artifact("review_assessment", hypothesis_id="h", submission_index=2,
                                   lane_id="l", support="supported", attribution="caused"))
        session.write_text("".join(json.dumps(event) + "\n" for event in events))
        self.assertFalse(project(session)["claims"][0]["review2"])
        for event in events:
            if event.get("artifact_kind") == "review_assessment":
                event["data"]["reviewer_alias"] = "system"
        session.write_text("".join(json.dumps(event) + "\n" for event in events))
        self.assertFalse(project(session)["complete"])
        self.assertEqual(project(session)["unassessed"], ["h"])

    async def test_failed_judge_abstains_then_retries_and_caches_valid_result(self):
        client = LLMClient(LLMConfig("https://example.invalid", "test", "model"))
        matcher = Matcher(self.root / "matches.jsonl", client=client)
        claim = project(self.recording())["claims"][0]
        with patch.object(client, "complete", new_callable=AsyncMock) as complete:
            complete.return_value = ChatResult('{"matched":"yes"}', "model")
            self.assertIsNone((await matcher.match(reference(), claim))["matched"])
            complete.return_value = ChatResult('{"matched":true,"reason":"same trigger"}', "model")
            self.assertTrue((await matcher.match(reference(), claim))["matched"])
            self.assertTrue((await matcher.match(reference(), claim))["matched"])
            self.assertEqual(complete.await_count, 2)

    async def test_solver_preserves_timeout_session_and_never_passes_labels_to_ccr(self):
        session = self.recording(closed=False)
        row = Row(arm_id="base", arm_key="key", corpus="test", case_id="case", run_id="run",
                  query=json.dumps({"repo": str(self.root), "from": "a", "to": "b", "references": [reference()]}))
        with patch("eval.benchmark.adapter.find_session", return_value=session), \
             patch("eval.benchmark.adapter.subprocess.run", side_effect=subprocess.TimeoutExpired("ccr", 1)) as run:
            result = await ReviewSolver().solve(row, Service(config={"engine": "ccr"}), "repo")
        self.assertEqual(result.observations["failed"], 1)
        self.assertEqual(result.meta["session_id"], "session-1")
        self.assertNotIn(reference()["text"], " ".join(run.call_args.args[0]))
        self.assertEqual(result.meta["session_sha256"], file_sha256(session))

    async def test_case_harness_resume_and_effect_selection_share_same_session(self):
        session = self.recording()
        corpus = self.root / "corpus.json"
        corpus.write_text(json.dumps({"dataset": "test", "repo": str(self.root), "entries": [
            {"name": "case-1", "from": "a", "to": "b", "references": [reference()]}]}))
        config = self.root / "experiment.yaml"
        config.write_text(f"name: test\ncorpus: corpus.json\nengine: {sys.executable}\n")
        metrics = [DurationS(), EngineFailed(), FindingCount()]
        with patch("eval.benchmark.run.subprocess.run"):
            exp = load_experiment(config, None, metric_names=[metric.NAME for metric in metrics])
        with patch("eval.benchmark.adapter.find_session", return_value=session), \
             patch("eval.benchmark.adapter.subprocess.run", return_value=subprocess.CompletedProcess([], 0, '{"comments":[]}', '')) as run:
            for _ in range(2):
                ws = await run_experiment(exp, RepoProvisioner(), ReviewSolver(), metrics,
                                          runs_dir=self.root / "runs", run_id="repeat-1")
            self.assertEqual(run.call_count, 1)
        self.assertTrue(all(cell.state.value == "ok" for row in ws.rows.values() for cell in row.scores.values()))
        export_runs(ws, self.root / "runs.jsonl")
        results = await run_analysis(self.root / "runs.jsonl", self.root / "effects", self.matcher(session))
        self.assertEqual(results[0]["session_id"], "session-1")
        self.assertEqual(results[0]["run_id"], "repeat-1")
        selected = json.loads((self.root / "effects/selected.jsonl").read_text())
        self.assertEqual(selected["selection_reasons"], ["review3_filtered"])
        self.assertEqual(selected["session_sha256"], file_sha256(session))

    async def test_parallel_limit_bounds_review_processes(self):
        solver = ReviewSolver(max_parallel=1)
        active, maximum = 0, 0

        async def solve(row, target):
            nonlocal active, maximum
            active += 1
            maximum = max(active, maximum)
            await asyncio.sleep(0.01)
            active -= 1

        row = Row(arm_id="a", arm_key="k", corpus="c", case_id="c", query="{}")
        with patch.object(solver, "_solve", side_effect=solve):
            await asyncio.gather(*(solver.solve(row, Service(), "repo") for _ in range(3)))
        self.assertEqual(maximum, 1)


if __name__ == "__main__":
    unittest.main()

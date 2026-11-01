import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from eval.benchmark.recovery_cases import build, verify_oracles, CASES, simulate
from eval.benchmark.replay import main, run_ccr
from eval.benchmark.evaluate import load_runs, selection_reasons
from eval.eval_snapshot import file_sha256
from eval.benchmark.matching import pair_id
from eval.benchmark.session_compare import Issue


class RecoveryCasesTest(unittest.TestCase):
    def test_oracles_distinguish_persistence_and_irreversible_harm(self):
        traces = verify_oracles()
        self.assertEqual(len(traces), 5)
        self.assertEqual(traces["r01"]["after"]["outputs"][-1], "skipped")
        self.assertEqual(traces["r04"]["after"]["outputs"][-1], "skipped")
        self.assertEqual(len(traces["r04"]["after"]["state"]["charges"]), 2)
        # A claim-level negative is not a clean change: alternating modes still oscillate.
        namespace = {}
        exec(CASES["r01"][1], namespace)
        state = {}
        self.assertEqual([namespace["reconcile"](state, ["a", "b"], due=bool(i % 2)) for i in range(8)], ["reviewed"] * 8)

    def test_sources_and_references_freeze_separately_and_never_reset(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory)/"corpus"
            corpus = build(destination)
            self.assertEqual(len(corpus["entries"]), 5)
            for entry in corpus["entries"]:
                repo = Path(entry["repo"])
                self.assertEqual({p.name for p in repo.iterdir()}, {".git", "worker.py", "pyproject.toml"})
                self.assertNotIn("references", (repo/"worker.py").read_text())
                self.assertEqual(simulate((repo/"worker.py").read_text(), entry["case_id"]), verify_oracles()[entry["case_id"]]["after"])
            with self.assertRaises(FileExistsError):
                build(destination)

    def test_plan_only_has_no_review_side_effects_and_keeps_case_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            corpus = root/"corpus.json"
            corpus.write_text(json.dumps({"entries":[{"name":"case", "repo":str(root/"subject"), "from":"a", "to":"b"}]}))
            out = root/"out"
            with patch("sys.argv",["replay",str(corpus),"--arm","base","--arm","candidate","--runs","2","--out",str(out),"--plan-only"]), patch("eval.benchmark.replay.run_ccr") as run:
                self.assertEqual(main(),0)
            run.assert_not_called()
            plan = json.loads((out/"plan.json").read_text())
            self.assertEqual([r["arm"] for r in plan["schedule"]],["base","candidate","candidate","base"])
            self.assertTrue(all(r["repo"] == str((root/"subject").resolve()) for r in plan["schedule"]))

    def test_uniform_caps_reach_the_review_command(self):
        import subprocess
        with patch("eval.benchmark.replay.subprocess.run", return_value=subprocess.CompletedProcess([],0,b"",b"")) as run:
            run_ccr("ccr","/repo",{"from":"a","to":"b"},[],"tag",timeout_minutes=10,max_tokens_budget=1000,max_completion_tokens=200)
        self.assertEqual(run.call_args.args[0][-6:],["--timeout","10","--max-tokens-budget","1000","--max-completion-tokens","200"])

    def test_execution_and_recording_lookup_use_the_case_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            corpus = root/"corpus.json"
            subject = (root/"subject").resolve()
            corpus.write_text(json.dumps({"entries":[{"name":"case", "repo":str(subject), "from":"a", "to":"b"}]}))
            with patch("sys.argv",["replay",str(corpus),"--arm","base","--out",str(root/"out")]), patch("eval.benchmark.replay.run_ccr",return_value=(False,"offline")) as run, patch("eval.benchmark.replay.find_session",return_value=None) as find:
                self.assertEqual(main(),0)
            self.assertEqual(run.call_args.args[1],str(subject))
            self.assertEqual(find.call_args.args[0],str(subject))

    def test_replay_reference_import_is_bound_to_plan_and_corpus(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            corpus=root/"corpus.json"
            corpus.write_text(json.dumps({"entries":[{"name":"case","from":"a","to":"b","references":[{"label":"negative"}]}]}))
            runs=root/"runs.jsonl"
            runs.write_text(json.dumps({"entry":"case","arm":"base","run":0,"sequence":0})+"\n")
            (root/"plan.json").write_text(json.dumps({"corpus_sha256":file_sha256(corpus),"schedule":[{"case":"case","arm":"base","repeat":0,"from":"a","to":"b"}]}))
            row=load_runs(runs,corpus)[0]
            self.assertEqual(row["case"]["references"],[{"label":"negative"}])
            self.assertEqual(row["solve_state"],"ok")
            corpus.write_text(corpus.read_text()+" ")
            with self.assertRaisesRegex(ValueError,"corpus changed"):
                load_runs(runs,corpus)

    def test_r1_false_claim_remains_a_diagnostic_candidate_after_r2_rejects(self):
        result={"complete":True,"references":[{"reference":{"label":"negative"},"stages":{"review1":True,"review2":False,"review3":False}}]}
        self.assertEqual(selection_reasons(result),["negative_claim_proposed"])

    def test_claim_conditions_survive_projection_and_change_match_identity(self):
        claim = {"path":"worker.py", "content":"Work can repeat forever", "trigger":"only when due", "impact":"repeated work", "uncertainty":"whether a later acknowledgement can recover"}
        issue = Issue.read(claim)
        self.assertEqual(issue.trigger,claim["trigger"])
        self.assertEqual(issue.impact,claim["impact"])
        self.assertEqual(issue.uncertainty,claim["uncertainty"])
        reference={"path":"worker.py","text":"permanent failure"}
        self.assertNotEqual(pair_id(reference,claim),pair_id(reference,{**claim,"uncertainty":""}))

    def test_existing_plan_is_never_replaced_by_changed_corpus(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); corpus=root/"corpus.json"; out=root/"out"
            corpus.write_text(json.dumps({"entries":[{"name":"case","from":"a","to":"b"}]}))
            argv=["replay",str(corpus),"--arm","base","--out",str(out)]
            with patch("sys.argv",argv+["--plan-only"]):
                main()
            before=(out/"plan.json").read_bytes()
            corpus.write_text(json.dumps({"entries":[{"name":"case","from":"a","to":"CHANGED"}]}))
            with patch("sys.argv",argv),patch("eval.benchmark.replay.run_ccr") as run, self.assertRaises(SystemExit):
                main()
            run.assert_not_called()
            self.assertEqual((out/"plan.json").read_bytes(),before)

    def test_unknown_usage_stops_admission_without_claiming_budget_spent(self):
        for unknown in ("unknown_usage","pending_calls","estimated_usage"):
            with self.subTest(unknown=unknown), tempfile.TemporaryDirectory() as directory:
                root=Path(directory); corpus=root/"corpus.json"; out=root/"out"
                corpus.write_text(json.dumps({"entries":[{"name":"case","from":"a","to":"b"}]}))
                session=root/"review.jsonl"
                session.write_text('{"type":"session_start","schema_version":14}\n')
                summary={"units":1,"findings":[],"outcomes":{"completed":1},"prompt_tokens":0,"completion_tokens":0,"rounds":1,"duration_s":1,unknown:1}
                with patch("sys.argv",["replay",str(corpus),"--arm","base","--arm","candidate","--max-total-tokens","1","--out",str(out)]), patch("eval.benchmark.replay.run_ccr",return_value=(True,"")) as run, patch("eval.benchmark.replay.find_session",return_value=session), patch("eval.benchmark.replay.collect",return_value=summary.copy()):
                    main()
                self.assertEqual(run.call_count,1)
                rows=[json.loads(line) for line in (out/"runs.jsonl").read_text().splitlines()]
                self.assertIn("cost unknown",rows[1]["error"])

    def test_matrix_rejects_duplicate_or_out_of_range_rows_and_preserves_missing(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); corpus=root/"corpus.json"; runs=root/"runs.jsonl"
            corpus.write_text(json.dumps({"entries":[{"name":"case","from":"a","to":"b","references":[]}]}))
            schedule=[{"case":"case","arm":arm,"repeat":0,"from":"a","to":"b"} for arm in ("base","candidate")]
            (root/"plan.json").write_text(json.dumps({"corpus_sha256":file_sha256(corpus),"schedule":schedule}))
            row={"entry":"case","arm":"base","run":0,"sequence":0}
            runs.write_text(json.dumps(row)+"\n")
            imported=load_runs(runs,corpus)
            self.assertEqual(len(imported),2)
            self.assertIn("not recorded",imported[1]["error"])
            self.assertEqual(imported[1]["solve_state"],"error")
            runs.write_text((json.dumps(row)+"\n")*2)
            with self.assertRaisesRegex(ValueError,"duplicate replay"):
                load_runs(runs,corpus)
            runs.write_text(json.dumps({**row,"sequence":-1})+"\n")
            with self.assertRaisesRegex(ValueError,"outside frozen"):
                load_runs(runs,corpus)

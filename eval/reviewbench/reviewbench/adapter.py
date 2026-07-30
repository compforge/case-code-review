"""adapter — eval_harness 的 review 评测接线：Provisioner / Solver / metrics。

映射（eval/README「建立固定 corpus 并重放」）：
- case   = 一个 merged PR：``input.query`` 是 {"repo","from","to"} 的 JSON（canonical
  case 的 query 是字符串，review 的"请求"就是一个评审范围）。
- Env    = 对照臂：``config.engine``（ccr/ocr 二进制路径）+ ``config.features``
  （"k=v,k=v"，ccr --feature 语法；ocr 忽略）。全部 light——review 无 provision 资源。
- solve  = 在 repo 里跑一次 `<engine> review --from --to --format json`，
  answer = stdout JSON（findings），观测面记 duration / findings 数 / 失败。
- metric = 质量走 LLM judge（judge_precision，无 GT：对照真实 diff 逐条判真伪）；
  成本/健壮性走 measurement 通道（不进加权分）。

后验/人工标注（posterior.py / labels.py）按 fingerprint 在报表外再 join——judge
只是质量轴的第一道近似，标注是校准它的 ground truth。
"""

from __future__ import annotations

import asyncio
import json
import os
import subprocess
import time
import uuid

from eval_harness.metric.base import BaseMetric
from eval_harness.model.evalset import EvalSet
from eval_harness.model.experiment import Target
from eval_harness.model.sample import MetricResult, Sample
from eval_harness.schedule.reconcile import Solver, SolveResult
from eval_harness.worksheet.worksheet import Row
from harness_common.llm import LLMClient

SOLVE_TIMEOUT_S = 35 * 60  # 长跑 review（外层再叠 engine 自身的超时）
_DIFF_CAP = 24_000  # judge prompt 里 diff 的字符上限（超出截断，宁可漏判不炸上下文）
_MAX_JUDGED = 10  # 每单最多逐条判定的 finding 数


class RepoProvisioner:
    """review 没有要 provision 的资源——repo 本身就是 subject。prepare 只做存在性检查。"""

    async def prepare(self, key: str, target: Target, evalset: EvalSet) -> str:
        return f"repo-{key[:6]}"

    async def clean(self, key: str, subject_id: str) -> None:
        pass


class ReviewSolver(Solver):
    """跑一次 review。engine/features 来自 Env 覆盖后的 target.config。"""

    async def solve(self, row: Row, target: Target, subject_id: str) -> SolveResult:
        rng = json.loads(row.query)  # {"repo","from","to"}
        cfg = target.config or {}
        engine = cfg.get("engine", "ccr")
        cmd = [engine, "review", "--from", rng["from"], "--to", rng["to"], "--format", "json"]
        for kv in filter(None, (s.strip() for s in str(cfg.get("features") or "").split(","))):
            cmd += ["--feature", kv]
        env = dict(os.environ, CCR_EVAL_TAG=f"reviewbench:{row.env}:{row.case_id}:{uuid.uuid4().hex[:8]}")
        t0 = time.monotonic()
        try:
            r = await asyncio.to_thread(
                subprocess.run, cmd, cwd=rng["repo"], env=env,
                capture_output=True, text=True, timeout=SOLVE_TIMEOUT_S,
            )
        except subprocess.TimeoutExpired:
            return SolveResult(response=None, observations={
                "total_ms": SOLVE_TIMEOUT_S * 1000, "failed": 1, "reason": "solve timeout"})
        dur_ms = int((time.monotonic() - t0) * 1000)
        if r.returncode != 0:
            return SolveResult(response=None, observations={
                "total_ms": dur_ms, "failed": 1, "reason": r.stderr[-400:]})
        try:
            payload = json.loads(r.stdout)
        except json.JSONDecodeError as e:
            return SolveResult(response=None, observations={
                "total_ms": dur_ms, "failed": 1, "reason": f"stdout not json: {e}"})
        findings = payload.get("comments") or payload.get("findings") or []
        return SolveResult(
            response=json.dumps(findings, ensure_ascii=False),
            observations={"total_ms": dur_ms, "failed": 0, "findings": len(findings)},
            meta={"eval_tag": env["CCR_EVAL_TAG"]},
        )


def _findings(sample: Sample) -> list[dict]:
    try:
        return json.loads(sample.response or "[]")
    except json.JSONDecodeError:
        return []


_JUDGE_SYSTEM = """你是代码评审 finding 的核查员。对照真实 diff 逐条判定 finding 真伪。

判定纪律：
- 必须依据 diff 求证，不能顺着 finding 的文本信；论证看起来扎实但在这段实际代码路径上
  不成立的，判 wrong（"教科书事实错套"是最常见的误报形态）。
- pre-existing 问题（diff 没碰的行为）不算本单的有效 finding。
- 拿不准判 debatable，不要硬判。

对每条 finding 输出一行 JSON：{"i": <序号>, "verdict": "real"|"debatable"|"wrong", "why": "<一句话>"}。
除这些 JSON 行外不要输出任何内容。"""


class JudgePrecision(BaseMetric):
    """LLM judge 的 finding 精度近似：real / (real+debatable+wrong)。

    交付 0 条 finding 时弃权（na）——"正确的沉默"不该得 1 分也不该得 0 分，
    沉默的对错归 posterior 漏报扫描与人工复审，不归本 metric。
    """

    NAME = "judge_precision"
    WEIGHT = 1.0
    DESCRIPTION = "LLM 对照真实 diff 判定 findings 的真伪比例（无 finding 弃权）"

    def __init__(self, client: LLMClient | None = None) -> None:
        super().__init__()
        self._client = client or LLMClient.from_env()

    async def score(self, sample: Sample) -> MetricResult:
        findings = _findings(sample)[:_MAX_JUDGED]
        if not findings:
            return self.na("no findings (correct-silence judged elsewhere)")
        if not self._client.ready():
            return self.na("judge LLM not configured (EVAL_JUDGE_BASE/_KEY/_MODEL)")
        rng = json.loads(sample.query)
        # to_thread + 超时：score 在事件循环里并发跑，阻塞的 git 会拖住所有 solve
        diff = (await asyncio.to_thread(
            subprocess.run,
            ["git", "-C", rng["repo"], "diff", rng["from"], rng["to"]],
            capture_output=True, text=True, timeout=60,
        )).stdout[:_DIFF_CAP]
        listing = "\n".join(
            f'{i}. [{f.get("path", "?")}:{f.get("start_line", 0)}-{f.get("end_line", 0)}] '
            f'{(f.get("content") or "").strip()[:500]}'
            for i, f in enumerate(findings)
        )
        res = await self._client.complete(
            _JUDGE_SYSTEM, f"## diff（评审范围原文，可能截断）\n{diff}\n\n## findings\n{listing}"
        )
        verdicts: dict[int, str] = {}
        for line in (res.text or "").splitlines():
            line = line.strip().strip("`")
            if not line.startswith("{"):
                continue
            try:
                v = json.loads(line)
                verdicts[int(v["i"])] = v.get("verdict", "")
            except (json.JSONDecodeError, KeyError, ValueError):
                continue
        if not verdicts:
            return self.na("judge returned no parseable verdicts")
        real = sum(1 for v in verdicts.values() if v == "real")
        # 逐条 verdict 带 fingerprint 留痕：没有它就无法拿人工标注按
        # fingerprint 校准 judge（judge vs human 的一致率是质量轴自身的质检）
        per = [
            {"fp": findings[i].get("fingerprint", ""), "v": v}
            for i, v in sorted(verdicts.items())
            if 0 <= i < len(findings)
        ]
        return self.quality(real / len(verdicts), judgement=json.dumps(
            {"real": real, "judged": len(verdicts), "total": len(findings), "verdicts": per},
            ensure_ascii=False))


class FindingCount(BaseMetric):
    NAME = "findings"
    KIND = "measure"
    DESCRIPTION = "交付的 finding 条数（计数不是分数——多不等于好）"

    def score(self, sample: Sample) -> MetricResult:
        return self.measure(len(_findings(sample)), unit="cnt")


class DurationS(BaseMetric):
    NAME = "duration_s"
    KIND = "measure"
    DESCRIPTION = "单次 review 墙钟耗时"

    def score(self, sample: Sample) -> MetricResult:
        return self.measure(round((sample.observations.get("total_ms") or 0) / 1000, 1), unit="s")


class EngineFailed(BaseMetric):
    NAME = "engine_failed"
    KIND = "measure"
    DESCRIPTION = "engine 运行失败/超时/输出不可解析（健壮性面）"

    def score(self, sample: Sample) -> MetricResult:
        return self.measure(int(sample.observations.get("failed") or 0), unit="cnt")

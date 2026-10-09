# 数据集效果分析

用已标注的真实变更定位 CCR 的效果问题，再把待分析 case 交给轨迹分析。
正常运行生产 Review 1、Review 2、Review 3，分别观察发现、复核和最终交付；参考标签只交给
离线匹配器，不进入 review prompt。Review 1 prompt 和生产交付标准由产品需求决定。

## 准备 AACR-Bench

从 [AACR-Bench](https://github.com/alibaba/aacr-bench/tree/main/dataset) 下载
`positive_samples.json` 和 `negative_samples.json` 到 `eval/data/datasets/aacr/raw/`。
以下命令均在 CCR 仓库根执行：

```bash
uv run --project eval python -m eval.benchmark.converters.aacr \
  --positive eval/data/datasets/aacr/raw/positive_samples.json \
  --negative eval/data/datasets/aacr/raw/negative_samples.json \
  --language Go --out eval/data/datasets/aacr/go.json
```

转换器按 PR 与 base/head 合并两份输入，记录源文件 SHA256 和 dataset 身份，保留评论的
category、context、AI/人工来源、位置与标签。省略 `--language` 保留全部语言。
负标签表示该条评论不正确；同一个 PR 可以同时有正负评论，不能据此当成无缺陷样本。
正标签是参考关注点，未覆盖整个 PR 的所有可能缺陷。

## 运行实验

先准备包含指定 base/head commit 的本地仓库。`run` 在启动模型前验证提交存在，
不替使用者 clone、切换或重置仓库。默认一次运行一个 case。
在 `eval/data/experiments/aacr.yaml` 保存本机配置：

```yaml
name: aacr-go
corpus: ../datasets/aacr/go.json
engine: ccr
model: your-model-alias
concurrency: 1
repositories:
  example/project: /path/to/project
envs:
  - name: baseline
    overrides: {}
```

`repositories` 的 key 使用 case 的 `repository`，为选中的每个仓库提供路径。
单仓库既有 corpus 仍可通过 `repo` 或 `--repo` 指定；`--only` 按 case 名称子串筛选。

```bash
uv run --project eval python -m eval.benchmark.run \
  eval/data/experiments/aacr.yaml --only 'example/project#123@' --run-id baseline-1
```

调度、worksheet、断点续跑和基础指标使用 `case-harness`；相同配置与 run-id 恢复 checkpoint，
独立重复实验使用新的 `--run-id`。默认输出
`eval/data/runs/benchmark/<experiment>/<run-id>/runs.jsonl`，关联冻结 case、原始 Session、
内容 hash、实际版本/模型/feature 与命令状态。失败与超时保留已产生的 Session。
`--parallel` 限制 review 进程数；`--judge-precision` 可额外启用既有 diff-only Finding judge。

## 评价与筛选

```bash
uv run --project eval python -m eval.benchmark.evaluate \
  eval/data/runs/benchmark/aacr-go/baseline-1/runs.jsonl \
  --out eval/data/reports/benchmark/baseline-1 --judge
```

`--judge` 使用 `EVAL_JUDGE_BASE`、`EVAL_JUDGE_KEY`、`EVAL_JUDGE_MODEL` 配置的独立语义匹配器。
候选要求同路径与 diff 侧，行号与文本交给 matcher 判断是否表达相同具体问题。
这是用于 CCR 诊断的匹配口径，不声称复现 AACR 官方 leaderboard 分数。
匹配不判断交付价值，也不根据 Review 2 自身的支持结论决定真值。

不加 `--judge` 时生成待判定 pairs，保留 `unknown`；可在 `matches.jsonl` 的对应 pair 上填写
`matched: true/false/null` 和 `reason`，另存文件，通过 `--matches <file>` 输入人工判断。
人工判断覆盖自动判断；自动结果按输入、prompt、模型与服务身份缓存，缺失/失败判断可重试。

| 阶段 | 取样范围 | 用途 |
|---|---|---|
| Review 1 | 全部 Hypothesis | 发现能力诊断 |
| Review 2 | Trial 关联的有效 Assessment，supported 且 caused | 主要效果观察，保留 low_value |
| Review 3 | 实际持久化 Finding | 产品交付观察 |

报告分别统计正标签命中和负标签错误评论复现，保留分母、unknown 与 incomplete。
没有匹配标签的新结论不能自动算误报。Session 缺失、截断、未闭合、复核未完成或执行失败时，
已有命中保留，缺失结果不计作已确认的漏报或正确拒绝。

- `results.jsonl`：三个阶段输出、参考标签、匹配理由、阶段判断和运行健康。
- `matches.jsonl`：可复核的语义匹配记录；模型调用失败不生成假分数。
- `REPORT.md`、`summary.json`：按实验 arm 和阶段汇总。
- `selected.jsonl`：未发现、Review 2 未支持、Review 3 过滤、错误评论复现、未评估等待分析 case。

`review3_filtered` 是待检查的策略差异，可以是符合预期的低价值过滤，并非自动判定为缺陷。

## 转入轨迹分析

```bash
uv run --project eval python -m eval.trajectory.analyze_cases \
  eval/data/reports/benchmark/baseline-1/selected.jsonl \
  --out eval/data/reports/trajectory/baseline-1
```

沿 dataset/case/run/arm/Session 与 Hypothesis/Unit ID 回到原始证据，不重新运行评审。
轨迹入口验证 Session hash，输出可供既有 judge 使用的 ATIF 和确定性分析。
详细用法见 [轨迹分析](../trajectory/README.md)；人工标签与对照实验见
[labels](labels.md) 和 [replay](replay.md)。

# 每周效果与轨迹汇总

周报是 ATIF v1.7 Trajectory Run 与规范化标签数据集上的可再生成读模型；原始 session、labels、
datasets 和 runs 继续累积存储，不按周搬动。命令先通过 `trajectory_harness` 为本周和上周生成
Dataset/Run/HTML/Verdict，再从这两份持久化 artifact 投影 Markdown。默认生成上一个完整 ISO week，
并按 `Asia/Shanghai` 的周一零点切分：

```bash
uv run --project eval python -m eval.weekly_report
```

生成指定周、限定一个或多个仓库：

```bash
uv run --project eval python -m eval.weekly_report \
  --week 2026-W32 \
  --repo <repo-path>
```

`eval/data/` 是 gitignore 的本地事实源，不会随 git worktree 复制。在隔离 worktree 生成报告时，
周报会通过 Git common dir 自动读取主 worktree 的默认 datasets、GitHub harvest manifest 和 label dataset manifest；
需要使用其它事实源时，显式传入全部规范化数据集和 manifest：

```bash
uv run --project eval python -m eval.weekly_report \
  --dataset <shared-eval-data>/datasets/review-comments-public.jsonl \
  --dataset <shared-eval-data>/datasets/review-comments-private.jsonl \
  --github-label-manifest <shared-eval-data>/labels/github-harvest.json \
  --label-dataset-manifest <shared-eval-data>/datasets/label-dataset.json
```

缺少任一输入、存在无效 JSONL、harvest 未完整覆盖报告窗口、数据集未消费当前 harvest 快照或输出
摘要不匹配时，报告仍生成执行指标，但 label coverage 与 Finding 质量比例显示为不可用，不能把缺数据
解释成 `0%`。unpaired label 单独展示为数据质量信号，不会让其它已完整连接的 cohort 失效。

默认输出：

```text
eval/data/reports/trajectory/ccr-weekly/2026-W32/
├── dataset.json          固定 Trajectory Dataset 与构建健康
├── run.json              Detector/Verifier/Measurer 的唯一运行结果
├── report.html           trajectory_harness HTML 视图
└── verdict.json          trajectory_harness 统一出口

eval/data/reports/weekly/2026-W32/
├── REPORT.md             人读的本周数据与上周对比，以及最慢 Unit
├── metrics.json          可供后续周报继续比较的机器指标
├── unit-durations.jsonl  每个 Review 1 Unit 的耗时、结果、轮次和 token
└── manifest.json         周区间、时区、输入范围与生成时间
```

执行指标按 `session_start` 归周，而不是按 Session 文件 mtime。模型调用次数和
input/output/cache token 统一由 trajectory_harness 的 `ModelUsageMeasurer` 从 Trajectory 测量，
Verifier 不携带这些成本事实。报告分别展示 Review 1 Unit 与 Review 2 Lane 的完成率、
`workflow.timeout`、`llm.routing.timeout`、score、轮次、耗时、token、
工具频率和主要扣分项。Failure 同时给出 operation / execution impact、事件数和受影响轨迹比例，
不再把 workflow 终态 timeout 与 LLM timeout 合成一个口径。
`search_code` 按 Review stage 记录 Provider 实际产生的可见源码投影数、返回行数、预算截断和不可用
次数；有命中的 search request 作为分母，分别计算有无可见 source projection 后的 follow-up read
比例，避免用“所有 read 中有多少来自 search”错误衡量工具优化收益。自动 symbol projection 另行记录
尝试数、`expanded / ambiguous / oversized / unsupported / budget_rejected` outcome、实际返回源码行数，
并分别计算尝试及成功展开后的 follow-up read；成功展开后的读取再区分完全落在已返回 symbol span
内的重复读取，以及向 span 外扩展的补证读取，防止 fallback、重复消费和合理补证混成同一效果口径。
Initial FileOutline 可用情况同样按 stage 和 language 展示 admitted、empty、read/analysis error 以及预算/容量淘汰，
使 gotreesitter 升级或 CCR fallback 的收益能由运行事实验证。
Review 2 成本同时展示 per-Lane 与 per-Assessment，避免 Lane 在一周内承载的 Assessment 数量变化
扭曲效果判断。平均、p50 和 p95 耗时同时进入本周与上周的对比表。
`REPORT.md` 展示最慢的 20 个 Review 1 Unit，完整的逐 Unit 耗时记录保存在
`unit-durations.jsonl`，可按 Unit、Session、执行结果、轮次、token、模型和工具版本继续分析。
报告按 stage、工具版本、模型和仓库列出执行 cohort，窗口汇总不能替代 cohort 对比；验证工具或 loop
改动时仍应在同一固定 corpus 上重放。质量指标分为两个口径：`review_week` 按 dataset 中的
`engine.session_id` 回看本周产出的 finding，
`labeled_this_week` 按人工标签时间统计本周新增标注。版本和模型分布始终单列，避免把一周内混跑的
不同引擎直接当成同一 cohort。对已标注 Finding，报告分别展示 `important + minor` 的 accepted
比例以及 `wrong`、`repeat`、`debatable` 比例，不把它们压成含义不清的“准确率”。`ccr:missed`
只作为漏报信号计数；在每个被评审变更都没有完整人工 ground truth 之前，recall 保持不可用。
成本与效果只在共同 cohort 上联合：周报给出 `tokens / labeled accepted Finding`，其中 accepted
只包括人工标注的 `important + minor`，并始终同时展示 label coverage；标签不完整时，该单位成本
只能作为上界信号，不能当作完整质量结论。周报同时展示 GitHub harvest 的生成时间、PR 采集覆盖率、
失败数、精确时间窗覆盖状态和 dataset snapshot 一致性；任一 snapshot manifest 缺失或不一致时，
不能把本周 `0 label` 解释为真实效果事实。


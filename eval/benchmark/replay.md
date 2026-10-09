# 固定语料与对照回放

命令在仓库根执行。

## 可选：建立固定 corpus 并重放

从本地 clone 构建 merge-parent corpus：

```bash
uv run --project eval python -m eval.benchmark.corpus_build <repo-path> \
  --limit 30 \
  --out eval/data/corpus/<name>.json
```

用相同 corpus 对比 feature arms：

```bash
python3 -m eval.benchmark.replay eval/data/corpus/<name>.json \
  --repo <repo-path> \
  --arm base \
  --arm candidate:<feature>=on \
  --model <model> \
  --concurrency 1 \
  --runs 3 \
  --out eval/data/runs/<replay-name>
```

默认 `--schedule interleaved` 按重复轮次旋转 arm 首位；两臂三次依次执行
`base0,candidate0 → candidate1,base1 → base2,candidate2`，降低模型服务随时间变化造成的偏差。
需要复现旧的逐 arm 顺序时显式传 `--schedule arm-major`。每条 `runs.jsonl` 同时记录 schedule、
model/concurrency override、feature args，以及实际 Session 的 tool version、git head、model、features
和 params，不能只用命令行意图代替生成事实。执行失败也按 sequence 写入 `runs.jsonl`，避免终端里
看见失败、聚合数据却把该格静默丢掉。

`REPORT.md` 与 `comparisons.json` 对每个相同 repeat index 的 base/candidate Session 做确定性对比。
命令失败但已写出 Session 时，仍保留其中的 Finding、阶段记录和已落盘成本，不把失败样本当成空结果。

已有两份 Session 可直接离线比较，不调用模型：

```bash
python3 -m eval.benchmark.session_compare <baseline.jsonl> <candidate.jsonl> \
  --out eval/data/runs/<comparison-name>
```

报告分为新增报告、持续、本次未再报告、未完成可比复查四类。**新增报告不等于新引入缺陷，
本次未再报告不等于已修复**；它们只描述两次运行的观测差异。JSON 保留完整问题、匹配依据、
覆盖状态、阶段去向和成本差值，Markdown 提供摘要；这些产物与 Session 一样应留在本地 ignored 目录。

比较依据如下：

- 问题按路径、old/new 侧、类别匹配，先用原指纹，再用空白归一化的代码片段；缺少片段时才用正文。
  保留重复次数，不按同一 symbol 合并不同问题。这是可检查的启发式对应，不是语义等价或准确率；
  同片段的不同缺陷仍可能混淆，改写片段也可能无法匹配。
- `review_input` 的仓库身份和 captured change digest 一致时，按 Fragment 的前后编辑区间计算覆盖。
  Unit regrouping 或预算切分不会改变区间并集；同文件其他目标完成、工具读到文件、形成了 Unit 都不算完成。
  Unit 必须有 completed debrief，产生的 Hypothesis 必须有可关联的 Trial/Assessment，Session 也必须已结束。
- 覆盖状态保留 `completed / incomplete / outside_scope / unknown`。旧 Session 缺少输入或编辑范围、
  换了 revision、损坏记录或无法定位问题目标时，保留 unknown，不用文件名/行号重合猜测。
  digest 只标识捕获的改动材料与引用，不证明工作区所有上下文相同；跨 revision 的 rename/行号迁移不在本轮范围内。
- 对未再交付的问题，关联本次 Hypothesis、Trial 指向的 Assessment submission，展示过滤的四轴原因、
  重复抑制、复核未完成，或未找到匹配 Hypothesis。`passed_trial=false` 本身不是“已反驳”的证据。
- 成本按请求 Stage ID 汇总 llm_request/response/error，覆盖 Review 2 和未产生 debrief 的调用；Unit 数仍由 Unit debrief 统计。
  输入、输出、缓存分量单列，估算、缺失 usage 和仍在运行的调用显式计数。wall time 使用 session_end，
  不累加并发 Execution 耗时。未闭合 Session 的已落盘成本只是部分成本，未知 wall time 保留为空。
- Session source 默认纳入未闭合和末行截断的运行；完整实验样本用 RecordingQuery 的
  `attributes={"closed": True, "recording_incomplete": False}` 筛选，运行健康统计保留全部状态。
- Review 2 超时且没有有效 Assessment 时保持未评估；历史系统兜底不算完成证据。
  已在超时前提交的有效判断和 Finding 继续保留。

固定 corpus 的准入应同时满足：merge-parent 范围在目标仓库可解析；dry-run 能形成预期 Unit；健康
smoke 完整结束；轨迹实际产生本实验所需的行为分母。优化 search 时，应优先选少量单 Unit 且有
search 命中的样本，避免多 Unit 并发健康问题先于工具差异主导结果。候选与真实运行产物继续放在
ignored `eval/data/`，不为共享实验命令提交真实源码、finding 或 Session。

同工作负载比较时同时看 finding 质量、token/轮数成本和 incomplete/timeout；不能仅用 finding
数量或随 search volume 一起变化的比例判断优劣。search 投影实验以每个完成 Unit 的实际
`read_files` range 数为主指标，并把完整落在 expanded span 内的重复读与向 span 外补证分开报告。


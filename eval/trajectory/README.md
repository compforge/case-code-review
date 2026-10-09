# 轨迹分析

从效果分析筛出的 case 出发，解释发现、复核或交付发生了什么。分析原运行的 Session，
保留其 dataset/case/run/arm/Session 身份；重复 review 应作为新的实验运行。
所有命令在仓库根执行，共用 `eval/pyproject.toml`。

```bash
uv run --project eval python -m eval.trajectory.analyze_cases \
  eval/data/reports/benchmark/<run>/selected.jsonl \
  --out eval/data/reports/trajectory/<run>
```

`cases.jsonl` 保存 case 与各 Unit/Lane 的确定性分析；同一 Session 只导出一次 ATIF。
Session 缺失、内容变化或导出失败逐项保留，命令返回非零。ATIF 来自 `ccr export`，不会调用评审模型。
匹配到的 Hypothesis/Unit ID 用于定位，完整 Session 上下文保留，便于分析 Formation 和 Review 2 Lane。
需要具体原因诊断时，把输出的 ATIF 交给下文 `trajectory_judge --labels ...`；已有的周轨迹运行继续使用
`trajectory_diagnostics`。问题是否成立由效果标注判定，重复搜索、上下文丢失等原因由轨迹分析求证。

## 生成带人工标注的 trajectory 样本

规范化 finding 数据集含 `engine.session_id` 时，可以把本地 Session 轨迹与 forge comment label
汇成可直接消费的样本：

```bash
uv run --project eval python -m eval.trajectory.build_trajectory_dataset
```

处理链路为：

```text
CCRSessionSource.select/fetch
  → ATIF Recording
  → ATIFTrajectoryLoader
  → ATIF v1.7 Trajectory
  + normalized forge label annotation
  → versioned TrajectoryDataset
```

`CCRSessionSource` 是 CCR 对 `trajectory_harness.RecordingSource` 的领域实现：它只发现已关闭的
`~/.casecodereview/sessions` 记录并通过 `ccr export --format atif` 读取原始 ATIF；ATIF 格式投影仍由
Loader 负责，人工 label 则由 CCR 的 `TrajectoryDatasetBuilder` 子类组合，不进入 Source。可用
`--repo` 限定仓库，`--labels` 重复指定输入文件。

默认生成：

```text
eval/data/datasets/ccr-trajectories/
└── dataset.json         固定 Dataset、label annotations、provenance 与构建健康信息
```

该文件包含 prompt、源码上下文和评论内容，继续属于被忽略的本地真实数据；不要提交。没有阶段身份的
旧 label 仍可连接到 Session，其 `trajectory_ids` 为空，并计入 Dataset build health。

一键生成指定周的固定 Dataset、评价结果、成本 Measurement、HTML 报告和 Verdict：

```bash
uv run --project eval python -m eval.trajectory.ccr_trajectory_report \
  --week 2026-W34
```

Dataset、Worksheet、Metric、HTML 渲染和运行产物由 `trajectory_harness` 提供；CCR 定义 label
join、作为评估 target 的 Review 1/2、领域 Detector/Verifier 套件，以及面向周报的摘要投影。
HTML 展示人工 label 占比、Detector Finding、token、工具调用、耗时、周环比和数据健康；逐轨迹
Detection、Evaluation 与 Measurement 明细保留在 Dataset / Run JSON 中。产物统一落在
`eval/data/reports/trajectory/ccr-weekly/<YYYY-Www>/`，上周产物存在时自动加入趋势对比。
日常生成完整周报时直接使用下文的 `weekly_report.py`：它会先生成本周和上周的这些 canonical
Trajectory Run，再从同一持久化结果投影 Markdown，不会另行执行一套 Detector/Verifier/Measurer。

### Trajectory 接口与升级

评测依赖固定到 case-harness `afb1f3e`（ATIF v1.7、Verifier API），需要 Python 3.11+。
`ATIFTrajectoryLoader` 将 CCR 的历史 Session/Scope 导出转换为官方 ATIF models：模型身份、
消息、token metrics、工具参数与 observation 使用标准字段；执行状态、上下文曝光和来源身份
保留在 `extra.case_harness`。步骤 ID 是连续整数，Finding / Verification 的证据使用对应 ID
的字符串形式；批内请求序号不再拼入步骤 ID。

处理顺序为 `Trajectory → Measurements → Detector / Verifier`。Measurer 确定性派生事实，
Detector 发现行为模式，Verifier 验证显式判据；后二者的 `category=cost|effect` 与
`rule_type=hard|soft` 相互独立。CCR 保留阶段判据及其摘要分，运行结果使用
`TrajectoryAnalysisRun.verifications`，不再输出 `evaluations`。

旧版 `dataset.json` / `run.json` 不能直接交给新版读取。升级后运行 `weekly_report.py --week
<YYYY-Www>` 可从原始 Session 与 labels 重新生成当周和上周产物；只生成单周时，可先用
`ccr_trajectory_report.py --week <YYYY-Www> --no-history`，随后按时间顺序重建需要比较的周。
原始 Session 与 labels 无需迁移。诊断 schema / prompt 已升级到 v3，旧缓存不会被误用。

## 可选：采集本地 review trajectory

`collect.py` 从 CCR session 导出 ATIF、comments 和按 unit 汇总：

```bash
python3 -m eval.trajectory.collect \
  --repo <reviewed-repo-path> \
  --since <YYYY-MM-DD> \
  --out eval/data/runs/<collection-name>
```

ATIF 的 Scope `extra.request_timelines` 保存按请求身份关联的原生 timeline Document，包含
模型 fallback、HTTP 重试和传输阶段。没有请求终态时保留已记录的 running stage；这些诊断事实
不自动构成质量判定或额外模型调用。

不调用 LLM 的客观链路诊断：

```bash
uv run --project eval python -m eval.trajectory.trajectory_judge \
  eval/data/runs/<collection-name>/<trajectory>.atif.jsonl \
  --no-llm
```

诊断先由 CCR 的 ATIF Loader 将每个 scope 投影为通用 `Trajectory + Step`，再交给
`trajectory_harness` 的通用重复调用/失败重试 Detector，以及 CCR 自己的相邻读取、同轮未批量读取
和 search 后 read Detector；工具成功率、搜索范围、`read_files` 行覆盖率和 Unit 完成度仍由
Verifier 按明确契约判定。报告按 `scope_kind` 分开 Review 1 Unit 与 Review 2 Lane：两者都以 Session
Execution Stage 的终态 `attributes.outcome` 作为唯一执行完成信号；Review 1 另计 `hypothesis_yield`，Review 2 另计已接受和
尚未提交的 Assessment，避免把“自然 clean”误判为未完成，也避免把“产出过结果”误判为完整执行。文件读取额外报告
tool call 数、批内 range 请求数、占用的模型轮次、批量程度、新增行覆盖率、与初始 File Message 的重合率，以及相邻
小范围可合并出的理论最少读取数。相邻读取按同一 tool call、同一 inference turn 和跨 turn 分层；跨 turn
相邻通常是逐步导航，不能反推前一次调用已经知道后续范围，因此只产生 `adjacent_file_reads` 诊断信号，不参与综合分。
同一 inference turn 内发出多个 `read_files` 调用才产生 `unbatched_same_turn_reads` warning；较早的
`search_code` 命中被后续读取范围覆盖时产生 `search_then_read` info，供后续评估 symbol-aware read 等工具设计；
有命中的 search request 另作为稳定分母，区分 Provider 实际返回和未返回 source projection 后的
follow-up read 比例。
这些模式由 Detector 输出 Finding，并在 Dataset 上聚合 count/rate；Verifier 只输出可选 verdict
和/或 score，Model/Tool/Context Measurer 记录可计数、求和的事实。
重复读取与初始 Prompt 重叠仍参与综合分；轮次与耗时
按 Review 1 Unit 或 Review 2 已完成 Assessment 的数量归一化，避免把持续消费多个案卷的 Lane
误判为单次超长执行。`search_code` 同样区分 tool call、批内 query 和模型轮次，报告
average/max batch；零命中按 query 区分有效 scope、空 scope、scope 未知与工具失败，有效范围内
未找到内容本身不扣分，是否属于有价值反证再由后续轨迹判断。Detector 产生
`DetectionResult + Finding`，Verifier 产生 `VerificationResult`；两者分别表达模式发现与契约判断。
重复工具调用只产生带 hypotheses 的 `Finding`，不伪装为执行 Failure 或低分；
CCR 展示和传递这些诊断线索，并显式以其余 applicable score 的算术平均作为当前摘要分。可选 LLM
judge 只在其后解释“为什么慢或弱”，不再直接解析 ATIF 私有字段。

对已经持久化的 canonical Run 做批量 LLM 诊断时，先查看确定性候选计划和缓存覆盖：

```bash
uv run --project eval python -m eval.trajectory.trajectory_diagnostics \
  eval/data/reports/trajectory/ccr-weekly/<YYYY-Www> \
  --plan-only
```

确认候选数量后，再给本次运行设置新诊断预算；有效缓存命中不占该预算：

```bash
uv run --project eval python -m eval.trajectory.trajectory_diagnostics \
  eval/data/reports/trajectory/ccr-weekly/<YYYY-Www> \
  --uncached-limit 20
```

规划器只读取 Run 中已有的 Failure、Detection、Evaluation 和 Measurement，优先选择执行失败、
工具失败、契约失败、Detector finding 和 p95 成本异常，不会重新执行 Detector/Verifier/Measurer。
每条候选保留完整 step outline，但只展开 Evaluation/Finding `step_ids` 指向的步骤、错误步骤、初始
context 和终态附近步骤；digest 自动限制在 24KB，不要求调用方或模型猜测上下文范围。
`--plan-only` 同时展示新调用的启发式 input token 估算、证据步骤数和裁剪量，执行后 manifest 再记录
provider 实际返回的 input/output/cache token 及 usage coverage，使调用数和 token 成本都可复查。
每条诊断成功后立即写入版本化缓存；key 绑定 trajectory、对应 Run 投影、taxonomy/prompt schema
和 judge model，因此中断后可继续，也不会把旧 prompt 或其它模型的结果误当命中。运行目录额外生成：

```text
diagnostic-facets.jsonl   每条候选的诊断类别、证据、建议与缓存状态
diagnostic-manifest.json  候选、命中、新调用、错误、预算跳过、成本和实际覆盖率
```

这些结果是辅助定位原因的 `DiagnosticFacet`，不是 trajectory_harness 的 Finding、Evaluation 或
Verdict；是否修改工具、prompt 或上下文，仍需回到固定 corpus 的 replay/A/B 求证。缓存默认位于
`~/.casecodereview/eval-cache/trajectory-diagnostics/`，诊断产物位于 ignored `eval/data/`。

[HTML 报告示例](examples/trajectory-evaluation-report.html)展示了 Trajectory facts、Failure、
DetectionResult/Finding、VerificationResult、Measurement 与聚合 Metric 在同一读模型中的分层关系。

ATIF 把首次 `context_projected` 作为 Initial Context exposure；CCR eval 再用按工具注册的算子从轨迹中
提取 `ContextDemand`，按 `source / outline / reference / missing` 连接统计。`source→read` 与行重合率
一起判断是否重复；`outline→read` 表示关系判断正确但结构信息不足；`reference→read` 表示路径有用但
需要原文；`missing→read` 则提示 Language/Project Knowledge 尚未覆盖该关系。后续没有 demand 保持中性，
是否过量注入需要固定 corpus 的 A/B 成本与效果共同判断。
初始 FileOutline 的每次生成与准入尝试另记录语言、结果和 fallback 原因；周报按语言展示 admission rate，
用来区分 provider 能力缺口、读取/分析失败和上下文预算淘汰，不能只从最终 Prompt 反推未准入原因。

### 已知问题未交付的阶段归因

`attribute_failures.py` 将一组已知问题与一次 Session JSONL 对齐，沿
`Formation → Unit Review → Hypothesis Review → Trial → Finding` 找到交付停止的位置；若对应
Unit 或 Lane 没有完成，则归到 `execution`。归因只读取 Unit scope、Hypothesis、Assessment、Trial
decision、Finding 和终态事件，不调用 LLM。

已知问题使用 JSONL，每条至少提供稳定 `id`、`path`，以及 `line` 或 Hypothesis 身份：

```json
{"id":"known-issue-1","path":"path/to/file.go","line":42}
```

由 `build_label_dataset.py` 生成的记录也可直接作为输入；归因器只保留 `important`、`minor`、
`missed`，优先使用 `engine.hypothesis` 中的 ID / fingerprint，缺失时才回退到 `path + line`：

```bash
python3 -m eval.trajectory.attribute_failures \
  ~/.casecodereview/sessions/<repo>/<session>.jsonl \
  eval/data/datasets/expected-issues.jsonl \
  --out eval/data/runs/<run>/failure-attribution.jsonl
```

每条输出包含 `stage`、可复查的 `reason`，以及参与判断的 Unit、Hypothesis、Assessment、Trial 或
Execution 证据。`delivered` 表示已知问题成功交付，不属于失败阶段。

后验扫描见 [人工标签与后验验证](../benchmark/labels.md)。

## 上下文压缩的确定性回放

先用捕获的请求比较压缩变换，再用[固定 corpus](../benchmark/replay.md)做真实评审对照。Harness 提供可选测试：

```bash
CCR_CONTEXT_REPLAY="$PWD/eval/data/trajectory/<run>/replay-input.json" \
  go test ./internal/harness -run '^TestContextCompactionReplay$' -v
```

本地 fixture 包含 `messages`（Session 的 llm_request 消息）、`result`（最新工具调用的原始
LLMToolResult：Tool、ToolCallID、Arguments、Content）和 `ratio`（两种策略共用的目标比例）。
从同一 Execution 连接 request 与 tool_result，恢复该最新结果后，测试比较默认消息压缩和分区策略，
检查新证据保留、总预算及估算 token。fixture 与输出留在 ignored `eval/data/`，不提交真实源码。

这是捕获输入上的局部变换比较，不能还原整段消息原来的 Go 类型，也不能替代完整轨迹重放。
它不调用模型，不证明实际 token 成本、评审质量或超时率改善；后者需要对齐输入的真实运行，
并确认轨迹确实触发压缩，包含摘要调用成本和未完成的执行。


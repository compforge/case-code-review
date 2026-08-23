# eval — 采集 CCR 评审数据

本目录提供可公开复用的采集、规范化和重放工具。最短路径是：在 PR/MR finding 线程中完成
`ccr:label` 标注，用 `labels.py` 回收标签，再用 `build_label_dataset.py` 生成自包含 JSONL
数据集。Session JSONL、Viewer 与 eval 的职责边界见
[`docs/observability.md`](../docs/observability.md)。

## 数据边界

```text
eval/
├── *.py、reviewbench/   工具代码和配置模板，可进入 Git
└── data/                真实 corpus、labels、datasets、trajectory 和运行产物，不进入 Git
```

所有真实数据都写入 `eval/data/`，该目录已整体 gitignore。公开仓只跟踪通用 GitHub/GitLab
采集逻辑；绑定私有 forge、CLI 或实例协议的 adapter 仍应留在本地。

不要把 token、内部 URL、仓库名、用户名或真实 finding 复制到脚本、README、测试 fixture
或 tracked 配置中。

## 五分钟上手

以下命令都在仓库根目录执行。

### 1. 准备环境

- Python 3.10+；基础采集脚本只使用标准库。trajectory 诊断和 reviewbench 共用
  `eval/reviewbench` 的 uv 环境，其中 `case-harness` 提供 `trajectory_harness`。
- GitHub：安装 `gh` 并确认 `gh auth status` 成功。
- GitLab：在当前 shell 或 secret manager 中提供 `GITLAB_TOKEN`，并设置 `GITLAB_HOST`；
  不要把 token 写入仓库文件。
- 采集本地 trajectory 时，额外要求 `ccr` 已安装并产生过
  `~/.casecodereview/sessions/`。

先确认本地数据目录确实被忽略：

```bash
git check-ignore -v eval/data/
```

### 2. 在 PR/MR 上形成 ground truth

CCR finding comment 末尾通常带 `ccr:fp=<fingerprint>`。逐条对照真实 diff、源码和执行路径
求证后，在 finding 的同一线程回复：

```text
ccr:label=important — 会破坏现有调用契约，已采纳修复
ccr:label=minor — 问题成立但影响较小，已采纳修复
ccr:label=debatable — 属于取舍或防御性建议，不作为缺陷采纳
ccr:label=wrong — 实际调用在进入此处前已被校验 #cross-file
ccr:label=repeat — 同一问题已由本 MR 更早的 comment 提出（附 comment 链接或 id）
```

发现 CCR 漏掉的真实问题时，直接在 diff 行创建新评论：

```text
ccr:missed — 这里在并发关闭后仍可能写入已关闭 channel
```

标注纪律：

- 每条 finding 都标，不能只收集 `wrong` 或只收集采纳项。
- 必须查代码求证，不能因为 finding 文本听起来合理就同意它。
- `wrong` 给出可验证反证；拿不准用 `debatable`。
- `repeat` 只表示同一 MR 的更早 comment 已交付同一问题，并附其链接或 id；它不表示
  “问题在本次 diff 之前就存在”。
- 本次 diff 之前已存在的行为不算有效 finding，按 `wrong #out-of-diff` 标注并给出 attribution 反证。
- 可选病因 tag：`#textbook`、`#padding`、`#out-of-diff`、`#stale`、
  `#cross-file`。

### 3. 回收 GitHub 标签

按时间窗批量发现并回收当前用户已合并的 PR：

```bash
python3 eval/github_labels.py \
  --owner <github-owner> \
  --author @me \
  --since 2026-08-17 \
  --until 2026-08-23
```

默认按仓库 upsert 到 `eval/data/labels/<owner>-<repo>.jsonl`，并写入
`eval/data/labels/github-harvest.json`。manifest 记录发现、成功和失败的 PR 数；周报用它区分
“确实没有 label”和“采集没有运行或只完成了一部分”。GitHub resolved thread 的 review comments
仍能从 REST API 读取，批量采集不会过滤它们。

需要只刷新一个 PR 时使用单 PR 入口：

同一仓库的多个 PR 反复写入同一个文件即可；脚本按 `(source, reply_id)` upsert，重复执行安全。

```bash
python3 eval/labels.py github <owner>/<repo> <pr-number> \
  --out eval/data/labels/<owner>-<repo>.jsonl
```

### 4. 回收 GitLab 标签

先在调用环境中安全注入 `GITLAB_TOKEN`：

```bash
export GITLAB_HOST=gitlab.example.com
python3 eval/labels.py gitlab <group>/<repo> <mr-iid> \
  --host "$GITLAB_HOST" \
  --out eval/data/labels/<group>-<repo>.jsonl
```

`GITLAB_HOST` 也可以只通过 `--host` 传入。脚本支持标准 GitLab discussions API；私有平台若
协议不同，应在本地维护 adapter，并保持 gitignore。

### 5. 构建规范化数据集

```bash
python3 eval/build_label_dataset.py
```

默认读取：

```text
eval/data/labels/*.jsonl
~/.casecodereview/sessions/**/*.jsonl
```

默认生成：

```text
eval/data/datasets/review-comments-public.jsonl
eval/data/datasets/review-comments-private.jsonl
```

这里的 `public/private` 是按 forge 来源分桶，两个文件都属于真实数据，都会被 gitignore。
session finding 只用于补齐早期没有在 forge comment 中保存正文、但仍有 fingerprint 的记录。
新 session 还会按 fingerprint 与时间连接生成该 Finding 的 Hypothesis、Assessment 和执行身份；
找不到对应旧 session 时这些字段为空，不影响历史标签入集。

快速检查：

```bash
wc -l eval/data/datasets/*.jsonl
jq -s 'group_by(.label) | map({label: .[0].label, count: length})' \
  eval/data/datasets/review-comments-public.jsonl
```

单条规范化记录包含：

```json
{
  "id": "stable-example-id",
  "kind": "finding",
  "finding": "review comment body",
  "label": "wrong",
  "rationale": "human counter-evidence",
  "tags": ["cross-file"],
  "fingerprint": "finding-fingerprint",
  "path": "path/to/file",
  "line": 42,
  "model": "model-alias",
  "source": "forge:repo#change",
  "comment_url": "thread URL",
  "reply_id": "forge reply id",
  "by": "reviewer",
  "at": "RFC3339 timestamp",
  "engine": {
    "session_id": "session id",
    "tool_version": "ccr version",
    "model": "model id",
    "features": {},
    "params": {},
    "git_head": "reviewed HEAD",
    "hypothesis": {},
    "assessment": {}
  }
}
```

`engine` 让同一个 human verdict 能追溯到 generator、reviewer、feature 与 Trial 输入，避免把不同
引擎版本的结果混成同一组效果数据。若只比较收敛式 Hypothesis Review，不要重新运行 Unit Review；
先冻结候选集：

```bash
python3 eval/build_hypothesis_dataset.py
```

默认从两个 finding 数据集提取带阶段产物的样本，生成
`eval/data/datasets/review-hypotheses.jsonl`。其中 `expected_delivery` 由人工标签推导：
`important/minor` 应通过，其余标签应被拦截；后续 reviewer/Trial 实验必须消费同一批 Hypothesis，
才能把收敛精度变化与 Unit Review 的召回变化分开。

### 6. 生成带人工标注的 trajectory 样本

规范化 finding 数据集含 `engine.session_id` 时，可以把本地 Session 轨迹与 forge comment label
汇成可直接消费的样本：

```bash
uv run --project eval/reviewbench python eval/build_trajectory_dataset.py
```

处理链路为：

```text
CCRSessionSource.select/fetch
  → ATIF Recording
  → ATIFTrajectoryLoader
  → Trajectory bundle
  + normalized forge label annotation
  → labeled sample
```

`CCRSessionSource` 是 CCR 对 `trajectory_harness.RecordingSource` 的领域实现：它只发现已关闭的
`~/.casecodereview/sessions` 记录并通过 `ccr export --format atif` 读取原始 ATIF；ATIF 格式投影仍由
Loader 负责，人工 label 则在 Dataset builder 中组合，不进入 Source。可用 `--repo` 限定仓库，
`--labels` 重复指定输入文件。

默认生成：

```text
eval/data/datasets/ccr-trajectories/
├── trajectories.jsonl  每个 Session 一个 recording + canonical trajectories bundle
├── samples.jsonl       每个 label 一个样本，引用 recording_id 与 Unit/Lane trajectory_ids
└── manifest.json       只含构建计数和缺失统计
```

这些文件包含 prompt、源码上下文和评论内容，继续属于被忽略的本地真实数据；不要提交。没有阶段身份的
旧 label 仍可连接到 Session 级 bundle，其 `trajectory_ids` 为空，并计入 manifest。

## 可选：采集本地 review trajectory

`collect.py` 从 CCR session 导出 ATIF、comments 和按 unit 汇总：

```bash
python3 eval/collect.py \
  --repo <reviewed-repo-path> \
  --since <YYYY-MM-DD> \
  --out eval/data/runs/<collection-name>
```

不调用 LLM 的客观链路诊断：

```bash
uv run --project eval/reviewbench python eval/trajectory_judge.py \
  eval/data/runs/<collection-name>/<trajectory>.atif.jsonl \
  --no-llm
```

诊断先由 CCR 的 ATIF Loader 将每个 scope 投影为通用 `Trajectory + Step`，再交给
`trajectory_harness` 的通用重复工具调用 Evaluator，以及 CCR 自己的工具失败、失败空参数、空搜索、
`read_files` 行覆盖率和 Unit
完成度 Evaluator。报告按 `scope_kind` 分开 Review 1 Unit 与 Review 2 Lane：两者都以 Session
`execution_end.outcome` 作为唯一执行完成信号；Review 1 另计 `hypothesis_yield`，Review 2 另计已接受和
尚未提交的 Assessment，避免把“自然 clean”误判为未完成，也避免把“产出过结果”误判为完整执行。文件读取额外报告
tool call 数、批内 range 请求数、占用的模型轮次、批量程度、新增行覆盖率、与初始 File Message 的重合率，以及相邻
小范围可合并出的理论最少读取数。相邻读取按同一 tool call、同一 inference turn 和跨 turn 分层；跨 turn
相邻通常是逐步导航，不能反推前一次调用已经知道后续范围，因此只产生 `adjacent_file_reads` 诊断信号，不参与综合分。
同一 inference turn 内发出多个 `read_files` 调用才产生 `unbatched_same_turn_reads` warning；较早的
`search_code` 命中被后续读取范围覆盖时产生 `search_then_read` info，供后续评估 symbol-aware read 等工具设计；
有命中的 search request 另作为稳定分母，区分启用和未启用 `context_lines` 后的 follow-up read 比例。
每个 query 的自由字符串 `purpose` 同时按原值统计覆盖率和分布，用来发现尚未进入既有工具分类的搜索需求。
这些模式的原始 count/rate 保留在 objective analysis 中，Evaluator 只输出 verdict、score 和 Finding。
重复读取与初始 Prompt 重叠仍参与综合分；轮次与耗时
按 Review 1 Unit 或 Review 2 已完成 Assessment 的数量归一化，避免把持续消费多个案卷的 Lane
误判为单次超长执行。`search_code` 同样区分 tool call、批内 query 和模型轮次，报告
average/max batch；零命中按 query 区分有效 scope、空 scope、scope 未知与工具失败，有效范围内
未找到内容本身不扣分，是否属于有价值反证再由后续轨迹判断。确定性结果统一产生
`EvaluationResult`，用 status、verdict、可选 0~1 score、explanation 与 step id 区分执行健康、
判断和证据。重复工具调用只产生带 hypotheses 的 `Finding`，不伪装为执行 Failure 或低分；
CCR 展示和传递这些诊断线索，并显式以其余 applicable score 的算术平均作为当前摘要分。可选 LLM
judge 只在其后解释“为什么慢或弱”，不再直接解析 ATIF 私有字段。

[HTML 报告示例](examples/trajectory-evaluation-report.html)展示了 Trajectory facts、Failure、
EvaluationResult、Finding 与聚合 Metric 在同一读模型中的分层关系。

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
python3 eval/attribute_failures.py \
  ~/.casecodereview/sessions/<repo>/<session>.jsonl \
  eval/data/datasets/expected-issues.jsonl \
  --out eval/data/runs/<run>/failure-attribution.jsonl
```

每条输出包含 `stage`、可复查的 `reason`，以及参与判断的 Unit、Hypothesis、Assessment、Trial 或
Execution 证据。`delivered` 表示已知问题成功交付，不属于失败阶段。

后验扫描 finding 指向的代码是否被后续提交修改：

```bash
python3 eval/posterior.py <session.jsonl-or-dir> \
  --repo <reviewed-repo-path> \
  --labels eval/data/labels/<name>-posterior.jsonl
```

`line_touched` 只是后验候选，仍需人工确认后续 commit 是否确实在修该 finding。

## 可选：生成每周对比报告

周报是 Session 与规范化标签数据集上的可再生成读模型；原始 session、labels、datasets 和 runs
继续累积存储，不按周搬动。默认生成上一个完整 ISO week，并按 `Asia/Shanghai` 的周一零点切分：

```bash
uv run --project eval/reviewbench python eval/weekly_report.py
```

生成指定周、限定一个或多个仓库：

```bash
uv run --project eval/reviewbench python eval/weekly_report.py \
  --week 2026-W32 \
  --repo <repo-path>
```

`eval/data/` 是 gitignore 的本地事实源，不会随 git worktree 复制。在隔离 worktree 生成报告时，
周报会通过 Git common dir 自动读取主 worktree 的默认 datasets 和 GitHub harvest manifest；
需要使用其它事实源时，显式传入全部规范化数据集和 manifest：

```bash
uv run --project eval/reviewbench python eval/weekly_report.py \
  --dataset <shared-eval-data>/datasets/review-comments-public.jsonl \
  --dataset <shared-eval-data>/datasets/review-comments-private.jsonl \
  --github-label-manifest <shared-eval-data>/labels/github-harvest.json
```

缺少任一输入或存在无效 JSONL 时，报告仍生成执行指标，但 label coverage 与 Finding 质量比例显示
为不可用，不能把缺数据解释成 `0%`。

默认输出：

```text
eval/data/reports/weekly/2026-W32/
├── REPORT.md             人读的本周数据与上周对比，以及最慢 Unit
├── metrics.json          可供后续周报继续比较的机器指标
├── unit-durations.jsonl  每个 Review 1 Unit 的耗时、结果、轮次和 token
└── manifest.json         周区间、时区、输入范围与生成时间
```

执行指标按 `session_start` 归周，而不是按 Session 文件 mtime。模型调用次数和
input/output/cache token 统一由 trajectory_harness 的 `ModelUsageMeasurer` 从 Trajectory 测量，
Evaluator 不携带这些成本事实。报告分别展示 Review 1 Unit 与 Review 2 Lane 的完成率、
`workflow.timeout`、`llm.routing.timeout`、score、轮次、耗时、token、
工具频率和主要扣分项。Failure 同时给出 operation / execution impact、事件数和受影响轨迹比例，
不再把 workflow 终态 timeout 与 LLM timeout 合成一个口径。
`search_code` 的 request purpose 按 Review stage 保留原始自由文本，报告展示 purpose 覆盖率和
分布，用于观察真实搜索意图以及埋点完整度。`context_lines` 同时记录请求数、实际返回行数、预算截断
和不可用次数；有命中的 search request 作为分母，分别计算启用和未启用 context 后的 follow-up read
比例，避免用“所有 read 中有多少来自 search”错误衡量工具优化收益。
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
失败数和时间窗覆盖状态；manifest 缺失时，不能把本周 `0 label` 解释为真实效果事实。

## 可选：建立固定 corpus 并重放

从本地 clone 构建 merge-parent corpus：

```bash
cd eval/reviewbench
uv sync
uv run python -m reviewbench.corpus_build <repo-path> \
  --limit 30 \
  --out ../data/corpus/<name>.json
cd ../..
```

用相同 corpus 对比 feature arms：

```bash
python3 eval/replay.py eval/data/corpus/<name>.json \
  --repo <repo-path> \
  --arm base \
  --arm candidate:<feature>=on \
  --out eval/data/runs/<replay-name>
```

同工作负载比较时同时看 finding 质量、token/轮数成本和 incomplete/timeout；不能仅用 finding
数量判断优劣。

## 给协作者 AI 的执行约定

可以把下面这段直接交给协作者的 AI：

```text
在仓库根目录按 eval/README.md 采集 CCR 数据。
1. 先读取 AGENTS.md，确认公开仓脱敏规则。
2. 不打印、记录或提交 token；认证缺失时停下并告诉我缺什么。
3. 对每条 finding 查真实 diff/代码后再打 ccr:label，五类都收集；wrong 给具体反证，
   repeat 指向本 MR 更早的同问题 comment。
4. 使用 eval/github_labels.py 按时间窗批量回收 GitHub PR；单个 PR 再用 eval/labels.py 补采。
5. 运行 eval/build_label_dataset.py，报告总数、label 分布和 unpaired 数。
6. 需要轨迹样本时再运行 eval/build_trajectory_dataset.py，报告 linked/missing/export 计数。
7. 最后确认 git ls-files eval/data 和
   git ls-files --others --exclude-standard eval/data 都没有输出。
8. 不执行 git add/commit/push，除非我明确要求。
```

## 收口检查

```bash
uv run --project eval/reviewbench python -m unittest discover \
  -s eval -p 'test_*.py'

python3 -m py_compile \
  eval/labels.py \
  eval/github_labels.py \
  eval/build_label_dataset.py \
  eval/build_trajectory_dataset.py \
  eval/build_hypothesis_dataset.py \
  eval/collect.py \
  eval/ccr_source.py \
  eval/ccr_trajectory.py \
  eval/attribute_failures.py \
  eval/posterior.py \
  eval/replay.py \
  eval/trajectory_judge.py \
  eval/weekly_report.py \
  eval/weekly_report_render.py

git ls-files eval/data
git ls-files --others --exclude-standard eval/data
git status --short
```

前两个 `git ls-files` 命令必须没有输出。最终只汇报样本数量和分布，不在终端或聊天中粘贴
真实 finding 正文。

## 常见问题

- **采集为 0**：先检查 `github-harvest.json` 的时间窗与 PR 覆盖率；再确认 label 是 finding 线程的
  回复，父 comment 带 `ccr:fp=` 或 CCR header。resolved thread 不会阻止 GitHub REST 拉取评论。
- **GitLab 401/403**：确认 token 有读取 MR discussions 的权限，host 没带协议前缀。
- **出现 unpaired**：优先重新采集带 finding 正文的 forge thread；早期记录可由本地 session
  fingerprint 回填。
- **找不到 session**：确认 `--repo` 是当时运行 CCR 的仓库路径，且对应 session 尚在
  `~/.casecodereview/sessions/`。
- **重复采集**：可以直接重跑；labels 输出是幂等 upsert，不需要手工去重。

# 可观测性

CCR 用两个视角观察同一次执行：**SessionView 看一个 Agent Session 如何运行，JobView 看一个 Unit 如何经过 R1、R2 和 Trial 得到结果。** 两者使用 `agent-ledger/observation` 的 Node 模型，模型和工具正文来自原生 Session。

存储只有两类 JSONL：

```text
<review-id>.jsonl             Review 业务日志：元数据、Operation 状态、产物和终态
<review-id>.sessions/
  <local-file>.jsonl          AgentGo 原生 Session：模型、工具、消息和恢复数据
```

JobView 是读取时的投影，不再持久化另一份完整 View，也没有独立的 `.timeline` 文件。
一个 Review 可有多个 Unit Job，一个持续运行的 Lane Session 也可参与多个 Job。

### Agent 与 Job 的关系

“任务式”和“持续式”描述 Agent 与 Job 的关系，不是两种 Session 数据结构。任务式 Agent 的一次
Session 可能完成一个 Job；持续式 R2 Lane 则在同一个 Session 的不同 Run 中处理多个 Unit 的 Hypothesis。
JobView 根据明确的 Unit 归属选择 Operation，再通过 Session/run/execution/attempt 四元组选择调用。
不会把整个共享 Session 塞进每个 Job，也不把时间相邻解释成因果关系。

Job 使用 `schema_version: "agent-ledger/job@1"`、`job.source: "ccr"` 和
`job.id: "<review-id>/<unit-id>"`。业务 Operation 保留原 ID、父子关系和源头时间；导入的 ModelCall / ToolCall
保留 `session.source: "agentgo"`、原 Session ID、Run ID 和原 Node ID。

Goal 来自 `review_unit`，Result 包含 Unit 终态与相关 Hypothesis、Assessment、Trial、Finding 等业务证据。
Unit debrief / 最终 Completion 决定完成状态；操作返回成功、Review 关闭或没有 Finding 都不能单独证明 Job 完成。
模型或工具失败、仍运行的节点、缺失 Session、截断和损坏的记录均保留证据缺口。业务已经完成但观测不完整，
仍分别展示业务状态和 Issues，不能用一个状态掩盖另一个。

## 1. Review 业务日志与局部 Session

`internal/recording.Review` 只管理业务元数据、产物、scope 计数和录制文件生命周期，
不保留全局对话历史。Review 不支持恢复中断的整体执行；局部 loop 的 Session 能力归 AgentGo。
同一 R2 Lane 保留上下文的连续 Execution 复用一个 Session，每次调用 loop 创建新的 Run。
探索超时后的收卷也创建新的 Run，避免与失败尝试混淆。

AgentGo Session 使用原生 JSONL 协议，记录 `model_request`、`model_exec_end`、`tool_exec_end`、
消息提交、context projection/compaction 和 run 边界。模型、工具正文只在这里存储。
CCR 的领域消息通过 AgentGo codec 注册内容类型，保留原始证据和当前压缩视图，不修改 Session envelope。
Harness 在 Execution 终态边界保存一次局部 AgentGo checkpoint，供加载对话状态；运行期间按事件记录，
不按 turn 或定时保存完整状态，也不复制全局 Snapshot。辅助规划、定位等单次模型调用同样使用
AgentGo Session 记录请求和结果。

Operation 从业务视角组织 Review → Unit/Lane → R1/R2/Trial（R3），并记录 CCR 拥有的构图、等待等非 loop 工作。
调用 Operation 的 `session_ref` 只有 `session_id`、`run_id`、`execution_id` 和 `attempt`；
Execution Operation 保存 scope、业务 outcome 和局部 Session 文件名。不能只用 provider tool call ID 关联，
因为不同 loop 和不同 Run 可以重复使用同一个 ID。

Viewer 展示 Review 时间线和 Unit Job 列表，按需读取选中 Job 或调用的 Session 正文。
Go 的 `ReadJournal(path)` 只读取业务日志，`ReadJob(path, unitID)` 只打开选中 Unit 引用的 Session；
`ReadReview(path)` 与 Python eval 为完整分析加载整轮证据。
供 Viewer、stats、export、eval 使用的 `llm_request`/`llm_response`/`tool_result` 等对象是查询投影，
不会再落盘成一份全局会话历史。业务 artifact、finding、debrief 仍保存在 Review 元数据文件。

Review 业务日志的 schema 为 **14**，旧版 schema 显式不兼容；AgentGo Session 使用自己的格式版本。读端接受完整前缀后的截断末行并显示缺口，完整行损坏不静默跳过。
局部 Session 缺失或损坏时保留可用 timeline，并标记记录不完整；不能将其解释成没有调用或已成功。
写入失败报告 execution/session 身份，继续排空 loop 事件，不改变业务运行结果。

Scope 是业务工作范围，Execution 是一次 Harness 执行；它可能包含收卷续接的多个框架 Run。
Stage 保存源头起止时间与完成状态。多个 Unit 或 Lane 并发时，duration 不可相加作为 Review wall-clock。
成本从实际请求/响应按调用身份计算，缺失 usage 保持 unknown，不依赖 debrief 是否生成。
Session 与 timeline 都不替代 Forge comment、代码仓和业务事实源；本地正文不自动上传。

### 本地记录保留与清理

CCR 默认保留最近 **7 天**的 Review，并将 `~/.casecodereview/sessions` 中 Review 业务日志、局部 Session 与配套日志的
总量控制在 **5 GiB**。`review/scan` 创建录制前自动清理，录制期间每 10 分钟再次检查；
help、preview、dry-run review 和 Viewer 等读取操作不会触发清理。

淘汰单位是一整个 Review：先删除超期记录，容量仍超限时继续按修改时间从旧到新删除，
同时删除同名 `.log` 和 `.sessions/` 下所有局部 Session。JSONL 不截断、不丢弃开头，保留下来的会话仍可供 Viewer、stats、export 和 eval 完整读取。
需要长期保留的评测样本应复制到独立数据目录，避免受本地历史保留策略影响。

```sh
ccr clean --dry-run                        # 预览可回收容量，不删除记录
ccr clean                                  # 立即按当前策略清理
ccr config set retention.days 7
ccr config set retention.max_mib 5120       # MiB，5120 = 5 GiB
ccr clean --days 14 --max-mib 10240         # 仅本次覆盖配置
ccr clean --test-sessions                   # 独立清理测试录制目录
```

两个配置项中，`0` 表示禁用对应限制；两项均为 `0` 时关闭自动清理。配置文件使用
`"retention": {"days": 7, "max_mib": 5120}`，省略的字段使用默认值。
自动清理只管理 `sessions`；`test-sessions` 需显式选择，配置、规则、评测数据目录均不在清理范围。

运行中的会话通过文件锁保护，最近一小时写入的记录也会保留。因此容量是保留目标，
活跃会话本身过大时允许暂时超限，CCR 会报告未达到目标，而不会截断当前录制。
进程异常退出后锁自动释放，后续清理可以回收这些未完成记录。
没有文件锁的旧版未完成会话默认保留，因为无法判断是否仍在录制；
确认旧版 CCR 进程均已停止后，可用 `ccr clean --include-incomplete --dry-run` 预览，
再去掉 `--dry-run` 清理。该选项仍然保护新版本正在录制的会话和最近一小时的文件。

### 时间与 token 成本归属

`ccr stats <session.jsonl> --format json` 从共同 Review reader 输出整轮成本和各 Stage 的成本。
每个模型调用按原生 Session reference 计一次，再通过对应 Stage 的父子关系归入所属 Execution、Unit/Lane 和评审阶段。
Timeline 尚未落盘时，request/response 仍通过原生调用身份关联；成本列为未归属，并显示录制缺口。
成本读取实际请求及响应记录，Review 2、辅助调用和没有 debrief 的中断运行都参与统计。
Viewer 的全局统计与阶段成本表、ATIF 的 `extra.cost` 提供同一投影，eval 的 comparison/replay 使用相同的请求身份与 usage 口径。

时间同时展示阶段经过时长和扣除直接子阶段区间并集后的自身时长，避免并行子任务相加超过父阶段。
自身时长包含尚未细分的编排或等待，不能直接解释为 CPU 时间。未结束阶段截至最后一条观测计算下界，
保留 running 状态；失败请求的已知耗时也进入统计。定位超时时先沿 Stage 的 outcome/reason/error 和
父子链查找失败或未闭合位置，再区分模型请求、工具、重试等待及编排时间。

Token 保留输入、输出和缓存分量，明确区分服务端报告、缺失报告时的估算和完全未知的 usage。
父阶段包含后代调用的 token，父子行不能再次相加。调用失败或请求尚未返回时，unknown usage
不表示零成本；模型请求内部的 transport attempt 没有独立 usage 时，也不能虚构各次尝试的计费拆分。
工具本身的耗时属于工具 Stage，其结果被后续模型消费的 token 属于对应模型请求。

Session discovery 保留未闭合和末行截断的完整前缀，通过 closed、recording_incomplete 与缺口字段供实验筛选。
缺少 session_end 表示终态未知，不能据此断言失败，也不能把这类样本悄悄移出运行健康统计。

### 两次运行的可比覆盖

Runner 在选择和分组前写入 `review_input`，记录协议版本、仓库身份与捕获改动材料的摘要；
`review_unit.targets` 保存 Fragment 前后两侧的实际编辑区间，Finding 与 Hypothesis 保存原代码片段、
old/new 侧和原路径。这些是比较所需的生产事实，是否可比、如何匹配和分类由 eval 决定。

两次 Session 的比较先检查输入，再以目标编辑区间对齐完成证据，不用 Unit ID 或文件名代替覆盖。
Unit debrief 完成探索，并不代表其全部 Hypothesis 已完成复核；eval 必须继续检查 Assessment/Trial。
超时前已经提交的有效 Assessment 继续参与判断；没有提交就是未评估，系统兜底不能充当判断证据。
缺少记录、超时或范围变化时保留 incomplete / unknown，不把没有交付 Finding 解释为修复。

现有实验入口 `eval/benchmark/replay.py` 对相同 repeat 的两臂生成问题、覆盖、阶段去向和成本对比；
`eval/benchmark/session_compare.py` 可离线消费两份原始 Session，包括没有完成 ATIF export 的异常运行。
比较结果是实验观测，不代替人工真值标签；使用方式及匹配限制见 `eval/README.md`。

### 整轮时间线

Review 拥有自己的 Operation journal；Context 只传播 Review/Operation ID，录制处显式指定 ParentID。
`internal/recording/operation` 记录 CCR 自己的业务动作，不安装全局 Timeline Manager，也不接管上游库的内部 span。
Review 负责 Start/Finish；Runner 与 Harness 记录业务阶段和模型／工具调用边界。

```text
Review timeline
  ├─ diff.load / project.select / codegraph.build / unit.formation
  ├─ unit.queue → review.unit → execution → turn
  │                              ├─ context.project / context.recover_overflow
  │                              ├─ model.attempt（一次模型调用，关联 Session Request / Response）
  │                              └─ tool.queue → tool.execution → tool.invoke
  ├─ lane.queue → review.hypothesis → execution → turn …
  └─ trial.assess / trial.finalize
```

图中箭头表达流转，实际父子关系由 stage ID 决定。Review 2 可在 Review 1 尚未结束时启动；两个 Review
阶段和多个 Unit 可以重叠，不存在为了绘图而新增的全局阶段屏障。CodeGraph 在第一次真正构建时记录，
后续查询复用同一图，不把缓存命中再算成构图。

`unit_grouping` artifact 通过 Stage ID 关联每个归拢阶段，保留输入／输出组数、namespace 合并依据（snapshot、Node ID 与 CodeGraph 原生证明路径）、
预算阻止的候选数量与缺少共同 namespace 的组数。`unit_formation.grouping` 和 dry-run JSON 的
`grouping` 提供整轮数量软上限、策略步骤与 `limit_exceeded`；超限表示当前事实与预算下无法满足数量上限，
所有目标仍进入评审。耗时读取对应 timeline stage。

每次 Begin/End 把一个 Operation 的完整当前状态同步追加到 Review JSONL，与业务产物共用序号、写锁和错误边界。
读端按 ID 折叠增量状态；不反复写入全量 Snapshot。并发动作只由 ParentID 建立关系，序号仅表示存储顺序。
取消仍写入已知终态；写入失败后停止追加，避免在半条 JSON 后继续污染文件。Review 结束后释放运行时索引。
进程中断前未观察到的结束事件仍保持缺失，不补造成功。

模型调用是业务 Timeline 的观测边界，不录制 routing/provider/HTTP/连接池等传输子阶段。网络阶段仅在调用失败时进入错误诊断。Viewer 默认展示实际 Prompt、Response、状态、耗时与 Session 记录的 token/cache 用量；金额 Cost 仅在 Session 有计费数据时展示，缺失不解释为零。原始协议字段折叠在详情中。

Timeline 不保存 prompt、response 或工具结果。Stage 的原生 `attributes` 保存业务身份和 Session reference。
框架调用 ID 以局部 Run 为命名空间，并带 Attempt；重试、收卷和连续 Lane 执行不会混淆。
Middleware 把 stage 身份传入真实调用，Event.Timestamp 是源头时间，消费者延迟不算模型或工具耗时。

JSONL 的 `elapsed_ms` 仍表示记录落盘顺序，Operation 的源头时间表示动作发生时间。源头区间应通过
原生 `Operation.Duration` 解读，不能假设每条 stage 都带有显式 `elapsed_ns`。嵌套和并行区间不能相加作为
整轮 wall-clock time；HTTP 等待首字节也不能直接解释为服务端排队。

请求结果由请求调用方决定：fallback 成功可使请求成功，同时保留失败的 provider attempt。Review
终态表示 Runner 是否正常返回，不替代领域 coverage、Execution outcome 或 Trial 结论。缺失结束事实的
stage 保留 running，Viewer 明确显示 incomplete；即使整轮已返回，也不补造子阶段成功。取消后尽力刷新
已经收到的事实；AgentGo 取消时未投递的事件和进程异常退出造成的缺口不能推断成成功。

Viewer 的 Run Timeline 展示共享起点、层级、重叠和终态，请求卡片只投影同一 Snapshot 的请求子树。
ATIF export 将完整原生 Snapshot 放在根 trajectory 的 `extra.operations`，不按请求复制多份记录。
每个 Execution 单独投影为 subagent trajectory，并保留所属 scope，避免同一 Lane 的多次 loop 相互覆盖。
Session schema 变更时同步 recorder、Viewer、export 和 fixture；历史文件需要分析时使用离线迁移。

### 流式交付不是 Session tail

Session JSONL 是本地执行事实源，不是对外发布协议。需要在长时间 review 中尽早消费成熟 Finding 的调用方，
使用 `ccr review --format jsonl` 读取 stdout 事件：`run_started` 建立运行身份，`finding` 只在 Trial、
行号定位、身份标记和 Session 持久化完成后出现，`run_finished` 携带与普通 JSON 模式相同的最终结果。
调用方不应 tail Session 文件推断交付，因为其中还包含未通过 Trial 的中间事实和可能变化的内部记录。

## 2. Viewer：单次运行的人工诊断

Viewer 读取业务 timeline 与关联的 AgentGo Session，把事件组织成人容易检查的页面，主要回答：一次 review 花费在哪里、模型
看到了什么、loop 如何推进、最终决策如何形成，以及哪里发生中断或空转。

首页按开始时间倒序列出 Review timeline，Repository 作为筛选条件。打开一次 timeline 后查看 Stage 的时间、属性与错误；Stage 详情展示关联的 Session ID，并直接展示对应模型与工具调用记录。页面同时列出 Unit Job；点击可查看跨 R1/R2/Trial 的 Operation 和原生调用，并通过同页的 JobView JSON 链接读取标准 Observation。
Timeline 用准备、R1 / R2 loop、R3 裁决与 Unit pipeline 泳道展示共享时间轴上的执行区间。同一 loop 的多次执行保留空隙，并发区间保持重叠；子阶段依据记录的 ParentID 在所属泳道内逐层展开，turn 下的模型、工具与上下文操作保留真实层级；Unit pipeline 标注 unit_id，其时间包含下属评审阶段，不应与 R1/R2 耗时相加。点击 Stage 弹框展示时间、attributes、错误、子阶段与关联 Session，详情不参与泳道列宽。模型 Stage 弹框展示 Session 记录的 Request / Response（包含 reasoning、tool calls 与 usage），工具 Stage 展示实际参数与最终结果；两者均按需读取关联 Session，按 session/run/execution/attempt 完整身份匹配；正文不写回 Timeline，缺失输入或输出明确标注。
未记录结束时间的阶段只显示起点标记和 incomplete，不向当前时间延长，也不补造耗时或成功状态。
请求卡片复用同一展示方式，时间轴相对该请求起点。

Viewer 不再提供独立的 Scope / Execution 详情页；分析从 Timeline 进入具体 Stage，Session ID 保留为记录关联身份。

每次 `llm_request` 直接投影为一个 **Prompt Snapshot**，展示那一轮实际发送给 provider 的完整消息，
而不是由上一轮 response 和 tool result 反推 conversation。`context_compacted` 则按发生顺序插入同一条
Conversation，直接展示次数、原因和前后比例；相邻 snapshot 的 token/message delta 只保留为辅助诊断值。

Viewer 可以增加简单、确定、便于人发现问题的数据统计，例如重复读取、context 与 `read_files` 路径重合、
空搜索和未完成 Execution 数。这些数据是诊断线索，不是效果分数。Viewer 不负责实验编排，不回写
Session 或 Trial 结果，也不能因为“0 Finding”就把 partial run 展示为 clean。

Viewer 只消费当前 Session schema。协议变化时应同步 recorder、fixture 和投影，不为旧记录叠加字段猜测、
终态推断或页面兼容分支；历史分析需要时由一次性的离线迁移完成。

单次运行容易受 diff、模型路由、缓存、网络和上下文差异影响。Viewer 更像显微镜：适合发现问题、查看
prompt 和形成改进假设，不适合凭少量 session 断言整体效果提升。

## 3. eval：效果与轨迹闭环

`eval/benchmark` 对固定数据集运行正常 Review 1/2/3，分别匹配全部 Hypothesis、有效的
supported + caused Assessment 对应 claim，以及最终 Finding。Review 2 的视图保留 low_value，
用于区分发现/复核能力与产品交付策略。参考标注和独立语义匹配是效果判据，Assessment 本身是被评测输出。
负标签标记错误评论，不表示整个变更 clean；未匹配到参考的新结论也不能自动判为误报。

效果分析输出待检查 case，保留 dataset/case/run/arm、Session 内容身份、Hypothesis/Unit ID、
三个阶段的产物与选择原因。`eval/trajectory` 消费同一次运行的 Session/ATIF，解释工具行为、
上下文、阶段流转与成本，不重新运行评审。Review 3 的正常低价值过滤属于可解释的策略差异，
不能自动列为待修复缺陷。所有未完成运行、缺失证据和未判定的匹配都保持显式。

两者共享 Python 环境、Session 读取和快照身份；周报组合效果与轨迹的持久化结果，同时观察：

- **效果**：参考问题命中、已知错误评论复现，以及 Assessment/Trial 的阶段去向；
- **健壮性**：Unit/Lane 是否完成、Hypothesis 是否全部 Assessment、超时、partial 和执行错误；
- **成本**：Measurer 记录的 token、时间、模型轮次、工具调用和 Unit 数量。

Detector 只形成可定位的 Finding，Verifier 按明确契约形成可选 verdict 和/或 score；Measurer
只记录可计数、求和的事实，不判断质量。eval 在同一 Dataset/Cohort 上组合三者，例如比较
completion、人工接受率和
`tokens / labeled accepted Finding`。单位成本必须与人工 label coverage 一起解释，不能把低 token
本身当作效果提升。

Dataset snapshot 把带时区的采集窗口、源快照身份与规范化产物摘要绑定为一次可验证构建。只有采集
完整覆盖目标窗口、规范化数据集消费了当前源快照且产物摘要一致时，周报才开放 Finding 质量指标；
否则执行指标照常生成，质量指标保持不可用，不能把缺失数据解释成零质量或零 Finding。

Viewer 中发现的重复 `read_files` 或搜索空转可以进一步沉淀为 Trajectory Detector；未完成 Unit
等明确契约由 Verifier 判断，人工确认的 Finding 则沉淀为 label 和固定数据集。只有在对照实验中
确认问题具有普遍性、指标改善且没有召回或成本回退，才能认为优化有效。

### 3.1 已知问题的阶段归因

一个已确认需要交付的问题没有形成 Finding，不能直接说明应该改 prompt。问题可能在 Formation
时没有进入正确 Unit，也可能在 Unit Review、Hypothesis Review、Trial 或最终持久化时丢失；相关
Execution 没有完成时，领域阶段甚至没有产生可判定结果。eval 因此把外部确认的已知问题与 Session
事实连接，寻找它在评审漏斗中到达的最深位置：

```text
Expected Issue
      │
      ▼
Formation ─▶ Unit Review ─▶ Hypothesis Review ─▶ Trial ─▶ Finding
      │             │                 │              │          │
   Unit scope    Hypothesis       Assessment      decision   delivered
                    └──────── Execution terminal state ────────┘
```

归因遵循以下顺序：

1. 已有匹配 Finding 时结果为 `delivered`；
2. 完整 Session 中没有 Unit 覆盖问题位置时归到 `formation`；
3. 相关 Unit 已完成但没有匹配 Hypothesis 时归到 `unit_review`；
4. 匹配 Hypothesis 没有 Assessment，或 Assessment 在 support、attribution、value、novelty 任一轴拒绝
   交付时归到 `hypothesis_review`；
5. Review 2 支持该问题，但确定性 Trial 没有批准交付时归到 `trial`；
6. Trial 已批准但 Session 没有持久化匹配 Finding 时归到 `finding`；
7. 相关 Unit 或 Lane 缺少完成终态时优先归到 `execution`，避免把运行中断误判为领域阶段漏报。

问题与阶段产物优先通过 Hypothesis ID / fingerprint 连接；缺少稳定身份时才使用 `path + line`。
归因器不使用 LLM 判断两段自然语言是否表达同一缺陷，因为语义近似会把不可复现的 judge 偏差带入
根因定位。匹配方式与参与判断的 Unit、Hypothesis、Assessment、Trial decision 和 Execution outcome
必须随归因结果保留，供人复查。

阶段归因是**定位信号**，不是根因证明：`formation` 不等于语言解析器必然有错，
`hypothesis_review` 也不等于 Review 2 prompt 必然有错。它只把调查范围收敛到拥有该阶段判断的模块；
具体修改仍需结合对应 artifact、trajectory 和代码事实求证，再通过固定 corpus 对照实验验证。归因输入、
命令和 JSONL 输出协议见 [`eval/README.md`](../eval/README.md)。

eval 的标签协议、数据边界、数据集构建、Trajectory 诊断、固定 corpus 与重放命令不在本文展开，统一见
[`eval/README.md`](../eval/README.md)。真实 corpus、labels、datasets 和 trajectory 放在被忽略的
`eval/data/`；公开仓只提交通用工具、匿名 fixture 与方法说明。

## References

- [`harness.md`](harness.md)——Session recorder、Execution 生命周期和 Viewer 投影契约
- [`../eval/README.md`](../eval/README.md)——效果评估的采集、数据集、Verifier 与实验流程
- [`unit_review.md`](unit_review.md)——Review 1 的效果、上下文与完成契约
- [`hypothesis_review.md`](hypothesis_review.md)——Review 2 的 Assessment、Lane 与 Trial gate


### Stage views

Stage 详情共享同一 Stage 身份、状态和时间，使用明确的视图投影：所有 Stage 提供 Default，
模型调用额外提供 Model，Loop execution 额外提供 Agent。Default 展示阶段事实和子阶段；Model
按完整 Session/run/execution/attempt 身份读取 Request、Response 和 usage；Agent 按真实 Turn
父子关系组织调用摘要，沿用 Default 的树遍历与逐级缩进。左侧为可折叠执行树，右侧展示选中节点详情，模型 Request/Response 与工具 Input/Output 按需加载，不离开 Agent 视图。重试独立显示，缺失 usage 不记为零。
多个 run 重启 Turn 编号时仍按不同的 Turn Stage 区分。模型节点高度使用同一 Loop 内记录的 total_tokens 线性刻度（48px 最小高度 + 最多 96px 增量），宽度随树深度缩进；未知 usage 明确标注。Turn/Tool 保持紧凑高度，不重复计入父子 token。
详情保留输入、输出、缓存和 total 数值，缓存不再次累加。

视图只在读取时投影，不写入 Timeline 或 Session。Agent 接口只返回摘要，调用正文在点击后按需读取。关联 Session 读取失败时保留阶段树和已读取的 usage，并显示文件/记录级诊断；事件内容解码失败只跳过对应记录，无法继续解析的 JSONL 保留已读部分，其余 usage 保持未知。
在弹框内下钻后返回会恢复原视图与滚动位置；切换视图不改变观测范围，也不重新执行 review。

## Review completion and on-demand views

The Unit pipeline owns the terminal result of each admitted Unit, including
unassessed Hypothesis IDs. The Review manifest seals these results in
`session_end.completion`, separately from delivered Findings and recording gaps.
CLI, Viewer and eval consume this same result. Warnings explain individual
failures; their names and counts do not determine completion. Budget-skipped or
unfinished Units make coverage partial, even when useful Findings were delivered.

The Timeline page reads business records and stages. Final token totals come from
bounded call accounting in the manifest; before finalization, the page leaves
those totals pending. Model and Tool views load the referenced local Session,
and Agent view loads the Sessions referenced by the selected loop. Corrupt call
bodies are diagnosed in those views without preventing Timeline navigation.

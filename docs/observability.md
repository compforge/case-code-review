# 可观测性

CCR 的可观测性建立在同一条链路上：Session JSONL 持久化执行事实，Viewer 帮助人理解一次运行，
eval 使用这些事实和外部标注判断版本效果。三者依次是基础、诊断投影和效果验证，不能互相替代。

```text
Review Execution
      │
      ▼
Session JSONL                 # 基础：实际发生了什么
      │
      ├──────────▶ Viewer     # 诊断：这一次为什么会这样
      │
      └──────────▶ eval       # 验证：改动是否稳定提升效果
```

可观测性从三条互补的轴理解同一次 review：

| 观察轴 | 主链路 | 回答的问题 |
|---|---|---|
| **成本轴** | `Run → Unit → Execution` | 整轮、一个行为范围和一次 agent loop 分别消耗了多少时间、token 与工具调用 |
| **决策轴** | `Unit → Hypothesis → Assessment → Finding` | 一个问题主张如何产生、被复核并最终通过交付门禁 |
| **时间轴** | `Formation → Review 1 → Queue → Review 2 → Review 3` | 一条评审路径何时形成、执行和等待，最终多久变成可交付建议 |

三条轴共享 Unit、Hypothesis 和 Execution 等稳定身份，但不能互相替代。并发 Unit 的 Execution
duration 不能直接相加成 Run 的 wall-clock time；Lane 排队也不是模型执行成本，却是 Finding 交付时延的一部分。
因此 Session 先保存可 join 的原始事实，Viewer、eval 和外部 comment 再按各自问题生成投影。

## 1. Session JSONL：共同事实源

Session JSONL 是持久执行事实，timeline 是其中的执行结构，不是另一份旁路计时日志。
Stage 记录一次动作的身份、父子关系、源头时间、状态；内容记录保存请求、响应和结果，并引用产生它的 Stage。

```text
Session JSONL
  ├─ session_start / session_end     # 输入身份、配置与最终汇总
  ├─ timeline_snapshot              # 关键边界读取的完整 timeline.Snapshot
  │    └─ execution → model.attempt → llm.request
  │                 → tool.execution → tool.invoke
  └─ 内容记录 ── stage_id ──▶ Stage
       ├─ llm_request / llm_response / llm_error
       ├─ tool_result / context_projected / context_compacted
       └─ artifact / finding
```

Scope 是领域工作范围，一个 Lane 可以包含多次 Execution；Execution 是一次真实 AgentGo loop，
其 `execution_id` 同时是对应 Stage 的 ID。Execution Stage 的最终 attributes 持有 outcome、reason、
turn/tool 统计，Stage 本身持有起止时间。不存在另一套 `execution_start` / `execution_end` 完成事实；
Viewer、export 和 eval 都不从最后一条 assistant 文本、终态工具或 Scope debrief 猜测 loop 是否完成。

所有记录共用写入入口：`uuid` 标识记录，`seq` 表示追加顺序，`timestamp` / `elapsed_ms` 表示落盘时间。
追加相邻不代表父子关系，不写 `parentUuid`；执行父子关系只来自 Stage 的 `parent_id`。
`timeline_id` / `stage_id` 连接内容与执行，模型响应引用请求 Stage，工具结果还通过 `request_id` 和
`tool_call_id` 连接发起它的模型请求与调用。并发返回不依赖相邻记录、工具名或返回顺序配对。
Stage 可能在内容记录之后刷新落盘，读取时先选择最新的完整 Snapshot，再按 ID 连接。

请求、工具、Execution 和 Formation 的耗时从对应 Stage 推导，不在内容记录中再存一份计时。
`session_end` 和 debrief 保留领域汇总：前者的 duration 来自 Operation，后者的 duration 是模型调用耗时之和，
不代表 Unit 的墙钟区间。Unit/Hypothesis/Assessment/Trial/Finding 保留业务身份和内容，通过 Stage 引用
连接产生它们的工作；`hypothesis_review_execution` 只保存 Hypothesis 与 Execution 的关联，不复制完成结论。

事实还包括实际发送的 prompt、response/reasoning、stop reason、usage、工具参数及结果、每次模型调用前
可见的 ContextItem，以及 context compaction 的原因、提交状态和前后规模。模型预算拒绝使用
`policy/admission` 错误详情，不累加 provider 失败计数。首次 context projection 是 Initial Context exposure，
并不由模型请求快照反推。

Go 消费方共用 Session reader：读取最后一份完整 timeline 快照，接受完整前缀后的截断末行并显式标记缺口；
完整行损坏则报错。写入失败会报告并停止继续追加，避免其后的记录看似连续。Python eval 同样检查输入完整性，
零值结束时间表示未结束，缺少结束事实不能当成零耗时或成功；损坏数据不参与完整覆盖判定。

Session 只说明“发生了什么”，不直接说明“效果好不好”。它也不替代 Forge comment、代码仓和业务事实源。
Session 可能包含源码、prompt 与工具结果，应默认作为本地敏感数据处理，不自动上传。

### 时间与 token 成本归属

`ccr stats <session.jsonl> --format json` 从共同 Session reader 输出整轮成本和各 Stage 的成本。
每个模型调用按请求 Stage ID 计一次，再沿父子关系归入所属 Execution、Unit/Lane 和评审阶段。
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

Session 拥有一个 `go-stdx/timeline` ID：进程安装共用的 NoopStore Manager，各层通过根包入口记录，
单机运行省略 Actor，应用退出时在生产者结束后关闭 Manager。Context 只传播 timeline / stage ID，
录制处显式指定 ParentID；调用 repocli 时使用同一 StageRef，使库阶段归入调用方阶段。
从创建 Session 到 Runner 收尾，diff 加载、项目选择、CodeGraph
构建、Unit formation、Unit 等待与 Review 1、Lane 等待与 Review 2、Trial 以及辅助模型请求都向其贡献
阶段。Runner 拥有评审阶段；Harness 拥有 Execution 和 AgentGo 事件适配；模型客户端拥有路由、fallback
与 HTTP 阶段。子阶段只开始和结束自己的工作，Session 唯一负责整轮 Start / Finish。

```text
Session timeline
  ├─ diff.load / project.select / codegraph.build / unit.formation
  │                                                └─ unit.grouping.relations / unit.grouping.namespace
  ├─ unit.queue → review.unit → execution → turn
  │                              ├─ context.project / context.recover_overflow
  │                              ├─ model.attempt → llm.request → routing / provider → HTTP phases
  │                              ├─ retry.wait
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

Session 在请求和 Execution 的开始、结束边界及每秒定时调用 `timeline.Read(ctx, id, false)`，由 CCR 的 JSONL writer 写入完整
`timeline_snapshot`；读取与追加共用 writer 锁，避免并发导出把旧快照排在新快照之后。
HTTP 回调和 AgentGo 事件只记录内存事实，不触发文件写入。定时导出让长时间等待可以被观察，
突然退出可能丢失上次成功 checkpoint 之后的事实。取消请求后仍可读取内存事实；
整轮结束时先停止定时导出，Finish 后写入最终快照和 session_end，再释放该 ID 的缓存。
timeline 只负责缓存中的录制和查询，JSONL 的格式、时机和文件 IO 全部由 CCR 持有，无需调用 Flush。
读取端保留最新的完整 timeline 快照和内容记录，不保留各次 checkpoint 的重复完整副本。
快照属于整轮 Session；Unit、Lane、
Hypothesis、CCR Execution 和请求身份保存在对应 stage attributes。AgentGo 的逻辑执行 ID 以 CCR Execution
为命名空间，物理尝试另带 Attempt，因此并发 loop 即使发出同名 tool call 也不碰撞。Middleware 把 stage
身份传入真实调用，Event 以源头 Timestamp 记录开始和结束；消费者处理事件的延迟不算作模型或工具耗时。

属性统一写入原生 `attributes`。Schema 12 的 Go Session reader 与 Python eval 均按追加顺序
选择最后一份完整 Snapshot，不再依赖持久化修订号。旧 schema 显式报告不兼容。

JSONL 的 `elapsed_ms` 仍表示记录落盘顺序，timeline 内的源码时间表示动作发生时间。源头区间应通过
原生 `Stage.Duration` 解读，不能假设每条 stage 都带有显式 `elapsed_ns`。嵌套和并行区间不能相加作为
整轮 wall-clock time；HTTP 等待首字节也不能直接解释为服务端排队。

请求结果由请求调用方决定：fallback 成功可使请求成功，同时保留失败的 provider attempt。Session
终态表示 Runner 是否正常返回，不替代领域 coverage、Execution outcome 或 Trial 结论。缺失结束事实的
stage 保留 running，Viewer 明确显示 incomplete；即使整轮已返回，也不补造子阶段成功。取消后尽力刷新
已经收到的事实；AgentGo 取消时未投递的事件和进程异常退出造成的缺口不能推断成成功。

Viewer 的 Run Timeline 展示共享起点、层级、重叠和终态，请求卡片只投影同一 Snapshot 的请求子树。
ATIF export 将完整原生 Snapshot 放在根 trajectory 的 `extra.timeline`，不按请求复制多份记录。
每个 Execution 单独投影为 subagent trajectory，并保留所属 scope，避免同一 Lane 的多次 loop 相互覆盖。
Session schema 变更时同步 recorder、Viewer、export 和 fixture；历史文件需要分析时使用离线迁移。

### 流式交付不是 Session tail

Session JSONL 是本地执行事实源，不是对外发布协议。需要在长时间 review 中尽早消费成熟 Finding 的调用方，
使用 `ccr review --format jsonl` 读取 stdout 事件：`run_started` 建立运行身份，`finding` 只在 Trial、
行号定位、身份标记和 Session 持久化完成后出现，`run_finished` 携带与普通 JSON 模式相同的最终结果。
调用方不应 tail Session 文件推断交付，因为其中还包含未通过 Trial 的中间事实和可能变化的内部记录。

## 2. Viewer：单次运行的人工诊断

Viewer 读取 Session JSONL，把事件组织成人容易检查的页面，主要回答：一次 review 花费在哪里、模型
看到了什么、loop 如何推进、最终决策如何形成，以及哪里发生中断或空转。

Session 页面通过 Session / Timeline 两个 tab 分开呈现概览与计时，避免大型阶段树挤占审查入口。
Timeline 使用可折叠阶段树和共享时间轴；attributes、错误与阶段身份按需展开，不参与主行列宽。
未记录结束时间的阶段只显示起点标记和 incomplete，不向当前时间延长，也不补造耗时或成功状态。
请求卡片复用同一展示方式，时间轴相对该请求起点。

Viewer 保留两个互补层级：

1. **Session Overview**：token、时间、模型、工具调用、完成状态，以及
   `Diff Files → Review Files → Unit → Hypothesis → Assessment → Finding` 漏斗；
2. **Scope / Execution**：Scope 页面先展示 Unit 或 Lane 的聚合数据和本地 Decision Trail，再把每个
   Execution 独立展开为 prompt、response/reasoning 与 tool 参数/结果时间线。

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

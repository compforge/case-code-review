# Harness：有界、可观测的 Agent 执行层

## 1. 理念 / 概念

Harness 只解决一件事：**让一次 agent execution 在明确输入、能力、预算和完成契约下可靠运行**。
它不理解 Unit、Hypothesis 或 Finding，也不决定某条结论是否值得发布。

```text
ExecutionSpec ──▶ Execution.Run ──▶ ExecutionResult
                      │
                      ├─ ContextManager
                      ├─ Tool / Hook adapters
                      ├─ Recorder
                      └─ AgentGo loop
                              │
                              └─ events + session JSONL
```

| 对象 | 责任 |
|---|---|
| `ExecutionSpec` | 一次执行的输入消息、工具、hook、预算和 scope |
| `Execution` | 单次运行的聚合根，持有 ContextManager、Recorder、完成状态与 AgentGo loop 生命周期 |
| `ContextManager` | 注入、去重、淘汰、压缩和投影上下文 |
| `Tool Registry` | 把工具定义、provider 与执行身份绑定 |
| `Hook / Event` | 提供领域扩展点和稳定观测事件 |
| `ExecutionResult` | 返回完成状态、usage、工具统计和错误；内部可携带后续 Execution 的不透明 continuation |
| `Session` | 以稳定 JSONL 持久化实际发生的执行事实 |

调用方可以用 `biz_id` 给整次 CCR 执行附加不透明业务身份。Harness 只在 `session_start` 持久化，
Viewer 只展示；它不解释格式、不改变评审行为，也不进入模型 prompt。需求内容仍通过 `background` 提供。

Runner 可以用 Harness 执行 Unit Review 或 Hypothesis Review；Harness 不反向 import Runner、Unit、
Assessment 等评审领域对象。领域语义通过消息、工具、hook 和 event 适配进入。

## 2. 流程

### 2.1 启动 Execution

调用方组装 `ExecutionSpec`，其中 `Messages` 是领域消息而不是提前摊平的 provider 文本；它同时明确
本次执行允许看到和做到什么，再通过稳定边界启动：

```go
execution, err := harness.NewExecution(spec)
result, err := execution.Run(ctx)
```

`Execution` 在构造时接管输入快照并组装 AgentGo model、tool 和 context 契约。它是单次使用的
运行实体；一次 Execution 的 Recorder、ContextManager、完成状态和其它运行事实不能被另一个 scope
复用。需要延续 conversation 时，调用方只把前一个 `ExecutionResult` 作为 `ContinueFrom` 交给新
`ExecutionSpec`；AgentGo message context 仍封装在 Harness 内。Review 2 Lane 用这一入口串行复核相关 Hypothesis。

### 2.2 每轮模型与工具循环

每轮按以下顺序推进：

1. ContextManager 根据当前状态生成模型可见消息；
2. recorder 记录实际发送给模型的 request；
3. model adapter 调用模型并记录原始 response、usage 和 stop reason；
4. tool call 经 Registry 找到 provider，经 hook 校验和执行；
5. tool result 进入 transcript，同时发出稳定事件；
6. completion contract 判断是否结束、强制收敛或继续下一轮。

预算耗尽、deadline、模型错误和缺少终态动作必须形成不同 completion 状态。调用方需要知道“没有
finding”究竟是审完了，还是执行没完成。

### 2.3 返回结果

Execution 结束后返回结构化 ExecutionResult，不直接生成 ReviewResult。Runner 将其解释成 Hypothesis、
Assessment 或 warning；任何领域后处理都发生在 Harness 外。

## 3. 关键设计

### 3.1 Typed message 是内部语义，wire message 是边界投影

Harness 内部消息保留文本、文件、来源、范围、优先级和可重取性等语义，并直接实现 AgentGo 的
`AgentMessage` 契约。模型返回的普通消息先进入
`Raw`，可识别的工具结果（例如 `read_files`）会重新提升为 `File` / `FileBatch`，因此后续压缩始终从 typed message
视角出发。只有在调用模型前才降成 provider wire message：

```text
msg.Msg / msg.File
   ── context lifecycle ──▶ lowered model messages
```

CCR 消息仍通过 `ToLLM(CompactionLevel)` 定义 full、condensed、reference 等领域投影，Harness adapter
把它映射为 AgentGo 的 `Raw / Compact / ToMessage`；只有 AgentGo 发起模型调用时才执行 `ToMessage`。
Harness 外只提供完整消息，不选择压缩等级。文件内容不是“碰巧放在一段字符串里的文本”。保留类型后，
ContextManager 才能判断两个范围是否
重叠、后一次完整读取是否覆盖前一次局部读取，以及 token 压力下哪些内容可以先淘汰后按需重取。

工具结果通过其前一条 assistant tool call 的 call ID 找回工具名和参数，再由统一 `FromLLM` 入口归入
少量语义类型，而不是“一种工具一个 class”。每种双向消息把 `FromLLM` 与 `ToLLM` 放在同一处，
修改 wire contract 或压缩投影时可以同时核对两个方向：

- `read_files` / `read_base_files` → `FileBatch`（内部保留各个 `File`），current 与 baseline snapshot
  参与身份，不能跨版本去重；一次 tool call 仍只对应一条 tool result；
- `read_diffs` → `Diff`，压缩时保留 path 与 hunk anchor；
- `search_code` / `file_find` → `SearchResult`，保留 query、命中位置或无命中反证；
- 结果提交、终态和可恢复错误 → `ToolReceipt`，领域 artifact 仍只由 Runner collector 持有。

无法识别的普通 LLM 消息退化为 `Raw`。初始任务、试验性的 Board 等只由 CCR 内部创建的单向消息没有可恢复的
wire 来源，因此只定义 `ToLLM`。

降低边界应保持可解释的顺序和一对一关系，避免 adapter 再暗中合并消息。Session 记录的是最终实际
发送的 wire shape，因此 Viewer 能回答“模型当时究竟看到了什么”。

`read_files` 只有批量 `reads[]` 契约，单文件也使用一个元素的数组；已经确定且彼此独立的范围由
provider 并行读取，并按请求顺序装回同一条结果。ContextManager 可以只剔除其中已覆盖的成员，执行
剩余成员后再按原顺序合并，因此批量不会削弱范围复用，也不需要保留另一套单文件入口。

### 3.2 上下文生命周期统一在 ContextManager

上下文不是只增不减的聊天数组。Harness 统一处理：

- 注入：system/task、静态源码消息、跨 turn provider 输出；
- 去重：后一次覆盖读取替代早期重复 file content；
- 复用：当 `read_files` 请求范围仍完整可见时返回轻量提示，不再次执行相同读取；
- 淘汰：优先移除可重取、低价值的大块内容；
- 压缩：只在轻量手段不足时进行有损总结；
- 投影：临近调用时降成模型可见消息。

ContextManager 默认从完整消息开始，只有预算趋紧才通过 AgentGo Compactor 单调降低消息 fidelity，
再做通用 tool-result trim 和 summary。默认先处理低优先级消息，同一优先级从尾部向前压，够用即停；
压缩一旦提交不再展开，从而尽量保住 provider prompt cache 的公共前缀。领域层可以决定“哪类事实值得提供”，
但不能各自实现一套 transcript 修剪，否则实际 prompt、成本
统计和恢复行为会分裂。

### 3.3 预算是机制，完成策略属于调用方

Harness 提供 token、tool round、deadline 等预算机制，并通过 AgentGo `BeforeTurn` 在模型调用前处理
增量上下文与“接近边界”的 wrap-up。进入 wrap-up 时
工具 schema 保持不变，只在消息尾部追加稳定的收敛指令；若调用方配置硬门，middleware 拒绝继续
执行调查工具，但结果/完成工具仍可用。模型忽略硬门时只再获得一次明确的 completion 修正轮；仍不
提交终态则以 truncated 结束，而不是用相同拒绝结果耗尽剩余轮次。这样既不能靠忽略 prompt 继续
空转，也不因中途换工具集破坏 provider cache。调用方定义终态
动作和收敛语义。例如 Unit Review 可在硬门后只允许提交 Hypothesis，Hypothesis Review 可要求每个
输入都有 Assessment。

Harness 不能把 `task_done` 统一解释为领域完成；它只执行调用方给出的 completion contract。超时或
轮次耗尽时返回 partial/incomplete，不把空输出包装成成功。

### 3.4 Tool 与 Hook 是执行能力，不是领域所有权

工具定义、参数解析、provider 调用和通用 telemetry 位于 Harness。某个工具是否在一个阶段可见、
调用后产生何种领域 artifact，由 Runner 的 execution spec / hook 决定。

这允许同一个 `read_files` 被多个流程复用，也允许 Review 2 只暴露只读证据工具而不暴露发布 Finding
的能力。Runner 适配可以依赖 Harness，Harness 不依赖 Runner。

### 3.5 AgentGo 是内核依赖，不是项目领域模型

AgentGo 负责模型循环和通用上下文机制。CCR 在 Harness 边界将自身 typed message、tool provider、
hook 和事件适配进去，并把 AgentGo response 转回稳定 ExecutionResult。

AgentGo 类型不应泄漏到 Runner、Session Viewer 或 Unit 模型。这样替换执行内核、保留旧 `llmloop`
作参考或并行演进 Viewer，都不会迫使评审领域一起迁移。

## 4. 可观测性：Session JSONL 与 HTML Viewer

一次昂贵或异常的 review 必须能回答三类问题：整体花费在哪里、每个 loop 如何演进、最终决策为何
产生。仅打印终端摘要不够，Harness 因而把实际执行持续写入 Session JSONL。

### 4.1 Session JSONL 是事实源

Session 使用追加式事件记录，不要求运行结束后才能生成完整对象。它按
`Session → Scope(Unit/Lane) → Execution` 组织：Scope 表示领域工作范围，Execution 表示一次真实
AgentGo loop。一个 Lane 可以包含多次连续 Execution，因此两者不能合并成同一层。

每个 Execution 的 `llm_request`、`llm_response`、`tool_call`、`context_compacted` 和
`execution_end` 共享稳定身份。`context_compacted` 记录一次完整上下文改写的原因、提交状态、
前后 token/消息数以及是否产生 summary checkpoint，不泄漏内部 Compactor 步骤。
`execution_end` 是唯一完成事实，持久化 outcome、reason、turn/tool 统计和耗时；Viewer 不从终态工具、
assistant 文本或 Unit debrief 反推 loop 是否完成。领域层仍把 Hypothesis、Lane assignment、Assessment
和 Trial decision 作为 artifact 追加到相应 Scope。

JSONL 的价值不只是“留日志”：它是问题分析、回放、eval 数据连接和版本对比的稳定输入。持久化发生在
Harness recorder 边界，保证记录的是实际 wire 行为，而不是模板渲染前的推测。

Session 仍是本地执行记录，不替代 Forge 上的持久评论、代码仓或业务事实源。跨 CI revision 的
prior delivery 应从 Forge 获取；不能假设上一次容器的 JSONL 仍然存在。

### 4.2 HTML Viewer 是诊断投影

Viewer 只读取稳定 Session JSONL，不读取 AgentGo 内部对象，也不持有执行状态。它提供两个互补层级：

1. **Session Overview**：总 token、时间、模型、工具调用和完成状态，定位成本与吞吐瓶颈；其中
   `Diff Files → Review Files → Unit → Hypothesis → Assessment → Finding` 把两阶段 review 与 Trial
   放回同一条漏斗，并显式标出不完整 Execution、未评估 Hypothesis 和被 Trial 拦截的判断。这样
   “0 Finding” 只有在各阶段完整时才可理解，partial run 不会伪装成 clean；
2. **Scope / Execution**：Scope 页面展示 Unit/Lane 聚合数据与本地 Decision Trail；每个 Execution
   独立展示实际 `llm_request` 形成的 Prompt Snapshot、assistant response/reasoning、tool 参数/结果和
   显式 compaction 节点。Snapshot 不从相邻事件重建；compaction 次数与比例也不再由 prompt delta 猜测。
   Provider
   实际返回 reasoning 时单独展示，不把普通 assistant 文本冒充为隐藏思维过程。

Review 1 页面还分别展示“调用时已被 context 覆盖”的读取与“同路径多次读取”。前者是确定的复用
机会，后者只是可能的探索回环；同时展示预载源码、Unit 静态已知路径和 caller/callee
路径与实际读取文件的重合率，用于判断下一步应预载什么，而不是把所有相关文件都塞进 prompt。

这两个层级分别回答“整次 review 怎么样”和“某个 Scope 中的某次 Execution 为什么这样推进”。
Overview 不能替代逐轮证据，Execution timeline 也不能替代全局统计。

### 4.3 展示层不反向定义协议

JSONL schema 是执行与诊断之间的稳定协议；HTML 只是其中一种投影。新增图表或页面不应迫使 recorder
依赖模板结构，Viewer 也不应回写 session 或修改 Trial 结果。若需要新的诊断能力，先定义稳定事件或
artifact，再让 CLI、eval 和 HTML 分别消费。Viewer 只消费当前 schema，不为历史记录维护字段猜测或
完成状态 fallback；需要保留的历史数据应由离线迁移转成当前协议。

### 4.4 隐私与体积边界

Session 可能包含源码、prompt 和工具结果，应默认按本地敏感数据处理，不自动上传到外部目的地。
体积治理应依靠事件语义、可配置保留和离线汇总，不能为了缩小文件而漏记“模型实际看到了什么”。

## 5. 验证 Harness 的方式

Harness 变更至少验证：

- 相同 typed input 降成稳定、可解释的 wire messages；
- 工具结果与触发它的 model request 正确关联；
- budget/deadline/terminal action 产生正确 completion 状态；
- recorder 能在错误和 partial execution 下写出可读取 JSONL；
- Viewer 的 overview 与 timeline 均由同一批事件推导，不出现统计和逐轮记录矛盾；
- Harness 包保持不依赖 Runner、Unit 和评审领域。

## References

- [`kernel.md`](kernel.md) — Harness 在 CCR Kernel 中的职责
- [`observability.md`](observability.md) — Session JSONL、Viewer 与 eval 的可观测性分工
- [`unit_review.md`](unit_review.md) — Unit Review 如何使用预算、工具与完成契约
- [`hypothesis_review.md`](hypothesis_review.md) — Review 2 的只读证据与 Assessment 完成契约
- [`unit-model.md`](unit-model.md) — Review Messages 所承载的 Unit / Clue 语义

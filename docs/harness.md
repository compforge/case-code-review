# Harness：Execution 驱动的 Agent 执行内核

> 绿地设计。本文展开 `kernel.md` 中 Harness 能力中心的内部模型；Runner 如何形成 Unit、
> 分配总预算和聚合 Finding 见 `run-model.md`，review 消息与上下文证据分别见
> `message-model.md` 和 `context-model.md`。

## 理念 / 概念

Harness 的目标不是提供更多 Agent 形态，而是**可靠执行一次有预算、有上下文管理、有明确
终态的 Agent loop**。在 CCR 中，这让 Runner 能为单个 Unit 提供更多有效上下文，又不会因
context、token 或耗时失控而降低 review 价值。

Harness 的通用性只表示 Core 不理解 review 领域：

- 不知道 Unit、Dossier、Finding、文件 diff 或源码语言；
- 不决定快速/深度模式、Unit admission、调度顺序和最终裁决；
- 不内建某一套 review tool、prompt、压缩摘要或完成信号；
- 只执行调用方注入的消息、工具、策略和限制，并交付可解释的结果。

**Agent 是 Harness 内部实现词，不是独立能力中心。** 对外稳定货币是 Execution：一次
Execution 有不可变输入、独占运行状态、共享预算中的额度和唯一终态。是否使用某个 AgentCore
实现 loop，不改变 Runner 与 Harness 的边界。

| 概念 | 语义与 owner | 首版提供方 |
|------|--------------|-----------|
| `ExecutionSpec` | 一次执行的不可变输入：模型、初始消息、工具、策略、预算句柄和限制 | 适配层组装 |
| `Execution` | Harness 内的一次运行；独占 conversation、compression、tool state、deadline 与收尾状态 | agentcore `Agent`（一 Unit 一实例） |
| `AgentLoop` | 在 Execution 内反复完成 context projection → model call → tool execution → stop decision | agentcore |
| `ContextStrategy` | 决定消息如何投影、回收、压缩和降为 provider 输入；策略由调用方组合 | agentcore `ContextManager` 接口；review 策略由适配层实现 |
| `Budget` | 为模型请求提供 lease 并按真实 usage 结算；可以由多个 Execution 共享 | 暂缓（见关键设计 3） |
| `Tool` / `ToolGate` / `Hook` | 工具能力、调用前门禁和调用前后扩展点；领域语义由外围 Hook 持有 | agentcore |
| `Guard` | 判断领域是否完成，以及预算、时间或外部信号是否要求停止或收尾 | agentcore `StopGuard`；完成条件由适配层注入 |
| `Event` | 执行过程的事实流，供 session、telemetry、eval 和 UI 消费 | agentcore |
| `ExecutionResult` | 唯一终态、usage 与必要的运行记录；不包含 Unit coverage 或 Finding | 适配层基于 Event 与 Guard 裁决 |

Runner 可以为一个 Unit 建立领域侧的 `UnitExecution`，但它进入 Harness 后只是一条
`ExecutionSpec`。Harness 返回 `ExecutionResult`，Runner 再结合领域 Hook 产出 `UnitResult`。
这样 Unit 仍是 review 的货币，Execution 则是通用执行货币。

## 流程

```text
Unit + Dossier + Briefing
            │
            ▼
Runner UnitExecution
  ├── review messages / lowering
  ├── read-only tools + ToolGate
  ├── review ContextStrategy
  ├── completion / stop guards
  └── finding hooks
            │
            ▼
      ExecutionSpec
            │
            ▼
       Harness.Run
  ┌───────────────────────────────┐
  │ create isolated Execution     │
  │             │                 │
  │             ▼                 │
  │ project / reclaim / compress  │
  │             │                 │
  │             ▼                 │
  │ acquire lease → model call    │
  │             │                 │
  │             ▼                 │
  │ validate / gate / run tools   │
  │             │                 │
  │             ▼                 │
  │ hooks + events + stop guard   │
  │             │                 │
  │        next turn / finish     │
  └───────────────────────────────┘
            │
            ▼
      ExecutionResult
            │
            ▼
Runner → UnitResult → aggregate / adjudicate → Finding[]
```

一次 Execution 的主链路是：

1. Harness 根据 `ExecutionSpec` 创建独立运行状态，不复用其他 Execution 的 conversation、
   compression job 或 tool state。
2. 每轮请求前由 `ContextStrategy` 生成本轮 provider context：先做确定性的去重和回收，
   必要时再申请预算执行压缩。
3. Harness 为模型调用申请 token lease；没有 lease 时不发送请求，转入已预留的收尾路径或
   形成 incomplete 终态。
4. 模型响应进入 transcript；工具调用经过参数校验和 ToolGate，再由 Tool 执行，并通过 Hook
   交给领域扩展。
5. Event 记录 model、tool、compression、usage 和终态事实；Stop Guard 决定继续、强制收尾
   或终止。
6. 所有同步和异步工作结束后，Harness 只产出一个 terminal `ExecutionResult`。

## 关键设计

### 1. Execution 是唯一可变状态边界

Harness Core 可以共享无状态的 LLM client、工具实现和预算账本，但不能共享“当前会话”。
conversation、compression snapshot/job、turn counter、deadline、tool call state 和异步收尾
都由单个 Execution 独占。Runner 负责多个 Execution 的并发；Harness 不以全局 Runner 对象
承载执行中的可变状态。

这条边界直接防止并发 Unit 互相抢压缩任务、串消息或覆盖完成状态。它应由依赖测试和并发契约
测试守住，而不只是一条文档约定。

### 2. Context 管理是一条有顺序的生命周期

更多上下文不是一次性预载更多字符串。Harness 每轮按以下顺序处理：

1. **projection**：保留领域消息身份，在模型边界统一 lowering；
2. **deduplication**：消除确定重复、但保持 tool call/result 配对；
3. **reclamation**：优先移除可重新获取的 File、Board 等内容；
4. **compression**：只压缩不可继续原样保留的历史；
5. **active context**：保留最近推理、未决工具调用和当前证据。

Core 负责编排这条生命周期并守住消息协议，具体保留什么属于 `ContextStrategy`。Review
Strategy 必须保留 confirmed fact、hypothesis、rejected hypothesis、证据引用与未决问题；
不能用通用聊天摘要抹掉“为什么某个问题已被否证”。具体消息身份和 lowering 不变量由
`message-model.md` 定义。

### 3. 预算机制与业务策略分离

Harness 的 Budget 只回答“这次请求能否发出、发出后花了多少”：

- 主 loop、compression 和 forced wrap-up 使用同一账本；
- 请求前 lease，响应后按 provider usage settle；
- provider usage 事后可知，所以限制的是新请求派发，不承诺账单精确封顶；
- deadline、turn limit 和 token lease 共同参与停止判断。

Runner 决定快速/深度模式、Run 总预算、Unit admission、覆盖公平和最终裁决预留。Harness
不能根据 Unit 类型私自改变额度，也不能在预算耗尽后绕过账本继续调用模型。

> **首版暂缓 lease/settle**：usage 只做事后记账，请求前不拦截。机制设计保留——它是
> Runner 侧总预算（`run-model.md`）落地时的既定接口，届时在适配层的 ChatModel 包装处
> 回补 lease 点，不动 agentcore 内部。

### 4. 完成信号与停止原因正交

Harness 不内建 `task_done`，而由调用方注入 Completion Guard 判断领域完成条件。通用结果至少
区分：

- execution 是否 completed、incomplete、failed 或 cancelled；
- 是自然结束还是 forced wrap-up；
- 停止原因是 budget、deadline、turn limit、model/tool error 或外部取消。

forced wrap-up 只是一种收尾方式，不自动代表 completed。CCR 的 Review Guard 只有观察到
`task_done` 才接受完整结果；否则即使没有 Finding，也必须向 Runner 返回 incomplete，不能被
解释为 clean review。

### 5. 工具能力通过组合进入 Core

Tool 提供 schema 与执行能力，ToolGate 在执行前实施权限和运行策略，Hook 在调用前后连接领域
行为，Event 则记录已经发生的事实。Core 只按 Registry 查找工具，不维护 review 工具枚举，
也不出现 `if review`。

CCR 的只读信任边界由 Runner Review Extension 组合出来：只注册受控代码读取工具，
`code_comment` 只形成内存中的 candidate，`task_done` 只改变完成状态。Harness 能运行注入的
通用工具，不代表 review loop 可以获得 shell、文件写或 git 状态修改能力。

### 6. Event 是观测接口，不是第二套状态机

Harness 对 model request/response、tool start/end、compression、usage、warning 和 terminal
outcome 发出有序 Event。Session 持久化、telemetry、eval 导出和 UI 是 Event 的消费者，
不反向控制 AgentLoop；改变下一轮行为仍通过显式 Strategy、Hook 或 Guard。

为了验证“预算内提高质量”，每个 Execution 至少应能回答：注入了哪些 context、各阶段消耗
多少 token/时间、发生了几次回收或压缩、调用了哪些工具、是否完成以及为何停止。Finding
正确性继续由 Runner/eval 量测，不能把“loop 顺利结束”当作质量提升。

### 7. 执行内核采用 agentcore，`internal/harness` 是适配层

本文概念表中的 AgentLoop、Execution 隔离、ContextManager、StopGuard、ToolGate 和 Event
流，与 [`agentcore`](https://github.com/voocel/agentcore)（Go，Apache-2.0）的已有接口逐条
对应。首版**不自研 Go agent loop**，直接以 agentcore 为执行内核；`internal/harness` 从
自研 Core 变为适配层，持有且只持有以下 ccr 专属职责：

- **ChatModel 适配**：包 `internal/llm` 现有 client（含 LLMRouter failover），不替换
  provider 层；agentcore 对 ccr 的路由与重试策略无感。
- **review 完成纪律**：`task_done` 映射为 terminal tool + `StopGuard` 裁决，deadline/round
  reserve 的 forced wrap-up 经 `Agent.Inject` 在 turn 边界注入（关键设计 4）。
- **review ContextStrategy**：file dedup、file evict、保 confirmed fact 的压缩纪律，实现为
  agentcore `ContextManager`/Strategy（关键设计 2）。
- **Event 接线**：agentcore Event 流 → ccr 的 session 持久化、telemetry 与 viewer（关键
  设计 6），以及基于 Event 与 Guard 的 `ExecutionResult` 领域裁决。
- **Board/Review Team**：继续走领域 middleware/hook，不进适配层。

**领域代码不直接 import agentcore，只经 `internal/harness` 适配层。** agentcore 是年轻的
外部依赖，这条边界保证升级、替换或 vendor/fork 只动一层；同时沿用现有 boundary test 守住
Harness 不 import review 域的方向，并加反向的 import 约束。仍不为未来接入 Codex/Claude
预建 Backend 抽象——只有当执行运行时本身再次变化时才重新评估。

## References

- 三个能力中心与依赖方向：[`kernel.md`](kernel.md)
- Runner、UnitExecution、Run 级预算与结果裁决：[`run-model.md`](run-model.md)
- review 消息、lowering、去重与驱逐：[`message-model.md`](message-model.md)
- Unit 的 Dossier、Briefing 与静态 context：[`context-model.md`](context-model.md)
- Board/Bulletin 的跨 Execution 信息共享：[`cross-unit.md`](cross-unit.md)
- 质量、成本和健壮性量测：[`../eval/README.md`](../eval/README.md)

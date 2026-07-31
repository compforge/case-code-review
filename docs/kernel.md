# CCR Kernel：Language × Unit × Harness

> 绿地架构总览。本文只定义长期稳定的能力中心、依赖方向与扩展纪律；领域模型、流程细节和
> 当前实现分别由 References 中的专题文档与代码负责。

## 理念 / 概念

CCR 的主线是：**在时间与 token 预算可控的前提下，为每个 Unit 提供足够的上下文，提高
review 质量并减少误报。**

沿这条主线，内核收敛成三个能力中心：

| 能力中心 | 拥有什么 | 不拥有什么 |
|----------|----------|------------|
| **Language** | 从源码得到 symbol、span、call、reference、doc 等语言事实 | Unit、评审策略、prompt 和执行状态 |
| **Unit** | 评审作用域，以及围绕作用域汇总的 Clue、Dossier、Briefing | LLM 调用、工具执行、压缩和运行预算 |
| **Harness** | Agent 执行、工具调度、上下文生命周期、压缩、预算、事件与终态 | 源码语义、Unit 形成规则和 finding 业务判断 |

三个中心之外保留一个薄的 **Runner** 编排入口：它拥有输入模式与评审结果。git diff 和
full-file scan 都先归一为 Unit 的 `Change` 输入；最终 `CandidateFinding` / `Finding`、
行号定位、重定位与裁决都属于 Runner 结果管道。Runner 将每个 Unit 交给 Harness 执行，
但不把这些 review 语义下沉到 Harness Core。

这里的“通用 Harness”是指**不含 review 领域分支的执行内核**，不等于建设多 Backend 或
多 Agent 平台。首版在 Go 内收敛 Execution Core，参考 Pi AgentCore 的消息转换、工具 Hook
和事件边界，但不引入 Node 运行时，也不为未来可能接入 Codex、Claude 预建适配层。

## 流程

```text
Runner Source
  ├── git change（workspace / range / commit）
  └── full-file scan
    │
    ▼
Change + Language Facts
    │
    ▼
Fragment ──merge──▶ Unit
                       │
                       ├── relation / spec / case / rule / link / history
                       ▼
                Dossier + Briefing
                       │
                       ▼
                    Runner
                       │
                       ▼
              Runner Review Extension
                 ├── read-only tools
                 ├── review compaction
                 ├── completion guard
                 └── finding hooks
                       │
                       ▼
                  Harness Core
                 ├── Agent Loop
                 ├── tool runtime
                 ├── context lifecycle
                 ├── token / time budget
                 └── execution outcome
                       │
                       ▼
              UnitResult ──aggregate / adjudicate──▶ Finding[]
```

这一流程使用两种不同的货币：

- **Unit** 是 review 领域的货币：决定评审什么、携带什么知识，也是 Runner 调度和覆盖率
  统计的基本单位。
- **Execution** 是 Harness 的货币：一次有明确输入、限制和终态的 Agent 执行。Harness 不需要
  知道它执行的是代码评审。

Runner 负责把 Unit 转成 Execution 输入，并把 Execution 结果还原成 UnitResult；跨边界的
转换不进入 Language、Unit 或 Harness Core。

代码所有权与数据流一致：`internal/runner/source` 和 `internal/runner/scan` 是两类输入，
`internal/unit/change` 是形成 Unit 的统一源材料，`internal/runner/finding` 是结果与
`code_comment` hook。不存在平铺的 `internal/diff`、`internal/scan`、`internal/finding`，
也不允许 Harness 反向依赖这些领域包。

## 关键设计

### 1. 依赖朝事实与通用机制流动

Language 只产事实，Unit 消费事实形成评审作用域与上下文；Harness Core 只消费执行输入。
Review 扩展同时理解 review 契约和 Harness 扩展点，是两侧的适配层。任何 provider
或 wire 类型都不得反向进入 Language 和 Unit。

### 2. Core 只有一套执行模型

Harness Core 统一拥有 Agent Loop、模型调用、工具调度、context projection、compression、
deadline、usage 和 outcome。一次 Execution 创建一个独立 Agent；conversation、compression
job、tool state 和异步收尾不得跨 Execution 共享。

`agent`、`reviewloop`、`llmloop` 不再各自代表一套生命周期。顶层编排只叫 Runner，领域对象
叫 Unit，Harness 只谈 Execution；Agent 是 Harness 内部实现词。

### 3. Review 强化只组合扩展点

Review 能力通过 Tool、ToolGate、Hook、Middleware、Context Strategy、Stop Guard 等扩展点
包在 Core 外围，包括：

- 封闭的只读代码工具和 `code_comment`、`task_done`；
- 保留 fact、hypothesis、rejected hypothesis、证据引用和未决问题的 review 专用压缩；
- 预算将尽时的 forced wrap-up，以及“没有 `task_done` 就不是完整结果”的结束纪律；
- File/Board 等可重新获取内容的去重与优先淘汰；
- finding 收集、评论重定位、Board 通信和评测事件。

Core 不出现 `if review`。当现有扩展点表达不了需求时，先提炼领域无关的机制，再由 Review
扩展实现策略；不 fork 第二套 loop。

tool 名称不是 Harness 的封闭枚举：Core 只按 Registry 判断工具是否可用，领域工具通过稳定
名称注入。异步 Tool 后处理使用 Harness 的通用 WorkerPool，但结果存储仍由领域 Hook 拥有。

### 4. 预算机制属于 Harness，预算策略属于 Runner

Harness 提供通用的 token lease、usage 结算、deadline 和 turn limit，并保证没有 lease 就不
发送模型请求；主循环、压缩和收尾调用使用同一账本。Runner 根据快速/深度模式决定总额度、
Unit admission 和覆盖策略。

provider usage 只能事后获得，因此预算承诺是“限制新请求的派发”，不是假装账单可以精确封顶。
预算或时间耗尽必须形成明确的 partial/incomplete 结果，不能被下游理解为 clean review。

### 5. 更多上下文不等于无限预载

Unit 负责把能一步定界、高信号的材料组织进 Briefing；开放式探索留给 Harness 中的只读工具。
当上下文增长时，Harness 先按可再生性去重和淘汰，再使用 review 专用压缩。上下文选择、压缩
和预算由此形成一条闭环，而不是三个互不知情的补丁。

### 6. Review 的只读边界是结构约束

Harness Core 可以执行任意被注入的工具，但 Review 扩展只注册 CCR 控制的只读工具，并以
ToolGate 做硬限制。shell、文件写和 git 状态修改不因 Harness 的通用性而进入 review loop；
需要副作用的能力属于评审之后的独立执行环节。

## References

| 主题 | 文档 |
|------|------|
| 源码事实、symbol-id 与解析后端边界 | [Language：源码事实边界](language.md) |
| Fragment、Unit、作用域形成与合并轴 | [Unit 模型](unit-model.md) |
| Clue、Relation、Dossier 与 Briefing | [Context 模型](context-model.md) |
| caller/callee、repo-map 与图消费策略 | [Codegraph：统一图消费层](codegraph.md) |
| review 领域消息、lowering、去重与驱逐 | [Message 模型](message-model.md) |
| Execution、Agent Loop、上下文、预算与终态 | [Harness 模型](harness.md) |
| Runner、UnitExecution、预算与结果终态 | [Runner 模型](run-model.md) |
| Board、Bulletin 与跨 Unit 协作 | [Review Team](cross-unit.md) |
| 质量、成本与健壮性的评测闭环 | [Eval README](../eval/README.md) |

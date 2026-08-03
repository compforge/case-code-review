# CCR Kernel：Knowledge × Unit × Harness

## 1. 理念 / 概念

CCR 的目标不是让一个大模型“读完整个 diff 然后自由发挥”，而是建立一条有明确领域对象、证据边界
和完成语义的评审流水线，在三项互相拉扯的目标间取得平衡：

- **准确性**：真问题能被发现，结论有证据且归因于当前变化；
- **健壮性**：每个评审范围确实完成，超时和预算耗尽不会伪装成 clean；
- **成本**：时间、token 和工具调用有界，能进入持续 review 工作流。

理论上，评审看到的代码越全越好；工程上，大部分代码与当前变化无关，通读也不能自动补足隐含的
业务约束。CCR 不追求穷举所有问题，而是在相关、可承担的上下文内，优先发现实现需求时容易忽略的
具体缺陷，如边界处理、错误路径和 API 使用问题；更隐含的业务问题依赖 spec、case、rule 等显式知识，
不能靠无限扩大上下文解决。

Kernel 由两类 Knowledge、Unit 和 Harness 组成：

| 能力中心 | 回答的问题 | 不负责 |
|---|---|---|
| **Project** | Repository 中有哪些 Component、文件角色，以及 `spec / case / link / rule / doc` 声明了哪些业务契约和场景 | 解析代码语义、决定是否成 Finding |
| **Language** | 源码中有哪些 outline、definition、reference、call edge、symbol/file proximity 和稳定身份，作者声明如何绑定到代码 | 决定声明含义、决定是否成 Finding |
| **Unit** | 哪些改动应一起审，本次 run 已获得哪些事实快照和阶段结论 | 运行 agent loop、决定阶段策略 |
| **Harness** | 一次 agent execution 如何有界运行、完成并被观测 | 理解 Unit、Hypothesis、Finding |

Runner 是薄编排层：选择 review snapshot，调用 Project / Language / Unit 形成输入，用 Harness 执行两个
agent Review，再交给确定性的 Trial（Review 3）并聚合领域结果。领域行为通过 execution spec、tool、hook
和 event 适配 Harness，而不是塞进执行内核。

评审事实来自两个互补方向：Language 提供 symbol、outline、definition、reference、call edge、
symbol/file proximity 及语法绑定机制；
Project Knowledge 一部分是 Repository / Component、可组合 FileRole（如 source + entrypoint / handler）
和 manifest 等结构知识，另一部分是 `spec / case / link / rule / doc` 等作者声明的 Biz Knowledge。后者
通过 Language 的注释、装饰器、symbol-id / fqn 等机制绑定到代码：Language 拥有“如何绑定”，Project
Knowledge 拥有“声明表达什么”。这套模型也是 case-code-review 最初的核心与名称来源。

长期看，一个 Repository 还应提供开发与 review 共同消费的
记忆文件，使项目约定、历史决策和业务背景不必分别维护两份；具体存储协议不属于当前 Kernel 契约。
它们既参与 Unit 形成，也可按评审作用域投影为上下文：Review 1 将 Clue 汇入 Unit，Review 2
可面向 Hypothesis 与 Lane 重新选择同一批事实，而不是复用 Review 1 的 prompt 形状。

## 2. 总体流程

![CCR 从 Diff 到 Finding 的评审流水线，以及两类 Knowledge 基础](review-pipeline.svg)

```text
Git diff
  │
  ▼
Change ─▶ Component / FileRole
                  ├─ source ─Splitter─▶ Fragment ─Merger─▶ Unit
                  │     └─ entrypoint / handler ──────────▶ Clue
                  └─ manifest / lock ─────────────────────▶ Clue
                                      │
                         ClueFinder ─▶ Unit{Fragments, Clues}
                                      │
                                      ▼
                              Unit Review (Review 1)
                                      │
                              append Hypothesis to Unit
                                      │
                              assign related Lane
                                      │
                                      ▼
                           Hypothesis Review (Review 2)
                                      │
                 append snapshots/results + Assessment to Unit
                                      │
                         Trial delivery gate (Review 3)
                                      │
                       append Decision to Unit ─▶ Finding
```

这条链路借用了“调查—复核—裁决”的比喻，但对象是工程契约，不是角色扮演：

- `Clue / Unit == Review 1 ==> Hypothesis`：在一个行为范围内探索，提出可证伪的怀疑；
- `Hypothesis == Lane / Review 2 ==> Assessment`：相关假设复用上下文，独立复核已有主张；
- `Assessment == Trial ==> Finding`：确定性规则决定是否值得向开发者交付。

Review 3 是 Trial 的流程别名，不引入第三个 agent loop 或新的领域对象。三个阶段借“吾日三省吾身”作
记忆点：发现、复核之后，结论在交付前还要由确定性规则再审一次。

两次 Review 的聚合维度不同：Unit Review 按行为形成 Unit，以减少重复 loop 并补齐局部上下文；
Hypothesis Review 把关系紧密的 Hypothesis 投入同一 Lane，串行复用 conversation 与证据。归 Lane
依据行为与证据关系，Project 目录距离只作加权，不把文件边界误当成问题边界；不相关 Lane 可以并行。

这条链路没有全局阶段屏障。一个成熟 Hypothesis 一经接受，就可以立即进入 Review 2 和 Trial；此时
其它 Unit 仍可能在 Review 1 中探索，后续 Hypothesis 甚至尚未产生。因此 Finding、Assessment 与
Hypothesis 可以按真实完成顺序交错出现，最终输出顺序不等于执行顺序。

## 3. 关键设计

### 3.1 事实、作用域、执行和结论各有唯一 owner

- Project 拥有 Repository、Component、FileRole 与项目事实；
- Language 拥有源码事实与置信度；
- Unit 拥有一次 run 的行为作用域、不可变文件/diff/搜索快照和已接受的阶段结果；
- Harness 拥有 execution 生命周期、工具机制、预算和观测事件；
- Review 1 拥有 Hypothesis 的产生逻辑，并把结果追加到来源 Unit；
- Review 2 拥有 Assessment 的产生逻辑，并把补充快照与结果追加到来源 Unit；
- Trial 接收完整 Unit，拥有 Assessment 到 Finding 的确定性门禁，并把决策追加到来源 Unit。

这些 owner 在 `internal/runner` 中分别落为 `formation`、`unitreview`、`hypothesisreview` 和
`trial`；根 Runner 只串联阶段并处理 run 级持久化。Trial 不依赖 Harness、LLM 或 prompt。

同一概念若在多个模块各自表示一次，最终会出现 diff、prompt、session 和 viewer 对不上。Kernel 的
首要职责不是添加更多层，而是保持这些 owner 清晰。

### 3.2 Unit 与 Execution 是两种不同货币

Unit 是一次 run 的评审领域聚合根；Execution 是 Harness 的一次 agent 运行。当前通常一个 Review 1
Unit 对应一个 Execution，但 Review 2 可以通过 Lane 为同一 Unit 产生更多 Execution。两者不能合并成
同一类型：重试、并行证据核查或固定 Hypothesis 重放都可能改变映射关系，而不改变 Unit 语义。

### 3.3 先捕获确定上下文，再允许有界探索

CCR 相比 file-only review 的优势来自两部分：

1. Unit 在 loop 前携带可确定的 diff、Clue、契约和调用邻域；
2. agent 在 loop 内用只读工具验证未知事实，成功读取的文件快照、相关 diff 和搜索结果回到 Unit；
3. Review 2 先复用形成 Hypothesis 时 Unit 已保留的事实，只为缺失事实继续读取；Trial 随后从同一
   Unit 读取 Hypothesis、Assessment、receipt 与必要事实，而不是接收另一份临时材料包。

全预载会放大成本，只给 diff 又会诱发猜测。初始消息与工具必须形成分工，并由统一上下文生命周期
控制重复读取、淘汰和压缩。

### 3.4 逐条流动，并在预算边界保留已完成结果

Unit Review 可以发散，但不应把成熟 Hypothesis 囤到最后一次性交卷。一条主张已有具体 trigger、
impact、diff attribution 和源码锚点后，就应立即追加到 Unit 并送入 Review 2；Review 1 随后继续调查
下一个 lead。仍缺的关键事实写入 uncertainty，由收敛阶段定向复核，而不是要求 Review 1 清零所有
不确定性。临时怀疑仍是内部调查状态，不能为了尽早提交而降格为 Hypothesis。

Hypothesis、Assessment 和 Trial Decision 都按各自完成时点持久化并向下游流动。Review 2 仍一次判断
一个 Hypothesis，合法 Assessment 立即触发确定性 Trial，不等待其它 Unit 或 Lane。一次 run 只在退出
时汇合仍在运行的 Review 1、Lane 与 Trial；并发造成的产出顺序由最终稳定排序吸收。

接近预算或 deadline 时，Harness 像收卷一样关闭继续调查，只要求提交当前证据已经支持的结果；不再
允许模型死磕尚未收敛的 lead。这样 review 具备 anytime 性质：运行越久覆盖越完整，但任何时点超时
都能保留此前已经形成的 Hypothesis、Assessment 和 Finding，而不是一无所获。时间和 token 预算
限制的是继续发现问题的范围，不应使已经完成的判断链路失效；这正是 CCR 在有限成本内持续交付
Finding 的目标。高影响但补证较贵的主张应在最短证据链后带明确 uncertainty 提交；只有模糊可能性、
始终没有现实 trigger 的调查才留在可舍弃的尾部。anytime 不等于必须运行到预算上限：简单 Unit
一旦确认没有其它 material lead，就自然结束，整条 pipeline 也可以快速收口。

partial/incomplete 是一等结果。任何未完成 Unit 或未评估 Hypothesis 都应出现在输出和 session 中，
不能混进 “Looks good to me”；空文本或 0 Finding 也不能单独证明完成。

### 3.5 LLM 判断与确定性门禁分离

模型擅长结合证据判断 trigger、impact 和业务语义，但不应自行决定最终协议是否满足。Assessment 将
support、causation、value、novelty 分轴；Runner 签发真实 evidence receipt；Trial 用代码规则决定能否
形成 Finding。这样 wrong 的来源可以被分阶段定位，而不是只看到最终 comment。

### 3.6 Harness 保持领域中立

Harness 可以提供 typed messages、context、tools、hooks、events、budget、completion 和 session，
但不 import Unit / Finding，也不内建“代码评审团队”。Lane、Assessment 以及试验性的 Review Team
都是 Runner 上的 review extension，通过通用机制接入。

旧 `llmloop` 可以保留作隔离参考，但新的领域链路只依赖统一 Harness execution，避免两套运行时同时
演进。

### 3.7 可观测性是正确性的一部分

Session JSONL 持久化实际 prompt、response、tool call、usage、完成状态和决策 artifact；HTML Viewer
从同一事实源投影全局统计与逐 agent loop 时间线。它们不仅用于看日志，还用于判断“0 Finding 是真
clean、被 Trial 拦截，还是 loop 根本没完成”，并支撑回放与 eval。

Viewer 不定义执行协议，JSONL 也不替代 Forge 上跨 revision 的持久交付事实。

### 3.8 只读证据边界

Review 过程默认只读取源码、Git snapshot、规则和既有评论；产出 Finding 由外部调用方决定是否发布。
这种边界让本地复盘已合并 commit、CI review 和离线 eval 可以复用同一内核，而不产生意外外部副作用。

### 3.9 效果由流程、轨迹与知识共同演进

CCR 的效果提升不是单纯换模型或扩大 prompt，而依靠三项能力互相校正：

1. **成熟稳定的 Review Pipeline**：明确发现、复核、裁决的职责和完成语义，使阶段结果能增量流动、
   超时可保留、简单问题可快速结束；
2. **基于轨迹的 Review 优化**：从 Session JSONL 生成 Review 1 / Review 2 trajectory，定位空转、重复读取、
   未完成和错误收敛，再据此新增、删除或调整 prompt、tool 与 tool schema；
3. **持续增长的 Knowledge**：Language 通过 Outline、CodeGraph 和 proximity 等能力提供更可靠的源码结构、关系与绑定位置，
   Project Knowledge 同时提供 Repository / Component / FileRole 等结构事实和
   `spec / case / link / rule / doc` 等业务契约，让 Unit formation 和两个 Review 获得更相关的事实。

Pipeline 提供稳定实验骨架，trajectory 说明问题发生在哪一步，Language / Project 决定还能补充哪些
高价值事实。三者缺一时，效果变化都难以归因，也容易把成本增长误当成质量提升。

## 4. 如何判断一项优化是否“对味”

任何新能力都应明确落在以下一个问题上：

1. 它是否补充可靠的 Project / Language Knowledge，而没有把评审策略塞进事实层？
2. 它是否让 Unit 更接近一个行为变化，或让 Unit.Clues 更准确？
3. 它是否让 Execution 更容易完成、成本更可控、失败更可见？
4. 它是否提高 Hypothesis / Assessment 的证据质量，而不是只增加 prompt？
5. 它是否能用 session 和人工标签验证，并同时观察 wrong 与 missed？

若一个改动只是增加 agent loop、添加重复 schema 或让空结果更像成功，它不属于 Kernel 优化。

## 5. 设计文档地图

| 文档 | 唯一 owner |
|---|---|
| [`project.md`](project.md) | Repository / Component / FileRole 与项目事实投影 |
| [`unit-model.md`](unit-model.md) | Fragment / Unit、Clue、上下文与图消费 |
| [`unit_review.md`](unit_review.md) | Review 1 的探索、收敛、跨 Unit 协作和效果优化 |
| [`hypothesis_review.md`](hypothesis_review.md) | Lane、Hypothesis Review、Assessment、receipt 与 Trial |
| [`harness.md`](harness.md) | Execution、上下文生命周期、工具、Session JSONL 与 Viewer |
| [`language.md`](language.md) | 多语言源码事实、身份、索引与置信度 |

README 面向使用者，`AGENTS.md` 只保留项目地图和长期约束；字段、阈值和具体分支行为以代码为准。

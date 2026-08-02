# Hypothesis Review：从怀疑到可交付结论

## 1. 理念 / 概念

Unit Review 负责发散：在每个行为范围内提出值得核实的 Hypothesis。Hypothesis Review 负责收敛：
把每个假设及其材料形成 Dossier，按关系投入可复用上下文的 Lane，复核证据、变化归因、交付价值和
重复性，再交给确定性的 Trial。
拆成两阶段的重要原因是 LLM 领域里“生成候选”通常比“验证给定候选”更难：前者需要开放搜索并追求
召回，后者面对封闭集合查找支持与反证并追求精度。两者需要不同的 role、上下文和完成契约。

```text
Hypothesis ─▶ Dossier ─▶ Lane / Hypothesis Review ─▶ Assessment ─▶ Trial ─▶ Finding
    怀疑          卷宗材料          相关案卷复用经验与上下文        结构化判断      确定裁决
```

两次 Review 的 system role 必须不同：

- Review 1 可以主动探索并提出多个可证伪解释，目标是避免漏掉真问题；
- Review 2 只评估已提交的 Hypothesis，不新造 issue，目标是挡住证据不足、归因错误、低价值和重复交付。

Finding 不是“模型又说了一遍同样的话”，而是 Assessment 通过 Trial 后的领域结果。

Review 2 是可替换的验证策略，不是 Hypothesis 或 Dossier 的所有者。若未来模型能在一次 Unit Review
中稳定完成发现与验证，可以缩短或移除 Review 2，让 Trial 直接消费同一批 Hypothesis；当前不为此
预设统一的大产出类型，避免把尚未验证的演进方向固化进核心模型。

## 2. 流程

### 2.1 Dossier 与 Lane

Dossier 是 Review 1 向 Review 2 移交的不可变材料。一个已解析完成的 Hypothesis 立即形成一个
Dossier，不等待其它 Unit Review，也不在开放案卷中继续聚合。它包含：

- 待复核的 Hypothesis 及其 trigger、impact、锚点和已有证据；
- 形成这些假设时涉及的 Unit 输入、结构化 evidence 和实际文件读取路径；
- review snapshot 的 diff 与 baseline 读取能力；
- 已经向当前 PR/MR 交付过的 finding，作为 prior delivery。

Lane 是触发和承载 Review 2 的执行边界。`LanePool` 按同一 Unit、改动符号、结构化 evidence 和高重合的
实际读取路径选择已有 Lane；Repository / Component 和目录公共祖先只作多个候选 Lane 的距离加权，
不能单独建立问题关系。找不到相关 Lane 时立即创建新 Lane。

同一 Lane 串行消费 Dossier，并保留 AgentGo conversation、已签发 evidence receipt 和前案 Assessment；
因此后案能直接复用已经读过的文件和判断背景。不同 Lane 在全局并发上限内并行。这样不再需要静默窗口、
开放案卷或 Dossier 编排器：Dossier 始终只是材料，Lane 才拥有 Review 2 的上下文与执行顺序。

### 2.2 独立复核

Reviewer 可用只读工具检查 head 源码、baseline、diff 和仓库引用。每个 Hypothesis 必须得到一份
Assessment，至少包含四个正交判断：

| 轴 | 关注点 |
|---|---|
| `support` | trigger、执行路径和 impact 是否被证据支持 |
| `attribution` | 问题是否由当前 diff 事实上造成 |
| `value` | 是否是开发者此刻值得修复的问题 |
| `novelty` | 是否为新结论，或已在 case / 既有评论中交付 |

`caused` 表示事实因果，不表示主观意图或责任：撤销当前 diff 后，Hypothesis 的 trigger 或 impact
会消失或实质改变。问题在 baseline 已完整存在时应为 `pre_existing`；证据不足则保持 `unknown`。

### 2.3 证据回执

模型写在 reason 里的路径不是证据证明。Runner 根据真实的 `file_read_base`、`file_read_diff` 等工具
调用签发 evidence receipt，并在提交 Assessment 时附着。模型不能自行构造可信 receipt。

该机制把两件事分开：模型负责判断“证据意味着什么”，执行层负责证明“这些证据确实被读取过”。
它不能保证判断一定正确，但能挡住未检查 baseline 就声称 `caused` 的明显 padding。

### 2.4 增量提交与完整完成

Review 2 的完成条件不是模型说 `task_done`，而是当前 Dossier 中每个 Hypothesis 都有一个合法 Assessment。
Reviewer 应在判断完成时立即调用 `submit_assessments`，可以单条或小批量多次提交，避免把所有结果押在
最后一轮。对同一 Hypothesis 的后续合法提交替换前值；Session 保留每次提交，Trial 只消费最终判断。
未知 ID 不进入案卷结果，缺项或非法枚举也不能被当成成功完成。

接近预算上限时，应停止继续取证，用 `insufficient` / `unknown` 完成剩余判断。**未评估不是 clean**；
若执行中断，已提交 Assessment 仍可进入 Trial，未评估项必须出现在 warning 中并被阻止进入 Trial。

### 2.5 Prompt 上下文形状

Review 2 的 Dossier 和后续工具结果都作为 typed message 进入 Harness；压缩可以收短 Change / Clue /
前案摘要，但不能删除当前待评估 Hypothesis。同一 Lane 首次运行建立 system 前缀，后续 Dossier 追加到
已有 conversation：

```text
system: <Review 2 convergence rules / closed-set contract>

user: Dossier A(
  dossier_id,
  hypotheses=[完整待评估集合],
  changes=[相关 diff 元信息],
  clues=[相关上下文],
  prior_dossiers=[前案 Assessment 摘要]
)

assistant: reasoning + tool calls
tool: File / Diff / Base / Search messages
assistant: reasoning
tool: submit_assessments([已完成的部分判断])

...继续核查剩余 Hypothesis...

user: <hard wrap-up：只允许 submit_assessments / task_done>
tool: submit_assessments([剩余判断])
tool: task_done

user: Dossier B(...)  # 同一 Lane，继续使用前面的 assistant/tool/context
assistant: reasoning + tool calls
tool: submit_assessments(...)
tool: task_done
```

稳定的 Lane 前缀有利于 provider cache；新增案卷、证据和提交回执追加在尾部。Review 2 只能验证
Dossier 里的既有 Hypothesis，不能通过 message 或工具重新发散出新问题。

### 2.6 Trial

Trial 是确定性规则，不再发起 LLM 推理。只有同时满足以下条件的 Assessment 才能产出 Finding：

```text
supported + caused + actionable + new + matching diff evidence receipt
```

其余状态保留在 Decision Trail 中，用于解释为何被拦截和后续评测。重复假设若指向 canonical
Hypothesis，但 canonical 自身尚未评估，不能据此宣布整个 case clean；它应显式暴露为未完成或先
完成 canonical assessment。

代码边界与流程一致：`internal/runner/hypothesisreview` 到 Assessment 为止；
`internal/runner/trial` 只接收 Hypothesis 与 Assessment，执行确定性门禁，不依赖 Harness 或 LLM。

## 3. 关键设计

### 3.1 真实性、归因、价值、重复必须分轴

一个问题可以真实但 pre-existing，可以由 diff 造成但只有样式价值，也可以完全正确但之前已经发布。
把这些压成单个 pass/fail 会丢失诊断能力，也无法回答 wrong 究竟来自模型能力、上下文、业务意图
还是交付重复。

### 3.2 Review 2 只收敛，不补做 Review 1

Review 2 若被允许不断新造问题，就会再次发散，Dossier 无法形成完成契约，Trial 也失去稳定输入。
发现材料暗示另一个问题时，只能作为现有 Hypothesis 的证据或不足理由；新问题应由 Review 1 在其
负责的 Unit 中提出。

### 3.3 Prior delivery 是 novelty 证据

连续 revision review 时，Forge 上既有 CCR comment 是持久的交付事实。调用方应在每次新容器运行前
读取当前 PR/MR 评论并注入，而不是依赖本地 session 文件。历史 comment 不证明问题仍然真实，只
证明“这项结论已经交付”，因此只影响 novelty 轴。

### 3.4 评测必须观察漏报风险

降低 Finding 数不等于降低 wrong。应同时统计：

- Review 1 产生的 Hypothesis 数和对应 Unit 完成率；
- 四个 Assessment 轴及 receipt 各拦截多少；
- 未评估 Hypothesis 数、Lane 数、每个 Lane 的 Dossier 数、Review 2 工具轮次和耗时；
- 最终 `important/minor/debatable/wrong/repeat` 标签；
- 被拦截样本中的 missed，防止通过少报制造虚假准确率。

Session 应在 Hypothesis 解析完成、Dossier 分配 Lane 和每次 Assessment 提交时立即追加 artifact；Trial
decision 另行记录并引用最终采用的 submission。这样进程中断后仍能还原部分进展，同一批固定
Hypothesis 也可以独立重放 Review 2，而不必每次重跑昂贵的 Unit Review。

## 4. 待验证方向：按判断视角拆分 Review 2

> 这是一项尚未成熟的设计假设，先记录问题与实验方向，不代表当前执行契约。

当前设计把同一 Dossier 交给一次收敛 Review，要求它同时核查技术事实、业务语义、跨模块影响和
交付价值。另一种可能是：**Review 2 的 loop 粒度不按文件或 Hypothesis 数量划分，而按判断视角
划分，一个视角对应一个独立 review loop。**

候选视角例如：

| 视角 | 主要关注点 |
|---|---|
| 技术正确性 | 类型、API/tool contract、控制流、错误处理和运行时可达性；纯语法问题仍优先交给编译器 / lint |
| 局部业务正确性 | 当前 Unit 或功能是否满足自身 spec、case、rule 和需求意图 |
| 跨业务影响 | caller、callee、共享协议、配置和其它业务是否因当前变化受到破坏 |
| 交付判断 | 问题是否 actionable、是否已经交付、是否值得形成公开 Finding |

这些 loop 读取同一个 Dossier，但只提交自己视角下的证据和局部判断；随后由聚合步骤合成每个
Hypothesis 的完整 Assessment，再进入 Trial。视角之间可以并行，也可以让低成本的技术检查先行，
只把未决问题交给业务视角。

该方案可能带来更明确的 role prompt、更聚焦的工具使用和更容易归因的错误来源，但也可能造成：

- 同一份源码和 diff 被多个 loop 重复读取，显著增加成本；
- 各视角边界重叠，对同一 Hypothesis 给出冲突判断；
- Dossier 很小时，拆 loop 的固定开销大于聚焦收益；
- “业务正确性”缺少足够 spec / requirement 时，只是把同一种猜测复制多次。

需要先回答的粒度问题包括：

1. 每个视角审整个 Dossier，还是只接收与该视角相关的 Hypothesis？
2. 技术正确性中哪些应由确定性工具完成，哪些确实需要 LLM loop？
3. 多视角输出是 evidence contribution，还是各自产出完整 Assessment？
4. 视角冲突由规则聚合、额外复核，还是保留为 `insufficient/unknown`？
5. 是否只有大型或跨模块 Dossier 才值得启用多视角？

验证时应固定同一批 Hypothesis，对比“单一收敛 loop”和“按视角多个 loop”，同时观察 Assessment
完成率、重复工具读取、token / wall time、视角冲突率，以及最终 wrong / missed。只有在召回或准确性
收益能够覆盖额外成本时，才把该方向升级为正式流程。

## References

- [`kernel.md`](kernel.md) — 两阶段 Review 与 Trial 的总体位置
- [`unit_review.md`](unit_review.md) — Hypothesis 的产生与 Unit 完成契约
- [`harness.md`](harness.md) — 工具回执、执行预算和 session 记录
- [`eval/README.md`](../eval/README.md) — 人工标签与阶段数据集

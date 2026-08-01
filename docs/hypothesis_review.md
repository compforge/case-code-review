# Hypothesis Review：从怀疑到可交付结论

## 1. 理念 / 概念

Unit Review 负责发散：在每个行为范围内提出值得核实的 Hypothesis。Hypothesis Review 负责收敛：
把相关假设归入同一 CaseFile，复核证据、变化归因、交付价值和重复性，再交给确定性的 Trial。

```text
Hypothesis[] ─▶ CaseFile ─▶ Hypothesis Review ─▶ Assessment[] ─▶ Trial ─▶ Finding[]
  怀疑与线索       同一案件             独立复核             结构化判断      确定裁决
```

两次 Review 的 system role 必须不同：

- Review 1 可以主动探索并提出多个可证伪解释，目标是避免漏掉真问题；
- Review 2 只评估已提交的 Hypothesis，不新造 issue，目标是挡住证据不足、归因错误、低价值和重复交付。

Finding 不是“模型又说了一遍同样的话”，而是 Assessment 通过 Trial 后的领域结果。

## 2. 流程

### 2.1 形成 CaseFile

CaseFile 是 Review 2 的输入边界。它按问题关系而不是文件边界组织假设，包含：

- 待复核的 Hypothesis 及其 trigger、impact、锚点和已有证据；
- review snapshot 的 diff 与 baseline 读取能力；
- 同一 case 内可用于识别重复或互相矛盾的假设；
- 已经向当前 PR/MR 交付过的 finding，作为 prior delivery。

CaseFile 不应无限增长。过大的案卷会让 Review 2 把轮次花在导航上，最后只评估少数假设。分案应
优先保持同一根因、同一行为路径或明显重复的假设在一起，同时给每案设置可完成的数量和证据预算。

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

### 2.4 原子完成

Review 2 的完成条件不是模型说 `task_done`，而是 CaseFile 中每个 Hypothesis 都有一个合法 Assessment。
推荐使用原子的 `submit_assessments` 作为终态动作：缺项、重复 ID 或非法枚举均不能被当成成功完成。

接近预算上限时，应停止继续取证，用 `insufficient` / `unknown` 完成剩余判断。**未评估不是 clean**；
若执行中断，run 必须报告未评估数量，并阻止这些 Hypothesis 进入 Trial。

### 2.5 Trial

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

Review 2 若被允许不断新造问题，就会再次发散，CaseFile 无法形成完成契约，Trial 也失去稳定输入。
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
- 未评估 Hypothesis 数、CaseFile 大小、Review 2 工具轮次和耗时；
- 最终 `important/minor/debatable/wrong/repeat` 标签；
- 被拦截样本中的 missed，防止通过少报制造虚假准确率。

Session 应持久化 Hypothesis、Assessment、Trial 结果和引擎身份，使同一批固定 Hypothesis 可以独立
重放 Review 2，而不必每次重跑昂贵的 Unit Review。

## 4. 待验证方向：按判断视角拆分 Review 2

> 这是一项尚未成熟的设计假设，先记录问题与实验方向，不代表当前执行契约。

当前设计把同一 CaseFile 交给一次收敛 Review，要求它同时核查技术事实、业务语义、跨模块影响和
交付价值。另一种可能是：**Review 2 的 loop 粒度不按文件或 Hypothesis 数量划分，而按判断视角
划分，一个视角对应一个独立 review loop。**

候选视角例如：

| 视角 | 主要关注点 |
|---|---|
| 技术正确性 | 类型、API/tool contract、控制流、错误处理和运行时可达性；纯语法问题仍优先交给编译器 / lint |
| 局部业务正确性 | 当前 Unit 或功能是否满足自身 spec、case、rule 和需求意图 |
| 跨业务影响 | caller、callee、共享协议、配置和其它业务是否因当前变化受到破坏 |
| 交付判断 | 问题是否 actionable、是否已经交付、是否值得形成公开 Finding |

这些 loop 读取同一个 CaseFile，但只提交自己视角下的证据和局部判断；随后由聚合步骤合成每个
Hypothesis 的完整 Assessment，再进入 Trial。视角之间可以并行，也可以让低成本的技术检查先行，
只把未决问题交给业务视角。

该方案可能带来更明确的 role prompt、更聚焦的工具使用和更容易归因的错误来源，但也可能造成：

- 同一份源码和 diff 被多个 loop 重复读取，显著增加成本；
- 各视角边界重叠，对同一 Hypothesis 给出冲突判断；
- CaseFile 很小时，拆 loop 的固定开销大于聚焦收益；
- “业务正确性”缺少足够 spec / requirement 时，只是把同一种猜测复制多次。

需要先回答的粒度问题包括：

1. 每个视角审整个 CaseFile，还是只接收与该视角相关的 Hypothesis？
2. 技术正确性中哪些应由确定性工具完成，哪些确实需要 LLM loop？
3. 多视角输出是 evidence contribution，还是各自产出完整 Assessment？
4. 视角冲突由规则聚合、额外复核，还是保留为 `insufficient/unknown`？
5. 是否只有大型或跨模块 CaseFile 才值得启用多视角？

验证时应固定同一批 Hypothesis，对比“单一收敛 loop”和“按视角多个 loop”，同时观察 Assessment
完成率、重复工具读取、token / wall time、视角冲突率，以及最终 wrong / missed。只有在召回或准确性
收益能够覆盖额外成本时，才把该方向升级为正式流程。

## References

- [`kernel.md`](kernel.md) — 两阶段 Review 与 Trial 的总体位置
- [`unit_review.md`](unit_review.md) — Hypothesis 的产生与 Unit 完成契约
- [`harness.md`](harness.md) — 工具回执、执行预算和 session 记录
- [`eval/README.md`](../eval/README.md) — 人工标签与阶段数据集

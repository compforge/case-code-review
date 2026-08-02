# Hypothesis Review：从怀疑到可交付结论

## 1. 理念 / 概念

Unit Review 负责发散：在一个行为范围内提出可证伪的 Hypothesis。Hypothesis Review 负责收敛：
对已有主张补证、反驳、判断变化归因和交付价值，再交给确定性的 Trial 门禁。

```text
Hypothesis ── assign Lane ──▶ Hypothesis Review ──▶ Assessment ── Trial gate ──▶ Finding
```

生成问题通常比验证一个明确问题更难，两次 Review 因而采用不同的 system prompt、工具和完成契约：

- Unit Review 可以探索并提出多个可能问题，目标是避免漏掉真问题；
- Hypothesis Review 一次只判断一个已提交 Hypothesis，不新造 issue；
- Trial 不运行 LLM，只把满足交付策略的 Assessment 转为 Finding。

核心领域对象只有 Hypothesis、Assessment 和 Finding。Lane 是 Review 2 的运行边界；Change、Clue、
prior Assessment 等材料在调用时投影为 `ReviewInput`，它只是包接口输入，不是新的领域对象。

## 2. 流程

### 2.1 Hypothesis 进入 Lane

每个解析完成的 Hypothesis 立即进入 Review 2 调度，不等待全部 Unit Review 结束。`LanePool` 用强关系
选择 Lane：同一 Origin Unit、重叠的 target/evidence/read path、共同 symbol 或高比例读取重合都可
建立关系；Repository / Component 和目录公共祖先只在多个候选 Lane 间加权，不能单独证明两个问题
相关。

找不到相关 Lane 时创建新 Lane。相同 Lane 串行判断 Hypothesis，并保留：

- AgentGo conversation continuation；
- 已签发的 evidence receipt；
- 前面 Hypothesis 的 Assessment。

因此后续 Hypothesis 可以复用已经读取的文件与判断背景；不相关 Lane 在全局并发上限内并行。
Lane 拥有上下文、顺序和生命周期，Hypothesis 本身不承载这些运行状态。

### 2.2 独立复核

Review 2 收到当前 Hypothesis、相关 Change / Clue、Unit Review 实际读取路径和 Lane 前案后，分别判断：

| 轴 | 问题 | 结果 |
|---|---|---|
| `support` | trigger、执行路径和 impact 是否被证据支持 | `supported / contradicted / insufficient` |
| `attribution` | 当前 diff 是否事实上造成或实质改变问题 | `caused / pre_existing / unknown` |
| `value` | 是否是开发者值得修复的具体缺陷 | `actionable / low_value / unknown` |
| `novelty` | 是否已经在本次 run 或 MR 中交付 | `new / duplicate_in_case / already_delivered` |

这些轴不能压成一个总分。真实但 pre-existing、真实但低价值、或已经交付的问题，都不应再次形成
Finding。

### 2.3 只读证据与 receipt

Review 2 可以读取 diff、baseline、当前源码和搜索结果，但不能发布 Finding。模型提交的 evidence 文本
只是引用；Runner 同时记录实际只读工具调用形成 receipt。Trial 要求评论锚点存在匹配的 diff receipt，
防止模型只靠叙述伪造“已经核实”。

receipt 是完整性机制，不是主流程中的领域对象。它随 Assessment 持久化，供 Trial、Viewer 和 eval
核对。

### 2.4 增量提交与完成契约

当前一次 Review 2 execution 只负责一个 Hypothesis。模型判断完成后应立即调用
`submit_assessments`。Harness 只在当前 Hypothesis 的 Assessment 通过解析和领域校验后接受完成；合法
提交由 AgentGo 作为 terminal tool 结束 execution，不再额外调用 `task_done`。无效提交只返回修正提示，
loop 继续运行。

若 execution 超时或失败：

- 已提交 Assessment 仍然保留；
- 未评估 Hypothesis 产生明确 warning；
- 未评估项不能通过 Trial，也不能把 0 Finding 解释为 clean。

### 2.5 Prompt 上下文形状

首次进入 Lane 时建立稳定 system 前缀；后续相关 Hypothesis 通过 continuation 追加到同一 conversation：

```text
system: <Review 2 convergence rules / closed-set contract>

user: Hypothesis A(
        claim,
        change_set,
        clues,
        evidence_paths,
        prior_assessments=[]
      )
assistant: tool_call(file_read_diff / file_read_base / file_read / code_search)
tool: <typed evidence result>
assistant: tool_call(submit_assessments)
tool: <accepted; execution completes>

user: Hypothesis B(...)  # 同一 Lane，只追加新材料，复用前面的 conversation
...
```

continuation 保留先前的 assistant、file/tool result 和 Assessment；新一轮只追加当前 Hypothesis 及其
增量 Change / Clue / navigation hints，不重复挂载旧 file message、Assessment 或 receipt。receipt 账本由
Runner 在模型上下文之外累计，继续供 Trial 校验；若旧文件内容已被 compaction 压到不足以判断，模型
仍可按需重新读取。

Supporting Change / Clue 可以在预算压力下降级，但当前 Hypothesis 不能被删除或只剩 ID。稳定前缀和
尾部追加有利于 provider cache，也避免每次复核重新读取相同上下文。

### 2.6 Trial 是确定性门禁

Trial 不发起模型推理。只有 Assessment 同时满足以下条件才产出 Finding：

```text
support     == supported
attribution == caused
value       == actionable
novelty     == new
evidence    != empty
matching diff receipt exists
```

Finding 是通过交付门禁后的结果，不是“模型又重复了一遍 Hypothesis”。

## 3. 关键设计

### 3.1 Lane 只拥有运行连续性

Lane 的存在依据是独立生命周期：它有稳定 ID、串行队列、continuation、证据账本和前案判断。它不拥有
Hypothesis 真伪，也不把目录接近直接升级为业务相关。这样调度和判断保持分离。

### 3.2 Review 2 只收敛

如果 Review 2 可以不断新造问题，它会重新变成第二个 Unit Review，完成契约和预算都会失控。发现材料
暗示另一个问题时，只能作为当前 Hypothesis 的证据或不足理由；新问题仍应由 Unit Review 产生。

Review 2 是可替换的验证策略。若未来模型能在一次 Unit Review 中稳定完成发现与验证，可以缩短或
移除 Review 2，而不改变 Hypothesis、Assessment、Trial 和 Finding 的语义。

### 3.3 Prior delivery 属于 novelty

Forge 已有评论是“已经交付”的事实，不是待办清单。调用方应把当前 PR 已有评论转为 history Clue；
Review 2 据此标记 `already_delivered`。跨 CI revision 的状态必须来自 Forge 等外部事实源，不能依赖
上次容器中的本地 Session 文件。

### 3.4 可观测性必须覆盖中间阶段

Session 在 Hypothesis 解析、Lane 分配、每次 Assessment 提交和 Trial 决策时追加 artifact。固定
Hypothesis 可独立重放 Review 2，不必每次重跑昂贵的 Unit Review。评测至少观察：

- Hypothesis 数、Unit 完成率和未评估数；
- Lane 数、每个 Lane 的 Hypothesis 数、Review 2 轮次和耗时；
- 四轴拦截分布及 receipt 缺失数；
- 被拦截样本中的 missed，以及穿过 Trial 的 wrong / repeat。

## 4. 试验方向：按判断视角拆分 Review 2

大型、跨模块 Hypothesis 可能需要分别核查语法/API、局部行为、跨模块影响或业务契约。未来可让多个
聚焦 execution 读取同一 Hypothesis，各自提交局部判断，再合成完整 Assessment。

这仍是试验方向：固定开销、冲突合并和业务背景缺失可能抵消收益。当前默认仍由一个 Lane execution
完成四轴判断，先用 labeled corpus 证明拆分能降低 wrong 且不增加 missed，再扩展模型。

## References

- [`kernel.md`](kernel.md) — 两阶段 Review 与 Trial 的总体位置
- [`unit_review.md`](unit_review.md) — Hypothesis 如何从 Unit Review 产生
- [`harness.md`](harness.md) — Execution continuation、typed message、Session 与 Viewer
- [`../eval/README.md`](../eval/README.md) — 标签、重放和阶段评测

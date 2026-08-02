# Unit Review：在有界探索中提高速度与效果

> Review 1 优化设计。Unit 与上下文如何形成见 [`unit-model.md`](unit-model.md)；Review 2 的
> Assessment 完成纪律见 [`hypothesis_review.md`](hypothesis_review.md)。

## 理念 / 目标

Unit Review（Review 1）的职责是从一个代码行为作用域中发现**值得独立复核、可以被证伪的
问题主张**，不是在一个 loop 内完成终审：

```text
Clue / Unit == Unit Review ==> Hypothesis ==> Lane / Hypothesis Review
                                             ==> Assessment == Trial ==> Finding
```

Review 2 已经负责补证、反驳、归因、价值判断和去重，因此 Review 1 应优化为**有界的线索筛查**：

- 保留重要问题的召回，不把“不确定”误当成“不能报告”；
- 尽早排除没有具体 trigger / impact 的空泛可能性；
- 将工具调用集中在少量有价值的 lead 上；
- 每个 Unit 都形成明确终态，不让轮次耗尽伪装成 clean；
- 以相关上下文密度换效果，不以无限搜索和无限预载换安全感。

这里的 lead 只是 Unit Review 内部的临时调查状态，不新增公开领域对象。第一个需要持久化、进入
Lane 调度和 eval 的结果仍然是 Hypothesis。

## 问题画像

一次 21 Unit 的真实回放暴露了下面的基线。数字只用于说明问题形状，不作为长期固定阈值：

| 指标 | 结果 |
|------|-----:|
| Unit | 21 |
| completed / truncated | 15 / 6 |
| Main LLM 请求 | 504 |
| Review 1 输入 token | 约 1250 万 |
| 只读调查工具调用 | 598 |
| Hypothesis | 21，来自 9 个 Unit |
| 无 Hypothesis 的 Unit | 12，仍消耗约 540 万 token、254 轮 |
| Hypothesis 首次提交 | 全部发生在第 22～30 轮 |
| `search_code` | 300 次，其中 161 次无命中或近空 |
| `read_files` | 239 次 |

第 1～10、11～20、21～30 轮分别消耗约 307 万、467 万、472 万输入 token；第 10 轮之后占
Review 1 输入成本约 75%。这不是“开局初始上下文太大”单独造成的，而是工具历史和静态任务内容
随着对话被反复发送。

一个典型 clean-path Unit 只修改了 `finding.Finding` 的注释。模型第一轮已经判断变更很小，之后
仍检查 scan runner、finding hook、resolver 和 collector，直到第 28 轮才结束，最终没有产生
Hypothesis。说明模型具备早期判断能力，但当前执行协议没有给它明确、可执行的停止标准。

### 当前表现不支持的结论

- **不是当前 Unit 源码没给够。** 239 次 `read_files` 中只有少量是在重复读取当前 Unit；typed
  File 消息已基本解决“先把评审对象再读一遍”。
- **不是工具经常失败。** 这次主要调查工具没有执行错误，成本来自成功但过度的探索。
- **不能简单归因于模型能力。** 模型多次早已得到“没有具体问题”的结论，却继续执行
  “再检查一项”；优先要修的是控制协议。
- **不能把 0 Finding 当成效果好。** incomplete Unit 和未进入后续 Assessment 的 Hypothesis
  都会造成静默漏报。

## 根因

### 1. Review 1 仍承担一阶段终审式调查

system prompt 要求跟随多条 plausible path，独立 Plan 又先生成一批风险 lead。模型自然倾向于
把每条路径查到底，甚至在已经没有具体怀疑时继续理解整个架构。Review 2 建立后，这种重复求证
已不再是正确分工。

### 2. 轮次是总上限，不是调查预算

当前一个 Unit 最多 30 轮，但没有“一个 lead 可以花多少轮”“何时必须停止新增调查”的约束。
wrap-up 只在接近上限时以文本提示注入，工具仍然可用；模型可以忽略提示继续搜索。

### 3. 结果提交和完成是两个动作

旧实现把“提交 Hypothesis”和“完成 Unit Review”拆成两个 tool call。在最后一轮提交结果时，可能已经
没有下一轮表达完成，最终被记录为 truncated。反过来，没有 Hypothesis 的 Unit 也缺少一个可以原子
表达“已检查、无主张”的终态结果。

### 4. 文件不是硬边界，但提示仍暴露全量 ChangeSet

真实回放中 21 个可评审文件形成 21 个 Unit，没有产生跨文件行为 Unit。每个 Unit 又看到完整的
`other_changed_files`，于是分别重建同一功能的全局架构，重复读取 `runner.go`、session、viewer
和 review 包。call-chain 只能表达调用邻接，无法覆盖类型引用、配置绑定和协议/schema 关系。

### 5. 上下文只在窗口压力下治理

原始搜索列表、文件内容和推理历史会在后续轮次反复进入 prompt。上下文治理主要等到接近窗口
上限才动作，无法解决“内容仍可放入窗口，但继续携带已无价值”的成本和注意力噪声。

### 6. Review 1 接收了过多低价值主张

样本中的 21 个 Hypothesis 有 14 个是 low severity，且多条属于 documentation / maintainability。
典型 trigger 是“未来维护者阅读注释可能困惑”或“模型违反 tool schema 返回畸形数据”。它们会
占用 Review 2 预算，却很少形成值得交付的 Finding。

## 目标流程

```text
Unit + Review Messages
    │
    ├── 首轮定向：理解 changed behavior，形成 0..N 条 candidate lead
    │
    ├── 有界补证：每条 lead 只走最短证据链
    │      ├── falsified / low value ──▶ 丢弃
    │      └── plausible + falsifiable ──▶ Hypothesis
    │
    └── submit_hypotheses([] | [...]) ──▶ completed UnitResult
```

lead 应按价值排序，而不是要求机械覆盖多条路径。一次补证链通常是以下之一：

- 一个精确 search 加一个相关范围 read；
- 一次 changed-symbol usage 检查；
- 一次 base/head 对比；
- 一次 spec/case/rule/requirement 核对。

当 lead 已经具备真实 trigger、可观察 impact、diff attribution 和源码锚点时就可以形成
Hypothesis；仍缺的证据写进 uncertainty，交给 Review 2 定向补证。

## 关键设计

### 1. 用完成契约限制发散，而不是靠模型自觉

Review 1 的 prompt 应明确：

- 先基于 diff 和初始上下文形成少量 candidate lead；
- 没有 lead 时立即结束，不为“更彻底”遍历仓库；
- 一条 lead 被反证后不换同义搜索继续证明；
- 不报告没有现实可达 trigger 的防御性可能；
- documentation / style 默认不进入 Hypothesis；maintainability 必须有具体故障路径或显著维护风险；
- 不要求 Review 1 把 uncertainty 清零。

“多想几个方向”可以保留为内部推理自由，不能成为必须把每个方向变成工具调用的完成条件。

### 2. 使用原子终态结果工具

Review 1 的外部结果协议收敛为一次终态提交：

```text
submit_hypotheses(hypotheses: [])
```

- 空数组表示完整检查后没有值得复核的问题；
- 非空数组一次提交本 Unit 的全部 Hypothesis；
- 调用成功即结束 Execution，不再额外等待 `task_done`；
- payload 解析或领域接收失败时不结束，让模型有机会修正一次。

Harness 只提供领域无关的 completion-tool 配置；具体工具名、参数校验和 Hypothesis schema 仍由
Runner Review 扩展拥有。只有 Runner 接受完整 payload 后才完成；空数组与非空结果使用同一条路径。

停止机制是一条显式状态机，而不是相信 assistant 文本：

```text
running
  ├─ submit_hypotheses 校验失败 ─▶ running（返回错误，允许修正）
  ├─ assistant 直接结束 ────────▶ StopGuard 注入交卷提示
  ├─ submit_hypotheses 校验成功 ─▶ completed（StopAfterTool 立即结束）
  └─ turn / deadline 耗尽 ───────▶ incomplete
```

AgentGo `BeforeTurn` 提供 turn 边界，Harness `Execution` 负责 wrap-up、completion tool、StopGuard
和停止状态；Runner 的 `HypothesisHook` 只负责整批校验与接收领域结果。三层都不互相偷走职责。

### 3. 探索预算结束后硬关闭调查工具

执行层将“探索预算”和“终态预留”分开。到达探索边界后，模型看到的工具 schema 不变，但工具
middleware 将执行能力收敛为：

```text
reject execution: search_code / file_find / read_files / file_read_diff / read_base_files
allow execution:  submit_hypotheses
```

这由 Harness tool middleware 在本地执行边界强制，而不是只追加 wrap-up 文本。保留 schema 和稳定
prompt 前缀是为了复用 provider cache；最终阈值通过 replay 调整。

不能在现有协议上直接把 30 改成 10：当前所有 Hypothesis 都在第 22 轮后提交，单独降上限会把
召回和成本一起清零，看起来快，实际是少报。

### 4. 默认去掉独立 LLM Plan

Review 1 本身就是发散阶段，第一轮可以完成 change summary 和 lead 选择。独立 Plan 既增加一次
模型调用，也会把 speculative lead 作为 Main Loop 的锚点。

真实样本中，启用 Plan 的 7 个 Unit 有 5 个 truncated；未启用 Plan 的 14 个 Unit 只有 1 个
truncated。改动规模是混杂因素，因此先以 `plan=off` 做固定 corpus 消融，不把该比例直接当成
因果结论。

若大 Unit 后续确实需要 Plan，应限制为：变更行为摘要、最多三条 candidate lead、每条唯一的
最便宜补证动作；不再生成一份全面风险清单。

### 5. 给 Unit 相关 Change，而不是全量 Change 列表

`other_changed_files` 改为有关系说明的有界列表：

```text
directly_related_changed_files:
  - path: internal/runner/unitreview/model.go
    relation: changed symbol used here

other_changed_files_count: 17
```

初期可用已有 call、usage、import/reference 和同一配置绑定结果排序；其余 diff 仍可通过只读工具
按需获取。repo map 同样应优先提供 Unit 相关切片，而不是让每个 Unit 看到相同的 run 级长列表。

### 6. 按调查价值治理上下文

Unit 与 Clue 在 Execution 之前被直接投影成 Review 1 消息，其中源码是带语义标签的 File 消息；Harness 不认识
Unit。每轮模型返回后，`read_files` 结果也会恢复成 File 消息再进入 ContextManager。在轮次已被控制后，
再处理上下文增长。

#### 当前 Prompt 组装形状（伪代码）

Runner 先构造稳定的开场消息，文件不是嵌进一大段 user 文本，而是独立的 typed message：

```text
system: <Review 1 system prompt / rules>
user:   <review task；Unit diff、Clue 摘要、源码 slot pointer>

<file_messages>
  user: File(path=A, range=..., context="code under review")
  user: File(path=B, range=..., context="related caller/callee/project context")
  ...
</file_messages>
```

进入 agent loop 后，assistant tool call 与对应 tool result 继续追加到同一条 conversation。工具结果先
由 `FromLLM` 恢复成 typed message，下一次请求时再按当前压缩等级 `ToLLM`：

```text
assistant: <assistant text, if any> + tool_calls(search_code, read_files(reads=[...]), ...)
tool:      SearchResult(query=..., hits=...)
tool:      FileBatch(files=[C, D, ...], snapshot=current)

assistant: <assistant text, if any> + tool_calls(read_base_files, file_read_diff, ...)
tool:      FileBatch(files=[C, D, ...], snapshot=baseline)
tool:      Diff(paths=[...])

... repeated agent turns ...
```

#### Review 1 工具的批量契约

Agent loop 虽然允许模型在一次响应里并行发出多个 tool call，但对 Review 1 来说，检索工具自身显式
支持批量仍更合适。模型看到一个改动后，常常能一次确定多个**彼此独立**的补证目标，例如多个相关
文件范围、多个 imported symbol 或多条调用线索；把它们分别包装成多个 tool call，仍会制造多份调用、
结果消息和轨迹节点，也无法由 provider 统一约束顺序与总输出预算。更重要的是，`reads[]` / `searches[]`
直接出现在参数 schema 里，是比“允许一次返回多个 tool call”更明确的 affordance，会持续提醒模型先收集
可并行目标、再批量补证。

因此 `read_files` 使用 `reads[]`，`search_code` 使用 `searches[]`，单个目标也走同一数组形状：

- 已经确定且互不依赖的目标放进同一调用，由 provider 并行执行并按请求顺序返回；
- 单个成员失败不影响同批其它成员；批量整体受共享输出预算约束；
- Harness 保留一个 tool call / result 配对，同时把成员恢复为 typed message，供 compaction、覆盖判断和
  trajectory eval 分别统计；
- 只有前一个结果决定后一个读取或 query 时才串行调用。批量能力不是“看到 import 就全搜”，仍由
  当前 lead 决定哪些目标值得补证。

每次调用模型前，Execution 通过 AgentGo `BeforeTurn` 在持久 conversation 上追加本轮控制消息，随后
ContextManager 做统一投影：

```text
[durable conversation above]
user: <incremental BoardDigest>                 # 仅 Review Team 试验开启且有新事实时
user: <wrap-up: stop investigation and submit> # 有限调查窗口或 turn/deadline 边界时只追加一次
user: <available file path/range inventory>     # request-only 尾消息，不写回 transcript
```

随后依次执行 covered-range 去重、typed compaction、必要时的通用 context strategy，最后统一降成 provider
消息。`system + review task` 尽量保持稳定；文件、搜索和 diff 可以从 full 降为 condensed/reference，
但 tool call/result 配对和消息顺序不变。Review Plan 是有限 lead 清单，Review 1 不因全局 turn 上限尚有
余量而继续扩散；清单完成或有限调查窗口结束后进入 wrap-up。wrap-up 之后工具 schema 仍不变，执行层只允许终态提交工具，
避免继续调查空转。

具体保留策略：

- 永久保留 Unit diff、当前 lead ledger、已确认事实和证据引用；
- 原始 search 列表在完成下一步定位后可淘汰；
- 完整 `read_files` 结果降为使用过的范围、path/line 和摘要；
- `search_code` / `file_find` 结果保留 query 和命中索引；工具无命中作为该 query 的反证保留一次，
  不在后续多轮携带完整相似词建议；
- 试验性的 Board 只共享事实或 Hypothesis，不共享“某 Unit 读过某文件”这种操作日志。

压缩等级只由 ContextManager 在预算趋紧时选择，消息自己决定对应形态。默认从尾部向前压并在够用
时停止，使 system/task 和较早消息保持稳定；已经提交的压缩不再反向展开。模型请求末尾会附一份
当前仍完整可见的 path/range/角色清单，既帮助模型复用已有源码，也作为 `read_files` 覆盖判断的同一
事实来源。

目标是降低后续 prompt 的注意力噪声，而不是为了压缩再增加一轮昂贵 LLM summary。

`read_files` 需要区分两个信号，不能合成一个“重复率”：

工具默认只提供批量形状；这样减少 tool call / 模型往返，但每个成员仍独立参与覆盖判断、typed
compaction 与轨迹统计。

- **已覆盖读取**：请求范围此刻仍完整存在于初始源码消息或先前工具结果中。Harness 直接返回复用提示，
  避免再次装入同一份内容；若范围只部分覆盖或内容已被淘汰，仍正常执行读取。
- **同路径重复读取**：一个 Unit 内多次读取同一路径。它用于发现探索回环，但不同调用可能读取不同
  行范围，不能仅凭路径相同判定为浪费。

Session debrief 同时记录实际预载的源码，以及 Unit 按 caller/callee/project 等关系
静态知道的路径。Viewer 将二者分别与 `read_files` 对比：前者判断预载是否命中，后者回答“静态分析
本可提前提供多少读取目标”，为后续扩大 caller/callee 预载范围提供数据，而不是先拍脑袋全量预载。

### 7. 试验特性：Review Team 跨 Unit 协作

`review_team` 与 `post_bulletin` 默认关闭，不属于 Unit Review 的稳定完成路径。试验开启后，多个 Unit
可以通过 run 级 Board 共享已经确认的事实和可能影响其他 Unit 的主张，但 Board 不是第二套
transcript，也不改变静态 Unit 边界：

- Bulletin 区分 `intent`、`observation`、`confirmed`，并携带 path / symbol 供相关 Unit 路由；
- 注入使用增量、相关性排序和数量上限，避免每轮广播整块 Board；
- 自动 file-read 记录等操作日志默认不共享，它们只说明“做过什么”，不说明“发现了什么”；
- 最终 Hypothesis 进入 Lane 调度和 Session；Board 只是一轮 run 内的临时协作读模型。

Board 的语义属于 Runner Review 扩展；Harness 只提供通用 event 和 turn-context provider。这样既能
让 Unit 像队友一样交换材料，又不把“代码评审团队”固化进执行内核。

### 8. 谨慎扩展 Unit 的关系轴

本次“一文件一 Unit”说明 call-chain 无法覆盖所有行为关系。可以在已有 usage/reference 事实上
增加 changed-symbol reference 候选轴：一个 changed symbol 被另一个 changed file 使用时，允许
它们组成有界行为簇。

先通过 directly-related changed files 验证收益，再决定是否真正 merge Unit。不能把整个
ChangeSet 合成一个巨型 Unit，否则只是把重复探索换成超大 prompt 和更差的覆盖公平性。

### 9. 路由以 Execution 为单位保持连续性

同一个 Unit 的不同 turn 若轮流由不同模型服务，会削弱推理连续性。可在 Execution admission 时
分配 primary model，执行内保持身份，失败时允许 failover；不同 Unit 之间继续 round-robin。
先用 Session 的 per-response alias 验证收益，优先级低于终态协议、ToolGate 和 Plan 消融。

## 落地顺序

1. **无代码基线**：固定 corpus 对比 `plan=on/off`，记录 Review 1 分阶段成本。
2. **完成协议验证**：对比 terminal `submit_hypotheses` 上线前后的完成率、轮数和 Hypothesis 召回。
3. **有界探索调参**：基于硬 ToolGate 和终态预留逐步下调 turn budget。
4. **上下文收窄**：只展示 directly-related changed files 和 Unit-scoped repo map。
5. **历史淘汰**：按 lead/evidence 生命周期压缩旧工具结果。
6. **Unit / routing 实验**：changed-reference 轴和 execution-level model lease 分别消融。

前 3 步应放在同一 candidate arm 中验证。只降轮次而不改变提交时机，会制造不可接受的漏报。

## Eval 与验收

Review 1 的效果不能用 Hypothesis 数量单独衡量。固定 workload 至少记录：

### 健壮性

- completed / truncated / timeout Unit 比例；
- 终态工具成功率；
- 未提交结果的 Unit 数；
- clean Unit 与有 Hypothesis Unit 的终态是否都可解释。

### 成本

- p50 / p95 turns per Unit；
- prompt tokens per completed Unit；
- prompt tokens per retained Hypothesis；
- 第 10 轮之后的 token 占比；
- no-Hypothesis Unit 的平均轮数；
- search 无命中率、同一目标跨 Unit 重读率；
- Review 1 wall time 与 aggregate LLM time。

### 效果

- 人工 `important/minor` 对应 Hypothesis 的召回率；
- `wrong`、`repeat` 和 `low_value` 进入 Review 2 的比例；
- Review 2 对 Review 1 Hypothesis 的 supported / contradicted / insufficient 分布；
- Trial 保留真问题的比例；
- 被早停 Unit 的 missed 抽样。

目标是：在 important/minor 召回不下降的前提下，提高 Unit 完成率，显著降低 clean-path 轮次、
后半程 token 和传给 Review 2 的低价值负担。

## 不采用的捷径

- **只增加 timeout 或工具轮次**：扩大已经失控的探索。
- **只把 30 轮改成 10 轮**：当前结果提交太晚，会直接损失召回。
- **继续预载更多整文件**：当前主要问题不是重复读取评审对象，更多静态内容会被每轮重放。
- **依赖 Review 2 清理无限 Hypothesis**：Review 2 自己有预算和完成边界，Review 1 必须控制负载。
- **把所有 changed files 合成一个 Unit**：会破坏相关上下文密度和覆盖公平。
- **用 finding 数下降证明效果提升**：可能只是 incomplete 或漏报。

实现以 `internal/runner/unitreview` 和 `internal/harness` 的通用 execution 能力为准；评测与回放
约定见 [`../eval/README.md`](../eval/README.md)。

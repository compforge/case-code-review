# Runner 模型：在预算内提高质量、减少误报

> 绿地演进设计。当前优先级是质量、上下文、压缩和预算；断点续审与接入外部 Agent
> Backend 是后续能力，不牵引当前内核边界。

## 理念

CCR 的演进主线是：**在 token 与时间预算可控的背景下，为单个 Unit 提供更多有效 context，
在 context 变长后可靠压缩，并以快速/深度等模式控制成本，从而提升质量、减少误报。**

token 直接对应费用，长 context 也会增加时延；review 耗时过长会降低它在开发闭环中的价值。
因此“更多上下文”不能脱离压缩和预算单独设计，“减少误报”也不能靠无限堆 token 获得。

CCR 已把 **Unit** 建模为一次 review loop 的评审作用域，但运行层仍混合了两种生命周期：

- **一次评审全局**：源码输入、Unit 调度、并发、总预算、Board、最终过滤与持久化。
- **一个 Unit 内部**：会话消息、工具轮次、压缩、deadline、wrap-up 与异步评论处理。

共享一个带会话可变状态的执行器，会让并发 Unit 互相影响；以文件作为预算和 checkpoint
单位，又会绕开 CCR 的核心模型。因此本设计新增两个 owner：

1. **Runner**：拥有一次评审的全局生命周期。
2. **UnitExecution**：拥有一个 Unit 的执行生命周期。

UnitExecution 不是 Harness 之外的第二套 loop；它是 Runner 对“一 Unit 一 Harness
Execution”的领域包装，负责把 UnitReview 转成 ExecutionSpec，并把 ExecutionResult 还原为
UnitResult。

Unit 由此不仅是评审作用域，也是调度、预算和质量量测的统一货币；未来 checkpoint 和恢复
也沿用同一货币。Runner
只共享只读代码服务、LLM client、TokenBudget 与 Board；conversation、compression 和
异步收尾必须由单个 UnitExecution 独占。

## 概念

| 概念 | 身份与职责 | 生命周期 |
|------|------------|----------|
| `SourceSnapshot` | 本次评审所见源码的不可变身份；包含 target、解析后的 ref 与内容摘要 | ReviewPlan 全程不变 |
| `ReviewPlan` | SourceSnapshot、全部 UnitReview 与 engine 配置摘要组成的不可变执行计划 | 一个 Runner 一个 |
| `UnitReview` | Unit + Dossier + Briefing，以及 Board interest、输入摘要和成本估算 | 计划形成时创建 |
| `UnitExecution` | 执行一个 UnitReview；独占消息、压缩、轮次、deadline 与评论异步任务 | start → terminal |
| `Hypothesis` | UnitExecution 提出的可证伪问题主张；携带触发条件、影响、证据引用与 diff 归因 | UnitExecution 内形成，Assessment 后终止 |
| `CaseFile` | 发散阶段移交给 Review 的案卷，包含相关 Hypothesis 及共享的 Change、Clue；首版一个 ChangeSet 一个案卷 | UnitResult 汇总后形成，Hypothesis Review 后终止 |
| `Assessment` | 对 Hypothesis 的证据支持度与交付价值判断，以及实际核查过的 Evidence | run 级 Hypothesis Review 终态产出 |
| `UnitResult` | 一个 Unit 的 Hypothesis、confirmed facts、usage 与 Debrief | UnitExecution 终态产出 |
| `ReviewResult` | 所有 Hypothesis 经 Assessment 选择后的最终 findings、覆盖率、总成本与停止原因 | Runner 终态产出 |
| `Attempt` | 对同一 ReviewPlan 的一次执行尝试；resume 会创建新 Attempt | started → finished/interrupted |
| `TokenBudget` | Runner 的 token 调度账本；管理 reservation、lease 与实际结算 | Runner 全程 |

`Attempt` 只解决同一 Runner 的中断恢复。允许输入或 engine 变化的跨 run 结果缓存是另一项
能力，不与 resume 混在一起。

## 流程

```text
ReviewTarget
    │
    ▼
SourceSnapshot / ChangeSet
    │
    ▼
Fragment ──merge──▶ Unit ──find clues──▶ Dossier ──brief──▶ UnitReview[]
                                                               │
                          ┌────────────────────────────────────┘
                          ▼
Runner（TokenBudget + Board + Attempt + deterministic scheduling）
    │
    ├──▶ UnitExecution ──▶ UnitResult
    ├──▶ UnitExecution ──▶ UnitResult
    └──▶ UnitExecution ──▶ UnitResult
                          │
                          ▼
               CaseFile（首版 = ChangeSet）
                          │
                          ▼
                 Hypothesis Review
                          │
                          ▼
                    ReviewResult
```

输入与输出也服从 Runner 边界：workspace/range/commit 由 Runner source 采集，full-file
scan 是 Runner 的另一输入模式；两者归一为 `Change` 后形成 Unit。Harness 只看到一次通用
Execution，通过 tool hook 把领域输出交还 Runner；`Hypothesis`、`Assessment` 与最终 `Finding`
始终属于 Runner，不进入 Harness Core。

主流程分为五步：

1. 将 workspace / range / commit 解析成可校验的 SourceSnapshot 与 ChangeSet。
2. 沿既有 split → merge → clue → briefing 链路形成不可变 ReviewPlan。
3. Runner 对 UnitReview 做确定性调度；每个 UnitReview 创建独立 UnitExecution。
4. UnitResult 只交付 Hypothesis；Runner 汇总整次变更，执行一次 Hypothesis Review 并产出
   Assessment。
5. 形成 ReviewResult，结束 Attempt，并持久化可恢复 checkpoint 与评测事件。

## 关键设计

### 1. UnitExecution 独占会话可变状态

UnitExecution 自己拥有：

- review 领域消息与 lowering 边界；
- 异步/同步 compression state；
- tool round、deadline、wrap-up 与 bulletin 限额；
- unit 内 comment collector、relocation 等异步工作；
- Unit 的 terminal outcome 与 Debrief。

共享层不得保存“当前 conversation”或“当前 compression job”。UnitExecution 只有在自己的异步
评论工作全部结束后才能产出 UnitResult；一个 Unit panic、timeout 或压缩失败不能修改其他
Unit 的消息与完成状态。

### 2. 结果状态与收尾方式正交

wrap-up 是收尾方式，不是完成结果。UnitResult 用三部分表达终态：

```text
Outcome:
  completed | incomplete | skipped | failed

CompletionMode:
  natural | forced_wrapup

Reason:
  budget | deadline | round_limit | compression_limit
  llm_error | panic | source_changed
```

forced wrap-up 后成功 `task_done` 才是 completed；没有 `task_done` 仍是 incomplete。只有
completed UnitResult 能被 resume 复用，避免截断或超时被误读为 clean。

ReviewResult 独立记录 run 级状态：

```text
State:
  completed | partial | failed | cancelled

StopReason:
  none | budget_exhausted | deadline | source_changed | infrastructure_error
```

`partial / budget_exhausted` 是正常的策略性终态，不伪装成 error；覆盖率必须同时展示哪些
Unit completed、incomplete、skipped 或 failed。

### 3. SourceSnapshot 是执行与恢复的源码锚点

- range / commit 在计划形成时解析成明确 SHA；merge commit 相对第一父提交评审，root commit
  相对 empty tree。
- workspace 摘要覆盖 HEAD、tracked/staged diff 与 untracked 内容。
- workspace 在 dispatch 前和最终输出前重新校验；发生变化时终止为 `source_changed`，不交付
  混合两个源码版本的结果。
- diff parser 显式区分 header 与 hunk：binary marker 只在 header 识别，增删只在 hunk 统计。

range / commit 的只读工具按目标 ref 读取；workspace 工具读取现场，但必须服从 SourceSnapshot
的一致性校验。

### 4. TokenBudget 管请求调度，不承诺精确账单封顶

provider 的真实 usage 只能在响应后得到，因此 TokenBudget 的契约是“没有预算 lease 就不能
发送新请求”，而不是声称实际账单绝不超过一个精确数字。

Unit admission 预留：

- 第一次 LLM 请求；
- 一次 forced wrap-up；
- 本次 run 的 Hypothesis Review 额度，只预留一次。

后续每次 LLM 请求前，根据当前 input 与最大 output 申请 lease；响应后以真实 usage 结算并释放
差额。lease 不足时停止探索并进入已预留的 wrap-up。预算耗尽后：

- 未启动 Unit → `skipped / budget`；
- 已启动 Unit → 使用收尾预留做 forced wrap-up；
- 本次 run 已产生 Hypothesis → 使用 review 预留完成最终审查；
- ReviewResult → `partial / budget_exhausted`。

输出同时给出 configured limit、reserved、estimated、actual usage 与 overrun。overrun 只能来自
已经发送的请求及本地/provider token 估算差异，不能来自预算耗尽后的继续派发。

首版调度追求确定性和覆盖公平：普通 Unit 按文件轮转，call-chain Unit 作为独立组参与轮转；
不先引入未经评测的“高价值 Unit”打分。若后续按 Dossier、history 或 scope 排序，必须作为可
消融策略进入 reviewbench。

### 5. Hypothesis 经 Review 后才能成为最终 Finding

UnitExecution 不写 run 级全局 collector，只返回 Hypothesis。Hypothesis 不是一段已经决定发布
的评论，而是一个待证伪的主张；它必须表达：

- 当前代码中真实存在的触发条件，而不是未来调用方或假设输入；
- diff 改变了什么行为，以及该行为如何产生可观察影响；
- 支持结论的源码、调用关系或契约证据；
- 问题由本次变更新增或改变，而不是仅仅存在于基线代码。

这组信息属于内部裁决模型，最终评论仍保持面向开发者的简洁表达。Unit loop 可以把未完成求证的
线索留在 Debrief 或 Board，但不得用措辞上的“可能”“建议考虑”把 hypothesis 伪装成 finding。

Runner 等全部 Unit 终态后汇总本次变更，由独立的 Hypothesis Review Execution 统一审查全部
Hypothesis。它看到 ChangeSet、Hypothesis 引用的 Dossier/Briefing，以及可用的
spec/case/rule/requirement；需要完整 diff、变更前后源码或调用路径时，只能使用 CCR 控制的
只读代码服务定向补证。Assessment 分开表达：

- `support = supported | contradicted | insufficient`：证据是否支持主张；
- `attribution = caused | pre_existing | unknown`：问题是否由本次变更事实上造成；`caused` 不判断
  主观意图，反事实标准是撤销当前 diff 后，trigger 或 impact 是否会消失或实质改变；
- `value = actionable | low_value | unknown`：主张若成立，是否值得交付；
- `novelty = new | duplicate_in_case | already_delivered`：是否已在本案卷或更早 revision 交付。

模型提交的 Evidence 是判断说明，不是工具调用证明。Hypothesis Review 每次成功读取 diff、baseline
或相关源码时，由 Runner 记录带工具调用 id、证据类型和引用对象的 Evidence Receipt；模型不能
自行伪造 receipt。Trial 仅允许 `supported + caused + actionable + new`，且具备
与 Hypothesis 锚点文件匹配的 diff receipt 的主张生成最终 Finding。`insufficient` 不等于错误，
只表示它还没达到公开 finding 的证据门槛；`supported + low_value` 则表示问题可能真实，但
交付收益不足。补证按主张缺口取材料：
执行路径问题查 caller、validator 和 wire contract；变更归因查 base/head；意图问题查
spec/case/rule/requirement；语言或库行为优先交给确定性分析器或契约测试，不让模型凭记忆断言。
这比为所有 Unit 固定扩充上下文更省预算，也能区分“证据没提供”“没有检索”“检索后忽略”和
“上下文充分但推理错误”。

这样可以守住两条边界：

- 不同 Unit、不同文件但属于同一行为链或重复问题的 Hypothesis 始终放在一起分析；
- resume 复用的 Hypothesis 与新 Hypothesis 会重新分析，不沿用旧文件环境下的 Assessment；
- Forge 已交付 comment 作为 prior delivery 进入案卷；无论问题仍存在还是已修复，都不得重复形成
  同一 Hypothesis，只有 trigger 或 impact 不同的独立回归才算新问题。

run 级 Hypothesis Review 自己也有 checkpoint，其复用键覆盖 hypothesis/evidence digest、
SourceSnapshot 与 review engine digest；Hypothesis、证据或审查环境变化时必须重跑。后续若
规模要求拆分，按相关 Hypothesis 组成的 CaseFile 分案，不按评论锚点文件硬切。

### 6. Resume 继续同一 Runner，不做跨 run 缓存（后续）

ReviewPlan 在 Runner 内不可变。中断恢复创建新 Attempt，并依次：

1. 校验 SourceSnapshot；
2. 校验当前 model / template / tools / feature gates 与 ReviewPlan 的 engine digest；
3. 加载 Unit checkpoints；
4. 复用 completed UnitResult，重跑 incomplete / skipped / failed Unit；
5. 将 completed Unit 的 confirmed facts 恢复到 Board；
6. 调度剩余 Unit；
7. 合并复用与新增 Hypothesis，恢复或重跑 run 级 Hypothesis Review；
8. 生成新的 ReviewResult。

Board 只恢复 confirmed facts，不跨 Attempt 回放 intent 或 observation。逻辑持久化记录至少包括
ReviewPlan、Attempt start/end、Unit checkpoint、file-filter checkpoint、Board post 与
ReviewResult；物理存储形态由原子写入、viewer 查询和 eval 导出共同决定，不在模型层预设。

### 7. Review loop 的只读信任边界不变

运行模型重构不能扩大主 loop 权限。UnitExecution 仍只接收封闭的只读工具集；MCP 通用工具、
delegate host 的任意工具和 shell/文件写能力不得进入主 loop。未来扩展必须先转换成受 CCR
控制的只读代码服务或 Briefing 输入。

## 落地顺序

1. 收敛内核边界：Runner 拥有 source/scan/finding，Unit 拥有 Change，Harness 用通用
   tool hook 接收领域扩展且不依赖 Runner/Unit/Finding。
2. 将共享的 loop 执行状态下沉为 UnitExecution，移除 run 级 conversation/compression
   可变状态。
3. 建立快速/深度模式与 TokenBudget 的 admission、lease、settle、压缩和 finalization reserve。
4. 建立 Clue → Hypothesis → Assessment → Finding 结果管道。
5. 建立 ReviewPlan、UnitResult、ReviewResult 与正交 outcome 类型，并强化 SourceSnapshot。
6. 质量、成本和健壮性指标稳定后，再引入 Attempt/checkpoint/resume。

跨 run cache/reuse、MCP、delegate mode 和启发式价值调度不在本轮范围。

## 验收

- 并发 Unit 同时触发压缩时，消息、摘要和取消动作不会跨 Unit。
- 每个 Unit 恰好产生一个 terminal UnitResult 与 Debrief；incomplete 永不表现成 clean。
- 没有 lease 的 LLM 请求不能发送；预算耗尽后不再 admission 新 Unit。
- 已 admission Unit 有 wrap-up/review 额度，partial 结果携带明确 coverage 与 stop reason。
- resume 不重复执行 completed Unit，且必定重跑 incomplete/timeout/panic/skipped Unit。
- source 或 engine digest 变化时拒绝 resume；Board 只恢复 confirmed facts。
- 每条最终 Finding 都有通过完整四轴门禁的 Assessment 与匹配锚点文件的 diff receipt；存在反证、
  补证后仍为 `insufficient`、归因为 `pre_existing`、属于 `low_value` 或已经交付的 Hypothesis
  不会对外发布，分析理由可进入评测事件。
- merge commit、root commit、binary、rename、非 ASCII 与特殊 hunk diff 均有契约测试。
- `go build ./...`、`go test ./...`、`go test -race ./...` 通过。
- reviewbench 分别回放 wrong、repeat 与 important/minor 样本，量测误报拦截率、重复交付率、
  真问题保留率和补证成本；
  precision 不回退，timeout/incomplete 降低，预算下覆盖率可解释。

## References

- Harness Execution、Agent Loop、context 生命周期与请求级预算：`harness.md`
- Unit 作用域、形成轴与完成边界：`unit-model.md`
- Dossier、Clue kind × relation 与 Briefing：`context-model.md`
- review 领域消息、compression 与 lowering：`message-model.md`
- Board / Bulletin 的跨 Unit 共享边界：`cross-unit.md`
- Debrief、finding、成本与健壮性评测：`../eval/README.md`

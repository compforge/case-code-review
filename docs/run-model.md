# Review Run 模型：以 Unit 为调度与恢复货币

> 绿地改造设计，尚未描述当前实现。目标是把 OCR 的并发压缩、总 token 预算、断点续审等
> case 收敛为 CCR 自己的运行模型，而不是在 file-oriented runtime 上逐项打补丁。

## 理念

CCR 已把 **Unit** 建模为一次 review loop 的评审作用域，但运行层仍混合了两种生命周期：

- **一次评审全局**：源码输入、Unit 调度、并发、总预算、Board、最终过滤与持久化。
- **一个 Unit 内部**：会话消息、工具轮次、压缩、deadline、wrap-up 与异步评论处理。

共享一个带会话可变状态的执行器，会让并发 Unit 互相影响；以文件作为预算和 checkpoint
单位，又会绕开 CCR 的核心模型。因此本设计新增两个 owner：

1. **ReviewRun**：拥有一次评审的全局生命周期。
2. **UnitExecution**：拥有一个 Unit 的执行生命周期。

Unit 由此不仅是评审作用域，也是调度、预算、checkpoint 和恢复的统一货币。ReviewRun
只共享只读代码服务、LLM client、TokenBudget 与 Board；conversation、compression 和
异步收尾必须由单个 UnitExecution 独占。

## 概念

| 概念 | 身份与职责 | 生命周期 |
|------|------------|----------|
| `SourceSnapshot` | 本次评审所见源码的不可变身份；包含 target、解析后的 ref 与内容摘要 | ReviewPlan 全程不变 |
| `ReviewPlan` | SourceSnapshot、全部 UnitReview 与 engine 配置摘要组成的不可变执行计划 | 一个 ReviewRun 一个 |
| `UnitReview` | Unit + Dossier + Briefing，以及 Board interest、输入摘要和成本估算 | 计划形成时创建 |
| `UnitExecution` | 执行一个 UnitReview；独占消息、压缩、轮次、deadline 与评论异步任务 | start → terminal |
| `CandidateFinding` | UnitExecution 提出的待裁决问题；携带主张、触发条件、影响、证据与 diff 归因 | UnitExecution 内形成，filter 后终止 |
| `UnitResult` | 一个 Unit 的 CandidateFinding、confirmed facts、usage 与 Debrief | UnitExecution 终态产出 |
| `ReviewResult` | 所有 UnitResult 经文件级过滤后的最终 findings、覆盖率、总成本与停止原因 | ReviewRun 终态产出 |
| `Attempt` | 对同一 ReviewPlan 的一次执行尝试；resume 会创建新 Attempt | started → finished/interrupted |
| `TokenBudget` | ReviewRun 的 token 调度账本；管理 reservation、lease 与实际结算 | ReviewRun 全程 |

`Attempt` 只解决同一 ReviewRun 的中断恢复。允许输入或 engine 变化的跨 run 结果缓存是另一项
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
ReviewRun（TokenBudget + Board + Attempt + deterministic scheduling）
    │
    ├──▶ UnitExecution ──▶ UnitResult
    ├──▶ UnitExecution ──▶ UnitResult
    └──▶ UnitExecution ──▶ UnitResult
                          │
                          ▼
              path 分组 + evidence adjudicator
                          │
                          ▼
                    ReviewResult
```

主流程分为五步：

1. 将 workspace / range / commit 解析成可校验的 SourceSnapshot 与 ChangeSet。
2. 沿既有 split → merge → clue → briefing 链路形成不可变 ReviewPlan。
3. ReviewRun 对 UnitReview 做确定性调度；每个 UnitReview 创建独立 UnitExecution。
4. UnitResult 只交付 CandidateFinding；ReviewRun 按 path 聚合，以完整证据做文件级裁决。
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
- Unit 涉及文件的 review-filter 额度，同文件只预留一次。

后续每次 LLM 请求前，根据当前 input 与最大 output 申请 lease；响应后以真实 usage 结算并释放
差额。lease 不足时停止探索并进入已预留的 wrap-up。预算耗尽后：

- 未启动 Unit → `skipped / budget`；
- 已启动 Unit → 使用收尾预留做 forced wrap-up；
- 已产生 candidates 的文件 → 使用 filter 预留完成最终过滤；
- ReviewResult → `partial / budget_exhausted`。

输出同时给出 configured limit、reserved、estimated、actual usage 与 overrun。overrun 只能来自
已经发送的请求及本地/provider token 估算差异，不能来自预算耗尽后的继续派发。

首版调度追求确定性和覆盖公平：普通 Unit 按文件轮转，call-chain Unit 作为独立组参与轮转；
不先引入未经评测的“高价值 Unit”打分。若后续按 Dossier、history 或 scope 排序，必须作为可
消融策略进入 reviewbench。

### 5. CandidateFinding 经证据裁决后才能成为最终 finding

UnitExecution 不写 run 级全局 collector，只返回 CandidateFinding。CandidateFinding 不是一段
已经决定发布的评论，而是一个待证伪的主张；它必须表达：

- 当前代码中真实存在的触发条件，而不是未来调用方或假设输入；
- diff 改变了什么行为，以及该行为如何产生可观察影响；
- 支持结论的源码、调用关系或契约证据；
- 问题由本次变更新增或改变，而不是仅仅存在于基线代码。

这组信息属于内部裁决模型，最终评论仍保持面向开发者的简洁表达。Unit loop 可以把未完成求证的
线索留在 Debrief 或 Board，但不得用措辞上的“可能”“建议考虑”把 hypothesis 伪装成 finding。

ReviewRun 等相关 Unit 终态后按文件聚合，由独立的 evidence adjudicator 统一裁决 sibling
candidates。它看到完整 diff、变更前后源码、CandidateFinding 引用的 Dossier/Briefing，以及
可用的 spec/case/rule/requirement；需要补证时只能使用 CCR 控制的只读代码服务。裁决结果是：

- `confirmed`：触发条件、执行路径、diff 归因和影响均成立，生成最终 finding；
- `refuted`：存在反证，删除 candidate 并记录原因；
- `needs_context`：当前证据不足，按主张缺口做一次定向补证；仍不能确认则不对外发布。

`needs_context` 不等于错误，只表示它还没达到公开 finding 的证据门槛。补证按 claim 取材料：
执行路径问题查 caller、validator 和 wire contract；变更归因查 base/head；意图问题查
spec/case/rule/requirement；语言或库行为优先交给确定性分析器或契约测试，不让模型凭记忆断言。
这比为所有 Unit 固定扩充上下文更省预算，也能区分“证据没提供”“没有检索”“检索后忽略”和
“上下文充分但推理错误”。

这样可以守住两条边界：

- sibling Unit 的 comments 始终在完整文件 diff 上一起过滤；
- resume 复用的 candidates 与新 candidates 会重新过滤，不沿用旧文件环境下的最终 verdict。

file review-filter 自己也有 checkpoint，其复用键覆盖 path、candidate/evidence digest、
SourceSnapshot 与 adjudicator engine digest；candidate、证据或裁决环境变化时必须重跑。

### 6. Resume 继续同一 ReviewRun，不做跨 run 缓存

ReviewPlan 在 ReviewRun 内不可变。中断恢复创建新 Attempt，并依次：

1. 校验 SourceSnapshot；
2. 校验当前 model / template / tools / feature gates 与 ReviewPlan 的 engine digest；
3. 加载 Unit checkpoints；
4. 复用 completed UnitResult，重跑 incomplete / skipped / failed Unit；
5. 将 completed Unit 的 confirmed facts 恢复到 Board；
6. 调度剩余 Unit；
7. 合并复用与新增 candidates，恢复或重跑 file review-filter；
8. 生成新的 ReviewResult。

Board 只恢复 confirmed facts，不跨 Attempt 回放 intent 或 observation。逻辑持久化记录至少包括
ReviewPlan、Attempt start/end、Unit checkpoint、file-filter checkpoint、Board post 与
ReviewResult；物理存储形态由原子写入、viewer 查询和 eval 导出共同决定，不在模型层预设。

### 7. Review loop 的只读信任边界不变

运行模型重构不能扩大主 loop 权限。UnitExecution 仍只接收封闭的只读工具集；MCP 通用工具、
delegate host 的任意工具和 shell/文件写能力不得进入主 loop。未来扩展必须先转换成受 CCR
控制的只读代码服务或 Briefing 输入。

## 落地顺序

1. 建立 ReviewPlan、UnitResult、ReviewResult 与正交 outcome 类型。
2. 重建 SourceSnapshot 和 diff 状态机，补 merge/root/binary/特殊 hunk case。
3. 将共享 Runner 拆成 UnitExecution，移除 run 级 conversation/compression 可变状态。
4. 建立带证据的 CandidateFinding → evidence adjudicator → final finding 结果管道。
5. 引入 TokenBudget 的 admission、lease、settle 与 finalization reserve。
6. 引入 ReviewRun / Attempt、Unit 与 file-filter checkpoint，完成 resume。
7. 核心模型稳定后再接 Responses API、streaming、traceparent 与 background-file。

跨 run cache/reuse、MCP、delegate mode 和启发式价值调度不在本轮范围。

## 验收

- 并发 Unit 同时触发压缩时，消息、摘要和取消动作不会跨 Unit。
- 每个 Unit 恰好产生一个 terminal UnitResult 与 Debrief；incomplete 永不表现成 clean。
- 没有 lease 的 LLM 请求不能发送；预算耗尽后不再 admission 新 Unit。
- 已 admission Unit 有 wrap-up/filter 额度，partial 结果携带明确 coverage 与 stop reason。
- resume 不重复执行 completed Unit，且必定重跑 incomplete/timeout/panic/skipped Unit。
- source 或 engine digest 变化时拒绝 resume；Board 只恢复 confirmed facts。
- 每条最终 finding 都有 `confirmed` 裁决；`refuted` 和补证后仍为 `needs_context` 的 candidate
  不会对外发布，裁决理由可进入评测事件。
- merge commit、root commit、binary、rename、非 ASCII 与特殊 hunk diff 均有契约测试。
- `go build ./...`、`go test ./...`、`go test -race ./...` 通过。
- reviewbench 分别回放 wrong 与 important/minor 样本，量测误报拦截率、真问题保留率和补证成本；
  precision 不回退，timeout/incomplete 降低，预算下覆盖率可解释。

## References

- Unit 作用域、形成轴与完成边界：`unit-model.md`
- Dossier、Clue kind × relation 与 Briefing：`context-model.md`
- review 领域消息、compression 与 lowering：`message-model.md`
- Board / Bulletin 的跨 Unit 共享边界：`cross-unit.md`
- Debrief、finding、成本与健壮性评测：`../eval/README.md`

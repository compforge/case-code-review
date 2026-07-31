# Review Team：跨 unit 协作（v0 已实现，gate 默认关）

> 隐喻链全景：Clue（线索）→ Dossier（卷宗）→ Briefing（交底）→ **Bulletin（通报）** → Debrief（复盘），全部围着 **Board（案情板）**。
>
> 演进记录：本文从"碰头会 vs 案情板"的方案对比开始，中途否决了固定 phase 的碰头会（见下），定稿为 **Review Team**——共享状态（Board）+ turn 边界通信（Bulletin）+ 动态任务（cross_check），不做角色化。

## 理念

一次需求变更围绕一个主题，它的几个 unit 之间必然有协作面——同一大案分线侦查的侦探，靠案情板互通进展、避免重复跑腿。两个候选收益：

1. **跨 unit 一致性召回**：单 unit 视角看不到的矛盾（Dockerfile 工具链版本 vs go.mod、配置默认值 vs 探活端口）是 eval 基线里最硬的漏报类别（`eval/README.md` §4.2），且恰好发生在价值最高的大改动上。chain 合并只覆盖**调用相邻**的改动函数；同主题但不相邻的 unit 之间今天没有任何共享面。
2. **省重复 tool call**：briefing 预载消掉了"读自己的文件"，但不同 unit 的 loop 仍会重复执行相同查询。

**先量化再设计**（§7 方法论）：post-preload 时代 68 个多 unit session（3150 次 main-task tool call）实测，**跨 unit 精确重复的 (tool, args) 只占 5.9%**，最重 session 8-15%。结论：收益 2 是小奖品——缓存只省 tool 执行，省不掉发起它的 LLM 轮次。**真正的奖品是收益 1（质量）。**

对"多 Agent 团队"的总判断在 ccr 语境下的收敛：

- ccr 的**执行层并行已经存在**（一 unit 一 loop）；缺的是**共享面**与**动态任务**，不是角色化。
- 北极星检验（"是否让 unit 更一等"）：共享面挂在 run 与 unit 的既有模型上（msg 消息类型、生命周期挂点），不引入平行 actor 模型。
- 早期版本以"分钟级 loop 没有轮询窗口"否决过 in-run 通信——**turn 边界寄生方案修正了这一半**（轮询点寄生在本就存在的 turn 循环上，零调度成本）；仍然成立的另一半是：角色扮演无收益（业界扫描）、跨进程 mailbox/文件锁不适用（ccr 是同进程 goroutine）。

## 业界扫描（2026-07）

围绕"multi-agent 能否提高 review 质量与速度"的证据，按结论组织：

**质量收益有三条被验证的路径，没有一条是"角色化团队"：**

1. **Generator-verifier（否证者）**：Cognition 报告 review agent 平均每 PR 抓 2 个 bug（58% severe）；CodeAgent 论文的消融显示大幅收益来自 QA-Checker 监督者（去掉它确认率 92.96%→73.23%），**而不是 CEO/CTO/Coder 那套角色扮演**；Refute-or-Promote 提出对抗式否证（kill mandates、context 不对称、跨模型 critic），且其最有效的误报杀手是**一次实证测试**而非更好的对抗话术。
2. **并行分工靠上下文分桶，不靠角色**：Anthropic 的 +90.2%（research 任务）中 80% 的方差由 token 用量解释——multi-agent 本质是"把更多 token 花在多个独立 context window 里"；商业工具中 Greptile 的形态是"代码图 + 并行 agent 各审一块"——恰是 ccr 的 unit 分桶 + codegraph 已有形态。
3. **专业化按 stage 不按人设**：AutoReview（FSE'25）的三 agent 是 Detect→Locate→Repair 流水线专业化（F1 +18.7%）；CodeRabbit 是 AST/SAST/LLM 多层管道。没有一家商业 review 工具 ship 了带 mailbox 的角色团队。

**两个直接影响设计的负面结果：**

- **辩论/多轮共识有反效果**："More Rounds, More Noise" 实测多轮 review 召回小升、精度大降，**最优轮数 = 1**；同源模型投票在"合理但错"的答案上收敛，共识放大共享错误。→ cross_check 任务是"一轮核查 + kill-mandate 纪律"，不是辩论。
- **MAST 失败分类**（1600+ trace）：spec/设计缺陷 41.8% + agent 间失调 36.9% + 验证缺失 21.3%——协调本身是主要失败源。→ 消息分级、observation 不得作事实引用，由 lowering 统一盖章。

**速度**：并行 wall-clock 收益 ccr 已经拿到（unit 并发）；multi-agent ≈ 15× chat token 的教训要求 board 注入必须封顶、增量、定向。**Claude Code Agent Teams**（lead/共享任务表/mailbox/文件锁）的官方定位是交互式长会话，其文件系统机制是**跨进程税**——ccr 同进程 goroutine 不必交。

**Superpowers v6 的信任边界经验（2026-07，外部背书）**：一个自主 reviewer 被上游影响时，审查是否还独立？其结论——**可靠的 AI 审查不是让审查者"更听话"，而是结构上不让它接触会污染判断的东西**——正是本设计分级/隔离机制的依据。三条直接对应：

- 事故一（controller 把缺陷预描述成 Minor、reviewer 接受）= **cross_check 消费 bulletin 时的命门**：一条 `observation` 级通报若被当成事实，就是这个事故的复现（MAST"misusing peer input"）。答案就是 D3 的**三级消息 + lowering 统一盖章"未确认观察不得作为事实引用"**——不靠 reviewer 自觉，靠输入结构。
- 其"末尾独立宽审查必须与 mid-run controller 隔离、是不同信息视野下的独立裁决" = cross_check **输入为 debrief/finding 结构化数据、不重放各 loop 对话**的设计依据（交接衰减 vs 独立视野）。
- 其"reviewer 只读、模型必须显式声明"两条：ccr 主 loop 已结构只读（`AGENTS.md` 关键约定 6），但 **cross_check 派生新 loop 时必须显式声明模型、不继承**——把这条写进 v1 的任务派生契约（避免 superpowers 事故二"26 个 reviewer 继承最贵 tier"）。

## 定稿架构

### 总映射：team 要素 vs ccr 现状

| Team 要素 | ccr 对应 | 增量 |
|---|---|---|
| Teammates | unit loop（已有：独立 context、并发） | 无 |
| Lead 拆任务 | split/merge（已有：确定性，比 LLM 拆得好） | 无（见 D5） |
| Shared Task List | dispatchUnits 的静态任务集 | **动态派生**（cross_check，v1） |
| Mailbox/通信 | 无 | **Board + Bulletin**（v0） |

### 决策记录

| # | 决策 | 内容 | 依据 |
|---|---|---|---|
| D1 | 进程模型 | 内存 Board（mutex 结构体）+ msg 类型；板事件落 session（`board_post`/`board_pull` 记录）只为 eval，不为通信 | 同进程 goroutine；文件/锁/mailbox 是跨进程税 |
| D2 | 读写不对称 | **发布**：引擎自动提取事实（读了 X/报了 Y，零成本）+ `post_bulletin` tool 发判断（只有模型知道它怀疑什么）；**消费**：turn 边界注入定向增量，**不做 pull tool** | 递线索是 push 形状（模型无法查询它不知道存在的东西）；check_board 轮次 = 刚消灭的 fetch 成本；repeated_reads/wrap-up 证明可选行为不可靠（MAST"ignoring peer input"入口） |
| D2b | cross_check 派生显式声明模型 | 派生任务的模型显式指定、不继承当前 loop | superpowers 事故二：省略模型静默继承最贵 tier；v1 任务派生契约的一条 |
| D3 | 路由与分级 | symbols/paths 交集定向抄送；intent/observation/confirmed 三级，lowering 统一盖章"未确认观察不得作为事实引用" | 5.9% 重复率 vs N 倍广播 token；传染性误报防线 |
| D4 | 动态任务 | teammate 可派生 `cross_check(units, 疑点)` 任务，空闲 worker 领走跑新 loop——**这是碰头会的正确形态**：不是每次都开的固定 phase，而是有事才碰头、发现者带上下文提出（交接衰减最小）。碰头会固定 phase 方案已否决，其 kill-mandate prompt 纪律遗产归 cross_check | 固定 phase = 无证据的常态成本；动态派生 = 按需 |
| D5 | Lead | v1 不设 LLM Lead：任务分解已确定性且更优；汇总归 collector + review-filter；协调归任务队列。真 Lead 等 v1 数据 | Anthropic orchestrator 价值在开放式分解，review 不是；Lead 是 MAST 协调失败的最贵入口 |

### 消费侧两道闸（board 信息多，怎么拿、怎么控）

**闸 1 · 关注面 = Dossier（拿相关的）**：unit 的利益图不用新算——

```
interest(unit) = 自己的 paths + AllSymbols      ← 身份
              ∪ clue_refs                       ← 邻居（caller/callee/owner/used，dossier 现成）
              ∪ usage-sites 命中文件             ← 谁引用我（briefing 已算过）
```

Bulletin 带路由键（tool 参数必填；自动层从 tool call 免费提取），匹配 = 集合求交，键是 symbol-id 与精确路径（无短名撞车）。打分：symbol 命中 > path 命中，confirmed > observation > intent 作乘数。**context-model 的 Relation 轴本来就是"这个 unit 关心什么"的定义——board 路由是它的第二个消费者。**

**闸 2 · 容量四道防线（相关但太多）**：

1. **源头小**：bulletin 发布即摘要+指针（text 封顶；事实型通报的内容本来就是 path+range，要全文自己 file_read）——板上只有卡片，没有大对象；
2. **增量游标**：per-订阅者 last-pull 游标，turn 边界只注新增；板面安静 = 零 token（"每 turn 增量"支配"每 N turn 查全量"）；
3. **双封顶**：每 turn top-K 条（按分）+ 字节上限；溢出不积压（陈旧通报失去价值），注一行"板上另有 N 条相关"——若 debrief 显示模型对此有反应，即是加 `check_board` 查询 tool 的证据（v1 后门，先不做）；
4. **驱逐（白捡）**：注入的 BoardMsg 是 typed 消息，自动进 file_evict 的可再生性驱逐序（板面可重拉，与 File 同档，先于推理被 stub）——C1/C2 的机器直接复用。

v1 第五道：板面自身压缩（同主题 supersede，与 File dedup 同构；intent 被后续 confirmed 关闭）+ per-loop 累计 board 预算（超了只放 confirmed）。

### Bulletin 语义

Bulletin = loop 干活期间贴到板上的对同伴可见的进展通报，字段 `{from, turn, level, symbols/paths, text, ref}`。三概念分辨：

| | 时机 | 方向 | 受众 |
|---|---|---|---|
| Briefing 交底 | 开工前 | 编排器 → 我 | 本 loop |
| Bulletin 通报 | 干活中 | 我 → 案情板 | 其他 loop |
| Debrief 复盘 | 收工后 | 我 → transcript | eval / 后验 |

final bulletin（收工通报）挂在 scope `Close()` 上——生命周期（unit-model.md 关键设计 8，已实现）即为此备好的挂点。

### 推导记录：从群聊到定稿

harness loop 视角（loop = `build_context → llm_call → tool_exec → compress`，一圈一 turn）的最糙群聊版：

```python
while condition:
    inbox = board.pull(exclude=self)   # turn 顶部拉别人的消息
    context = build_context(query, inbox)
    llm_result = llm_call(context)
    if llm_result.tool_calls:
        tool_results = tool_exec(llm_result.tool_calls)
        board.publish(scope=self.unit, intent=llm_result, result=tool_results)
    else:
        return llm_result.text
```

四个问题（token 账倒挂 / 注意力污染 / 时序偏差——turn 级通道天然传过程不传结论 / 传染性误报+不可复现）+ 三个约束（摘要化 / 定向路由 / 消息分级）→ 收敛为上面的定稿。时序偏差是 v0 试验要回答的最大不确定：并发 loop 生命周期高度重叠，bulletin 的可消费窗口可能很小——自动层（事实通报 turn 2-3 即有）能缓解多少，看数据。

## 实现选型：gate + 单引擎接缝，不 fork

1. **现有 loop 不止 7 行伪代码**：wrap-up、compression、session 落盘、token 记账、comment 管道、生命周期/debrief 全挂在引擎上，fork = 每个修复改两遍（快照竞态修复即先例）。
2. **eval 方法论要求同引擎消融**（§2.5）：机制收益与实现差异必须可分离。
3. **仓库同一答案已用四次**：plan / review-filter / relocation / typed_briefing 都是"单引擎 + gate 可选环节"。

接缝（Board 语义留在 Runner，Harness 只提供中立扩展点）：

```go
// Runner 的 UnitExecution 实现这两个 Harness 契约。
PullTurnContext(ctx, scope) []msg.Msg // turn 顶部从 Board 拉取并包成 msg.Board
HandleTool(ctx, request) (...)       // post_bulletin 等领域工具

// Harness EventSink 由 Runner 消费，在 tool 事件后自动发布事实。
```
> 板负责渲染（打分/封顶/隔离盖章都在 Registry），Runner 只把非空 digest 包成
> `msg.Board` 交给 Harness——渲染集中、执行层不知道 Board。

## 切片、验收与已知弱点

**v0（纯机制，已实现）**：`internal/harness/board`（Registry：交集打分 symbol>path、level 乘数、增量游标、top-K+字节封顶、隔离盖章渲染）+ `msg.Board`（进 `Reclaimable` 驱逐序，与 File 同档）+ Runner 的 UnitExecution 适配（turn 顶通过 `TurnContextProvider` Pull 注入、消费 Harness tool Event 后自动 Publish 事实）+ Runner 注册 unit interest（paths+symbols+clue_refs）+ debrief `board{pulled,injected_tokens,posted}` + `board_post` 落 session。gate `review_team` 默认关（experimental，语义入 registry `Experimental` 字段）。**不含**：cross_check、Lead、板面压缩。

**v0.5（怀疑通道，已实现）**：`post_bulletin` tool——模型把本 unit 范围外的跨文件疑点以 observation 级贴上板（发现者带上下文递线索，D2 的发布侧补全）。实现要点：Runner 的 UnitExecution 持有 handler（发布需要 scope 身份、turn 与每 Execution 预算，不走 Registry provider）；路由键（paths/symbols）必填、每 Execution 发帖封顶、text 截断——闸 2 的源头防线；无板或 gate 关时装配阶段直接从 tool defs 剥离，模型看不到用不了的工具。gate `post_bulletin` 默认关，依赖 `review_team`。消费侧零增量：observation 的降权乘数与"未确认观察不得作为事实引用"盖章 v0 已就位。

**v0 冒烟（2026-07-05，ed0ca39 6 文件 5 unit）**：19 条 confirmed 级 file-read 事实自动发布；路由生效——`agent.go` 被 3 个 unit 读，其 debrief 各记 pulls 3/5/10；注入 token 86-473/unit（封顶有效）。跨 unit 同文件重复读（loop 内 dedup 够不着的那类）现在互相可见——是否降低重复读，正是 v0 待答的问题。

**v1（动态任务，v0/v0.5 数据达标后）**：`post_task(cross_check)` 动态任务队列 + 板面 supersede 压缩 + 累计预算。

**验收（先写死）**：回归集 gate on/off——①两个跨文件实锤（builder 版本 vs go.mod、健康端口错配）的召回；②精度不回退；③token 增幅 <15%；④pulled→行为改变的证据（如 pulled 后该 unit 少了对应的重复 tool call）。**①或④至少中一个才推进 v1；若 pulled 高而行为无变化，止损。**

**已知弱点（接受后开工）**：bulletin 触发的 loop 内 finding 无法单条归因（cross_check 的可以——独立 scope），验收靠回归集总量对比 + pulled/行为相关性分析。

## References

- 漏报证据与方法论：`eval/README.md` §4.2（跨文件一致性）、§7（数据驱动优先级）、§9（replay 首个翻转许可证先例）
- 既有共享面与机器：briefing（`docs/context-model.md` 关键设计 8）、msg model 与驱逐（`docs/message-model.md`）、unit 生命周期（`docs/unit-model.md` 关键设计 8）
- 采集面：debrief / finding 记录（`eval/README.md` §8）
- 独立 backlog（与 team 无依赖）：run 级 briefing 补给——从 debrief 挖"多个 unit 都在搜什么"升级为 run 级预载（repo_map 先例）
- 业界扫描来源：[Anthropic multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system) · Cognition [Don't Build Multi-Agents](https://cognition.com/blog/dont-build-multi-agents) / [Multi-Agents: What's Actually Working](https://cognition.com/blog/multi-agents-working) · [MAST: Why Do Multi-Agent LLM Systems Fail (arXiv:2503.13657)](https://arxiv.org/abs/2503.13657) · [CodeAgent (arXiv:2402.02172)](https://arxiv.org/html/2402.02172v4) · [AutoReview (FSE'25)](https://dl.acm.org/doi/10.1145/3696630.3728618) · [More Rounds, More Noise (arXiv:2603.16244)](https://arxiv.org/pdf/2603.16244) · [Refute-or-Promote (arXiv:2604.19049)](https://arxiv.org/html/2604.19049) · [Claude Code Agent Teams docs](https://code.claude.com/docs/en/agent-teams) · [Greptile vs CodeRabbit](https://www.greptile.com/greptile-vs-coderabbit) · Superpowers v6.0.0（SDD reviewer 信任边界：输入隔离 / reviewer 只读 / 模型显式 / 独立末尾复审——本地 `superpowers/skills/subagent-driven-development/task-reviewer-prompt.md`）

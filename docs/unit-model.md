# Unit 与评审上下文

## 1. 理念 / 概念

CCR 不把“文件”直接等同于评审任务。文件是源码的存储边界，**Unit 才是一次行为审查的边界**：
它应尽量容纳理解同一项行为变化所需的改动，同时避免把互不相关的变化塞进同一个 agent loop。

相对 OCR 固定的“一文件一个 loop”，Unit 有两个直接收益：

1. **粒度更灵活**：可以是函数、文件或跨文件 call-chain，按真实行为边界组织评审；
2. **减少重复 loop**：若 `file1.func1` 调用 `file2.func2`，两个 file loop 最终都要读取彼此，
   不如把两处改动形成一个 call-chain Unit，一次理解并判断，从而节省 token 和时间。

CCR 追求 **Review 1 loop 数不多于需要评审的改动文件数**：单文件改动收为一个 Unit，跨文件
协作改动通过 call-chain 合并后可以进一步减少 loop。

这种可变粒度依赖 Language Knowledge 提供足够可靠的 caller/callee 关系；随着
[`gotreesitter`](https://github.com/odvcencio/gotreesitter) 的跨语言解析、符号定位和调用分析能力成熟，
Formation 才能把分散在不同文件、但共同完成一个行为变化的 Fragment 实用地归入同一 Unit。

Project Knowledge 先用 Repository / Component / FileRole 解释文件的稳定项目职责，再把 source 交给
Unit formation，把 manifest / lock 等项目事实投影为 Clue。Component 是静态项目边界，Unit 是一次
diff 动态形成的行为边界；具体分类与 snapshot 约束见 [`project.md`](project.md)。

```text
Git Change ─▶ Component / FileRole
                   ├─ source ─▶ Fragment ─────────────▶ Unit{Fragments, Clues, Review State}
                   │     └─ entrypoint / handler ─▶ project Clue ─────▲
                   ├─ manifest / lock ────────────▶ project Clue ─────▲
                   └─ version ─▶ no Unit Review
```

| 对象 | 语义 |
|---|---|
| `Change` | Git 层的一份文件变更 |
| `Fragment` | Change 中可独立定位的改动片段，通常对应函数、类型或残余文件区段 |
| `Unit` | 一次 run 的评审聚合根：稳定行为范围，以及逐阶段追加的事实快照、Hypothesis、Assessment 和 Trial decision |
| `Clue` | 与 Unit 有关系、可用于判断契约的事实或线索 |

Project、Unit 和上下文回答三个不同问题：文件在项目中是什么、哪些目标改动一起审、审它时带哪些
事实。Project 事实可被不同 Review 阶段复用；Unit 只保存与当前行为范围有关的投影。

## 2. 流程

### 2.1 先区分 Unit target 与项目上下文

每个 Change 先由 Project Knowledge 解析所属 Component 与可组合 FileRole。当前策略把 source 作为
target，把同 Component 中变化的 manifest / lock 作为 project Clue；entrypoint / handler 等角色作为
Unit 自身的项目先验。用户显式 include 仍可提升文件，未被 Component 认领的文件继续走全局规则。

Project 分类完成后才进入 formation；Clue 在 Unit scope 最终确定后挂载，避免静态 Component 边界
替代动态行为边界。Project 只提供事实，是否形成 Unit 仍由 formation 决定。

### 2.2 从 Fragment 形成 Unit

Unit 粒度是一条从小到大的阶梯，而不是固定按函数或文件：

1. `runner/formation` 调用语言层，把选为 target 的 Change 切成可定位的 Fragment；无法可靠切分时保留文件级 Fragment。
2. 若 Unit Review 只有一个 target 文件，直接收为一个 file Unit。一次 loop 共同理解同文件内的相关改动，
   通常比机械地逐函数启动多个 loop 更快、更完整。
3. 若改动跨多个文件，先用高置信调用关系合并真正协作的 Fragment。例如 `func1` 调用另一文件
   的 `func3`，两者可形成 call-chain Unit；无关的 `func2` 保持独立。
4. 剩余 Fragment 再按最小合理作用域收敛。每条 call-chain 独立接受成本约束：加入后若不会让
   Unit 总数超过 target 文件数就保留，否则只把该 chain 退回文件级，避免一个膨胀的 chain 连带
   取消其它有效关系。

合并的目标不是追求更少 Unit，而是让每个 Unit 接近一个可独立判断的行为变化。调用图没有足够
置信度时宁可保持分离，再通过 Clue 补充邻域；错误合并会同时放大 token、推理和归因成本。

### 2.3 为 Unit 组织上下文

Clue 用两个正交维度表达上下文：

- **Relation**：事实与 Unit 的关系，如 `self`、`owner`、`caller`、`callee`、`used`、`project`。
- **Kind**：事实的来源或契约种类，如 `spec`、`case`、`rule`、`link`、`doc`、`history`、`project`。

因此“caller 的 spec”和“self 的 history”无需新增专用字段。ClueFinder 只负责发现并挂载事实，
不决定 prompt 排版；Unit 保存完整的 Fragments 与 Clues，Runner 再按预算、优先级和消息形状投影为
Review Messages。

```text
Unit
  └─ ClueFinder[]
       └─ Clue{Relation, Kind, Ref, Content}
            └─ Unit.Clues
                 └─ Review Messages
```

### 2.4 初始消息与按需工具

初始消息只预载高确定性、高复用的信息：Unit 自身 diff、必要源码、直接契约和少量高价值邻域。
未知路径和低概率细节由 Review loop 通过只读工具按需获取。实际进入上下文或由工具成功读取的仓库
事实按其真实形状追加到 Unit：文件内容是 `FileSnapshot`，额外变更切片是 `DiffSnapshot`，检索输出是
`SearchResult`；Unit 自身目标 diff 仍只来自 Fragment，避免重复事实源。Review Messages 是这些完整
事实到 Execution 的可压缩投影，不再引入一个泛化的材料对象。

这条边界同时控制两个风险：

- 全量预载会让每个 Unit 重复携带仓库材料，成本随 Unit 数放大；
- 完全依赖工具会浪费轮次重新寻找本可确定注入的事实。

当内容超预算时，应先降级为范围、摘要或可重取指针，而不是静默丢掉整个 Unit。无法完成的 Unit
必须显式标为 partial/incomplete，不能伪装成 clean。

## 3. 关键设计

### 3.1 稳定身份连接 diff、源码和契约

路径和短函数名不足以跨文件、重命名和依赖建立关系。语言层提供稳定 `symbol-id`；作者声明的
契约另保留可跨仓匹配的 `fqn`。Unit、Clue 和历史反馈优先用稳定身份连接，Forge 只剩文件锚点时
才退化到 path。

身份解析失败表示 `unknown`，不能用猜测的同名符号替代。这是防止“上下文看似丰富、实际属于
另一个函数”的基本准确性边界。

### 3.2 图事实按置信度消费

Language 负责产出 definition、reference、call edge 等源码事实；Unit 层决定这些事实能否参与合并
和上下文组织。

- 低置信文本线索可用于 repo map、搜索建议或 clue 候选。
- 类型解析后的调用边可用于 caller/callee 关系与 call-chain Unit。
- 无法判定的边保持 unknown，不升级成“确定调用”。

图既不是独立的最终产品，也不能直接控制 review loop。它是 Unit formation 和 Clue 的证据来源，
其错误成本取决于消费位置：展示错一个候选影响有限，错误合并 Unit 则会改变整个评审边界。

### 3.3 Unit 在一次 run 内是追加式聚合根

Unit 在 formation 后保持稳定身份和 Fragment 边界，并沿主链路追加四类状态：Review 实际读取的
文件/diff/搜索快照、Review 1 提出的 Hypothesis、Review 2 接受的 Assessment，以及 Trial decision。阶段包拥有
“如何产生”的逻辑，Unit 只保存“关于这个行为范围已经知道什么”，不吸收 Lane conversation、turn、
token 等执行状态；后者仍属于 Harness Session。

快照始终保存完整 raw 内容；Runner 为不同 Execution 投影独立的 File/Diff/Search AgentMessage，由消息
类型定义压缩方式，并按当前 Review 阶段赋予保留优先级。消息压缩不会反向修改 Unit，因此 Review 2
和 Trial 看到的领域事实不受某次 prompt 投影影响。

Hypothesis `ID` 标识来源 Unit 中的一次主张，`Fingerprint` 标识跨 Unit / revision 的同一底层 claim。
因此每个 Unit 都能保留自己的完整轨迹，而 Trial 仍可按 Fingerprint 去重交付。并发调度只改变执行
时机，不改变状态的语义归属。默认关闭的 Review Team 试验可以通过 Board / Bulletin 交换跨 Unit
主张，但同样不得反向修改已确定的静态 Unit 边界。

### 3.4 效果评估不能只看 comment 数

Unit 设计同时影响召回、准确率和成本，至少应观察：

- 原始 diff file 数、实际 review file 数与 Review 1 loop 数，区分文件过滤和 Unit formation 各自
  节省的 loop；
- Fragment 到 Unit 的合并比例与错误合并样本；
- 每个 Unit 的预载字节、工具调用、token 和完成状态；
- 有真实 finding 的 Unit 是否获得了足够契约和邻域；
- 被合并或被拆开的 Unit 是否改变 wrong / missed；
- partial Unit 是否被单独统计，而非混入 clean。

## References

- [`kernel.md`](kernel.md) — CCR 总体主链路与领域边界
- [`project.md`](project.md) — Repository、Component、FileRole 与项目事实投影
- [`language.md`](language.md) — symbol、definition、reference 与图事实的生产边界
- [`unit_review.md`](unit_review.md) — Unit 进入 Review 1 后的探索、收敛与效果优化
- [`hypothesis_review.md`](hypothesis_review.md) — Hypothesis 在 Lane 中的复核与 Trial

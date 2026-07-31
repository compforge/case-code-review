# Unit 与评审上下文

## 1. 理念 / 概念

CCR 不把“文件”直接等同于评审任务。文件是源码的存储边界，**Unit 才是一次行为审查的边界**：
它应尽量容纳理解同一项行为变化所需的改动，同时避免把互不相关的变化塞进同一个 agent loop。

```text
Git Change ─▶ Fragment ─▶ Unit ─▶ Dossier ─▶ Briefing
                 改了什么    审什么     已知什么      本轮看到什么
```

| 对象 | 语义 |
|---|---|
| `Change` | Git 层的一份文件变更 |
| `Fragment` | Change 中可独立定位的改动片段，通常对应函数、类型或残余文件区段 |
| `Unit` | 应由一次 Unit Review 共同判断的行为范围 |
| `Clue` | 与 Unit 有关系、可用于判断契约的事实或线索 |
| `Dossier` | 按关系与种类组织后的 Unit 案卷，不受 prompt 形状限制 |
| `Briefing` | 从 Dossier 投影出的本轮只读上下文 |

Unit 与上下文是两个正交问题：Unit 决定“哪些改动一起审”，Dossier 决定“审它时带哪些事实”。

## 2. 流程

### 2.1 从 Fragment 形成 Unit

Unit 粒度是一条从小到大的阶梯，而不是固定按函数或文件：

1. 语言层把 Change 切成可定位的 Fragment；无法可靠切分时保留文件级 Fragment。
2. 若一次 review 只改一个文件，直接收为一个 file Unit。一次 loop 共同理解同文件内的相关改动，
   通常比机械地逐函数启动多个 loop 更快、更完整。
3. 若改动跨多个文件，先用高置信调用关系合并真正协作的 Fragment。例如 `func1` 调用另一文件
   的 `func3`，两者可形成 call-chain Unit；无关的 `func2` 保持独立。
4. 剩余 Fragment 再按最小合理作用域收敛，避免碎片遗失，也不把整个 diff 合成一个巨型 Unit。

合并的目标不是追求更少 Unit，而是让每个 Unit 接近一个可独立判断的行为变化。调用图没有足够
置信度时宁可保持分离，再通过 Clue 补充邻域；错误合并会同时放大 token、推理和归因成本。

### 2.2 为 Unit 组织上下文

Clue 用两个正交维度表达上下文：

- **Relation**：事实与 Unit 的关系，如 `self`、`owner`、`caller`、`callee`、`used`。
- **Kind**：事实的来源或契约种类，如 `spec`、`case`、`rule`、`link`、`doc`、`history`。

因此“caller 的 spec”和“self 的 history”无需新增专用字段。ClueFinder 只负责发现并挂载事实，
不决定 prompt 排版；Dossier 先保存完整语义，Briefing 再按预算、优先级和消息形状投影。

```text
Unit
  └─ ClueFinder[]
       └─ Clue{Relation, Kind, Ref, Content}
            └─ Dossier
                 └─ Briefing / typed file messages
```

### 2.3 静态 Briefing 与按需工具

Briefing 只预载高确定性、高复用的信息：Unit 自身 diff、必要源码、直接契约和少量高价值邻域。
未知路径和低概率细节由 Unit Review 通过只读工具按需获取。

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

图既不是独立的最终产品，也不能直接控制 review loop。它是 Unit formation 和 Dossier 的证据来源，
其错误成本取决于消费位置：展示错一个候选影响有限，错误合并 Unit 则会改变整个评审边界。

### 3.3 Unit 在一次 run 内是有生命周期的对象

Unit 在形成后保持稳定身份，并沿主链路逐步获得 Dossier、Briefing、Execution 结果和 Hypothesis。
并发调度只改变执行时机，不改变 Unit 的语义归属。跨 Unit 共享的信息由 Unit Review 的 Board /
Bulletin 机制表达，不反向修改已确定的静态 Unit 边界。

### 3.4 效果评估不能只看 comment 数

Unit 设计同时影响召回、准确率和成本，至少应观察：

- Fragment 到 Unit 的合并比例与错误合并样本；
- 每个 Unit 的预载字节、工具调用、token 和完成状态；
- 有真实 finding 的 Unit 是否获得了足够契约和邻域；
- 被合并或被拆开的 Unit 是否改变 wrong / missed；
- partial Unit 是否被单独统计，而非混入 clean。

## References

- [`kernel.md`](kernel.md) — CCR 总体主链路与领域边界
- [`language.md`](language.md) — symbol、definition、reference 与图事实的生产边界
- [`unit_review.md`](unit_review.md) — Unit 进入 Review 1 后的探索、收敛与跨 Unit 协作
- [`hypothesis_review.md`](hypothesis_review.md) — Hypothesis 跨 Unit 归案后的复核与 Trial

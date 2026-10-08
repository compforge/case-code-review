# Unit 与评审上下文

## 1. 理念 / 概念

CCR 的 Unit 是一次 run 的评审聚合根，围绕一组相关变更保存 Clue、实际读取的事实和各阶段结果。
通用的 Change / Fragment / Unit 定义、关系强度及聚合规则由 repocli 维护，见
[Fragment 与 Unit](https://github.com/compforge/repocli/blob/main/docs/units.md)。

repocli 负责 `Diff → Fragment → Unit`，提供面向任意调用方的仓库事实与组合方法。
CCR 先捕获 `Diff`，按评审范围筛选变更，再调用 `FormUnits`；图、契约和源码工具共用捕获版本。
`RepoUnit` 是 `repocli.Unit` 的别名，评审 Unit 组合它与 Clue、预算和运行状态。
CodeGraph 提供组装时使用的代码关系与 namespace 证据。
CCR 负责选择评审目标、提供 token 预算、将仓库 Unit 适配为评审 Unit，并在范围确定后加载上下文。
共享 import 可以出现在多个评审 Unit 中，目标编辑在唯一 Fragment 目录中校验覆盖。

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
| `Fragment` | Change 中可独立定位的改动片段，对应声明、导入/导出绑定或残余文件区段 |
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

### 2.2 接入仓库 Unit

Formation 调用 repocli 的拆分与组装能力，复用当前 run 的前后版本图。库定义 Fragment 类型和
合并策略；Language 为这些源码范围关联 CCR 的图身份，供契约与邻域查询使用。

CCR 提供 diff token 计量和合并预算，保留分阶段数量、namespace 证明、预算边界与解析缺口。
CCR 将 `max(进入 formation 的不同文件数, --max-units)` 作为数量软目标传给 repocli。
强关系可以聚合出更少的 Unit；大小预算可能阻止降到目标，此时报告超限并保留全部改动。
Session 的 grouping 阶段记录库调用的实际耗时，后续 review 状态仍由 CCR 管理。

聚合完成后，CCR 跳过前后两侧 element counts 满足 `import count == total count` 的 Unit，包含空 counts；
混有代码、unknown 或 whitespace 的 Unit 保留。repocli 的完整编辑覆盖不变，过滤数量记录在
`skip_import_only` 步骤中，最终 Unit 数与超限标记以实际评审范围为准。

最终范围确定后才创建评审 Unit 并运行 ClueFinder。大小超限不等于跳过评审；Harness 的上下文、
时间和 token 预算控制执行，无法完成时报告 incomplete。分属不同 Unit 的图关系可投影为有界的
跨 Unit 线索，支持模型按需补证。

### 2.3 为 Unit 组织上下文

Clue 用两个正交维度表达上下文：

- **Relation**：事实与 Unit 的关系，如 `self`、`owner`、`caller`、`callee`、`used`、`project`。
- **Kind**：事实的来源或契约种类，如 `spec`、`case`、`rule`、`link`、`doc`、`history`、`project`。

其中 `spec / case / link / rule / doc` 是 Project Knowledge 中作者声明的 Biz Knowledge：它们表达项目
希望代码守住的业务契约、具体场景、关系和说明。Language Knowledge 负责识别注释、装饰器、
symbol-id / fqn 等语法与身份，把这些声明绑定到 definition / relation；Project Knowledge 负责声明
内容本身。两者分开后，新增语言只需实现可靠绑定，不必复制业务契约模型。

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

每个 Unit 都按自己的符号集合寻找 self / owner / used / caller / callee，上下文查询不依赖 file、func
或 related 的展示标签。邻域搜索有独立深度、数量和访问预算；改动总量变大不会关闭整批 Unit 的上下文。
已归入 Unit 的成员提供自身契约与文档，避免合并后因排除“内部邻居”而丢失证据。旧侧线索带基线标识，
旧的仓库契约从基线版本读取；删除侧 finding 保留旧路径和旧行号。

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
契约另保留可跨仓匹配的 `fqn`。Fragment 与 Unit 的目标身份复用 repocli 的版本、源码范围与补丁身份，成员顺序不改变 Unit 身份。
Clue 和历史反馈使用各自支持的身份连接，Forge 只剩文件锚点时
才退化到 path。

身份解析失败表示 `unknown`，不能用猜测的同名符号替代。这是防止“上下文看似丰富、实际属于
另一个函数”的基本准确性边界。

### 3.2 图事实按置信度消费

CodeGraph 负责产出 definition、reference、call edge 等源码事实；Unit 层决定这些事实能否参与合并
和上下文组织。

- repo map 可使用图已提供的低置信候选排序；它不从裸名匹配重新生成符号关系。
- `Exact/Scoped` 调用边可用于 caller/callee；分组也消费引用、类型和绑定关系，不宣称完整编译器语义。
- owner 沿图的语义归属或词法嵌套查找；used 按改动行选择 Reference，并沿 references / aliases 查找声明或显式公开绑定。
- 同一版本的 caller/callee、owner、used、usage 和声明文档共享图快照；新旧侧分别解释。源码项边与声明依赖边按用途选择，不重复计数。
- 无法判定的边保持 unknown，不升级成“确定调用”；contract catalog 提供作者的含义，不能通过同名命中证明源码绑定。

图是 Unit formation 和 Clue 的证据来源，
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
- 目标编辑覆盖率、Fragment 到 Unit 的合并比例、错误合并和预算切断样本；
- 每个 Unit 的预载字节、工具调用、token 和完成状态；
- 有真实 finding 的 Unit 是否获得了足够契约和邻域；
- 被合并或被拆开的 Unit 是否改变 wrong / missed；
- partial Unit 是否被单独统计，而非混入 clean。

## References

- [repocli Fragment 与 Unit](https://github.com/compforge/repocli/blob/main/docs/units.md) — 通用变更模型、关系优先级与分阶段聚合

- [`kernel.md`](kernel.md) — CCR 总体主链路与领域边界
- [`project.md`](project.md) — Repository、Component、FileRole 与项目事实投影
- [`language.md`](language.md) — symbol、definition、reference 与图事实的生产边界
- [`unit_review.md`](unit_review.md) — Unit 进入 Review 1 后的探索、收敛与效果优化
- [`hypothesis_review.md`](hypothesis_review.md) — Hypothesis 在 Lane 中的复核与 Trial
